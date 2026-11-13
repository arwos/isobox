//go:build linux

package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/vishvananda/netlink"
)

const (
	proxyTUNName    = "isobox0"
	proxyTUNAddress = "10.89.0.1/24"
	proxyTUNMTU     = 1500
	tunSetIFF       = uintptr(0x400454ca)
	tunGetIFF       = uintptr(0x800454d2)
	tunIFFTun       = uint16(0x0001)
	tunIFFTap       = uint16(0x0002)
	tunIFFNoPI      = uint16(0x1000)
)

type tunInterfaceRequest struct {
	name  [16]byte
	flags uint16
	pad   [22]byte
}

// RunProxyTUNHelper creates and configures the isolated TUN interface, passes
// its descriptor to the host supervisor, then execs the requested application
// after the host has verified its SOCKS5 data plane is ready.
func RunProxyTUNHelper(args []string) (int, bool) {
	if len(args) == 0 || args[0] != internalProxyTUNCommand {
		return 0, false
	}
	ingressGuard, nftPath, tcpPorts, udpPorts, applicationIndex, ok := parseProxyTUNHelperArgs(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "isobox: invalid internal proxy TUN helper arguments")
		return 125, true
	}
	runtime.LockOSThread()
	if err := verifyProxyTUNCapabilities(capSetupMask); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: proxy TUN helper lacks its temporary network capability: %v\n", err)
		return 125, true
	}
	if err := setNoNewPrivs(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot set no_new_privs before proxy TUN setup: %v\n", err)
		return 125, true
	}
	if err := unshareNetworkNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot create the proxy network namespace: %v\n", err)
		return 125, true
	}
	if err := dropProxySysAdminCapability(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot drop CAP_SYS_ADMIN after proxy network namespace setup: %v\n", err)
		return 125, true
	}
	if err := disableIPv6InNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot disable IPv6 in the proxy network namespace: %v\n", err)
		return 125, true
	}
	if err := verifyNoIPv6EgressRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: unexpected IPv6 routes remain in the proxy network namespace: %v\n", err)
		return 125, true
	}
	if err := enableProxyLoopback(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot enable loopback in the proxy network namespace: %v\n", err)
		return 125, true
	}
	tunFile, err := configureProxyTUN()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot configure the proxy TUN interface: %v\n", err)
		return 125, true
	}
	defer tunFile.Close()
	if ingressGuard {
		if err := enableNftCapabilityInheritance(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot pass firewall capability to nft: %v\n", err)
			return 125, true
		}
		filterErr := installProxyIngressFilter(nftPath, tcpPorts, udpPorts)
		if err := clearAmbientCapabilities(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot clear inherited firewall capabilities after nft: %v\n", err)
			return 125, true
		}
		if filterErr != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot install the proxy ingress egress filter: %v\n", filterErr)
			return 125, true
		}
	} else {
		if err := clearAmbientCapabilities(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot clear temporary proxy TUN capabilities: %v\n", err)
			return 125, true
		}
		if err := dropCapabilities(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot drop temporary proxy TUN capabilities before startup: %v\n", err)
			return 125, true
		}
		if err := installNamespaceSeccomp(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot prevent nested namespace creation before proxy application launch: %v\n", err)
			return 125, true
		}
	}
	program, err := exec.LookPath(args[applicationIndex])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: application executable %q cannot be found inside the sandbox: %v\n", args[applicationIndex], err)
		return 127, true
	}
	gate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: internalProxyTUNGatePath, Net: "unix"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot connect to the proxy TUN startup gate: %v\n", err)
		return 125, true
	}
	defer gate.Close()
	if err := sendProxyTUNDescriptor(gate, tunFile); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot send the TUN descriptor to the host supervisor: %v\n", err)
		return 125, true
	}
	if ingressGuard {
		if err := waitForProxyIngressReady(gate); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: proxy ingress forwarding was not ready: %v\n", err)
			return 125, true
		}
		if err := configureProxyIngressRouting(); err != nil {
			_, _ = io.WriteString(gate, "rejected\n")
			fmt.Fprintf(os.Stderr, "isobox: cannot install the marked-only pasta return route: %v\n", err)
			return 125, true
		}
		if _, err := io.WriteString(gate, "prepared\n"); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot confirm proxy ingress route setup: %v\n", err)
			return 125, true
		}
		if err := clearAmbientCapabilities(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot clear temporary proxy ingress capabilities: %v\n", err)
			return 125, true
		}
		if err := dropCapabilities(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot drop temporary proxy ingress capabilities before startup: %v\n", err)
			return 125, true
		}
		if err := installNamespaceSeccomp(); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot prevent nested namespace creation before proxy application launch: %v\n", err)
			return 125, true
		}
	}
	if err := waitForStartupGate(gate); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: proxy TUN startup was aborted: %v\n", err)
		return 125, true
	}
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot remove the internal Go runtime setting before application launch: %v\n", err)
		return 125, true
	}
	if err := syscall.Exec(program, args[applicationIndex:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot execute application %q: %v\n", args[applicationIndex], err)
		return 126, true
	}
	return 0, true
}

func parseProxyTUNHelperArgs(args []string) (bool, string, []string, []string, int, bool) {
	if len(args) < 5 || args[0] != internalProxyTUNCommand {
		return false, "", nil, nil, 0, false
	}
	index := 1
	var nftPath string
	var tcpPorts, udpPorts []string
	ingressGuard := false
	if args[index] == "--ingress-guard" {
		if len(args) < 9 || !filepath.IsAbs(args[index+1]) {
			return false, "", nil, nil, 0, false
		}
		var err error
		tcpPorts, err = parsePortSet(args[index+2])
		if err != nil {
			return false, "", nil, nil, 0, false
		}
		udpPorts, err = parsePortSet(args[index+3])
		if err != nil || (len(tcpPorts) == 0 && len(udpPorts) == 0) {
			return false, "", nil, nil, 0, false
		}
		ingressGuard = true
		nftPath = args[index+1]
		index += 4
	}
	if len(args) <= index+3 || args[index] != "--gate-path" || args[index+1] != internalProxyTUNGatePath || args[index+2] != "--" || args[index+3] == "" {
		return false, "", nil, nil, 0, false
	}
	return ingressGuard, nftPath, tcpPorts, udpPorts, index + 3, true
}

func waitForProxyIngressReady(gate net.Conn) error {
	if err := gate.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	message := make([]byte, len("forwarder-ready\n"))
	if _, err := io.ReadFull(gate, message); err != nil {
		return err
	}
	if string(message) != "forwarder-ready\n" {
		return fmt.Errorf("host sent an invalid pasta readiness message")
	}
	return gate.SetDeadline(time.Time{})
}

func verifyProxyTUNCapabilities(expected uint64) error {
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("read process capability state: %w", err)
	}
	var effective, permitted, inheritable, ambient string
	for _, line := range strings.Split(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "CapEff":
			effective = strings.TrimSpace(value)
		case "CapPrm":
			permitted = strings.TrimSpace(value)
		case "CapInh":
			inheritable = strings.TrimSpace(value)
		case "CapAmb":
			ambient = strings.TrimSpace(value)
		}
	}
	if effective == "" || permitted == "" || inheritable == "" || ambient == "" {
		return fmt.Errorf("process capability state is incomplete")
	}
	eff, err := strconv.ParseUint(effective, 16, 64)
	if err != nil {
		return fmt.Errorf("parse effective capabilities: %w", err)
	}
	prm, err := strconv.ParseUint(permitted, 16, 64)
	if err != nil {
		return fmt.Errorf("parse permitted capabilities: %w", err)
	}
	inh, err := strconv.ParseUint(inheritable, 16, 64)
	if err != nil {
		return fmt.Errorf("parse inheritable capabilities: %w", err)
	}
	amb, err := strconv.ParseUint(ambient, 16, 64)
	if err != nil {
		return fmt.Errorf("parse ambient capabilities: %w", err)
	}
	if eff != expected || prm != expected || inh&^expected != 0 || amb&^expected != 0 || amb&^inh != 0 {
		return fmt.Errorf("unexpected proxy setup capabilities (expected mask %x): effective=%s permitted=%s inheritable=%s ambient=%s", expected, effective, permitted, inheritable, ambient)
	}
	return nil
}

func dropProxySysAdminCapability() error {
	if err := clearAmbientCapabilities(); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	header := capUserHeader{version: capVersion3}
	data := [capabilitySetsSize]capUserData{{
		effective: uint32(capNetAdminMask),
		permitted: uint32(capNetAdminMask),
	}}
	_, _, errno := syscall.RawSyscall(syscall.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&data[0])),
		0,
	)
	runtime.KeepAlive(&header)
	runtime.KeepAlive(&data)
	if errno != 0 {
		return errno
	}
	if err := verifyProxyTUNCapabilities(capNetAdminMask); err != nil {
		return fmt.Errorf("verify retained CAP_NET_ADMIN: %w", err)
	}
	return nil
}

func enableProxyLoopback() error {
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find loopback interface: %w", err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		return fmt.Errorf("bring loopback interface up: %w", err)
	}
	return nil
}

func configureProxyTUN() (*os.File, error) {
	device, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	if err := syscall.SetNonblock(int(device.Fd()), true); err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("set /dev/net/tun nonblocking: %w", err)
	}
	var request tunInterfaceRequest
	copy(request.name[:], proxyTUNName)
	request.flags = tunIFFTun | tunIFFNoPI
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, device.Fd(), tunSetIFF, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(&request)
	if errno != 0 {
		_ = device.Close()
		return nil, fmt.Errorf("TUNSETIFF: %w", errno)
	}
	var observed tunInterfaceRequest
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, device.Fd(), tunGetIFF, uintptr(unsafe.Pointer(&observed)))
	runtime.KeepAlive(&observed)
	if errno != 0 {
		_ = device.Close()
		return nil, fmt.Errorf("TUNGETIFF: %w", errno)
	}
	if string(observed.name[:len(proxyTUNName)]) != proxyTUNName || observed.flags&tunIFFTun == 0 || observed.flags&tunIFFTap != 0 || observed.flags&tunIFFNoPI == 0 {
		_ = device.Close()
		return nil, fmt.Errorf("kernel returned an unexpected TUN interface identity or flags")
	}

	link, err := netlink.LinkByName(proxyTUNName)
	if err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("find TUN interface %q: %w", proxyTUNName, err)
	}
	if err := netlink.LinkSetMTU(link, proxyTUNMTU); err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("set TUN MTU: %w", err)
	}
	address, err := netlink.ParseAddr(proxyTUNAddress)
	if err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("parse internal TUN IPv4 address: %w", err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("assign internal TUN IPv4 address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("bring up proxy TUN interface: %w", err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
	}); err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("install IPv4 default route through proxy TUN: %w", err)
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		_ = device.Close()
		return nil, fmt.Errorf("verify proxy TUN IPv4 routes: %w", err)
	}
	defaultRoutes := 0
	for _, route := range routes {
		if route.Dst == nil {
			defaultRoutes++
			if route.LinkIndex != link.Attrs().Index {
				_ = device.Close()
				return nil, fmt.Errorf("an unexpected IPv4 default route bypasses the proxy TUN")
			}
		}
	}
	if defaultRoutes != 1 {
		_ = device.Close()
		return nil, fmt.Errorf("expected one IPv4 default route through the proxy TUN, found %d", defaultRoutes)
	}
	return device, nil
}

func verifyNoIPv6EgressRoutes() error {
	routes, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return fmt.Errorf("read IPv6 route table: %w", err)
	}
	contents := strings.TrimSpace(string(routes))
	if contents == "" {
		return nil
	}

	var unexpected []string
	for _, entry := range strings.Split(contents, "\n") {
		fields := strings.Fields(entry)
		if !isKernelIPv6NullDiscardRoute(fields) {
			unexpected = append(unexpected, entry)
		}
	}
	if len(unexpected) == 0 {
		return nil
	}

	const maxDiagnosticEntries = 8
	shownEntries := unexpected
	if len(shownEntries) > maxDiagnosticEntries {
		shownEntries = shownEntries[:maxDiagnosticEntries]
	}
	details := strings.Join(shownEntries, "; ")
	if len(details) > 2048 {
		details = details[:2048] + "..."
	}
	if len(unexpected) > len(shownEntries) {
		details += fmt.Sprintf("; ... (%d more entries)", len(unexpected)-len(shownEntries))
	}
	return fmt.Errorf("IPv6 route table contains %d unexpected route(s): %s", len(unexpected), details)
}

func isKernelIPv6NullDiscardRoute(fields []string) bool {
	// Linux keeps the FIB's default null entry in /proc/net/ipv6_route even
	// after IPv6 is disabled. The kernel defines it with RTF_REJECT and
	// RTF_NONEXTHOP, an unreachable type, and a discard output handler.
	// Accept only that zero-prefix, no-next-hop, loopback entry. Reference and
	// use counters vary independently and do not affect the route semantics.
	if len(fields) != 10 {
		return false
	}
	const (
		zeroAddress  = "00000000000000000000000000000000"
		nullRouteFlg = "00200200" // RTF_REJECT | RTF_NONEXTHOP
	)
	if fields[0] != zeroAddress || fields[1] != "00" ||
		fields[2] != zeroAddress || fields[3] != "00" ||
		fields[4] != zeroAddress || fields[5] != "ffffffff" ||
		fields[8] != nullRouteFlg || fields[9] != "lo" {
		return false
	}
	for _, counter := range fields[6:8] {
		if _, err := strconv.ParseUint(counter, 16, 32); err != nil {
			return false
		}
	}
	return true
}

func sendProxyTUNDescriptor(gate *net.UnixConn, device *os.File) error {
	message := []byte("ready\n")
	n, oobn, err := gate.WriteMsgUnix(message, syscall.UnixRights(int(device.Fd())), nil)
	if err != nil {
		return err
	}
	if n != len(message) || oobn == 0 {
		return io.ErrShortWrite
	}
	return nil
}
