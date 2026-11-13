//go:build linux

package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
	if len(args) < 5 || args[1] != "--gate-path" || args[2] != internalProxyTUNGatePath || args[3] != "--" || args[4] == "" {
		fmt.Fprintln(os.Stderr, "isobox: invalid internal proxy TUN helper arguments")
		return 125, true
	}
	runtime.LockOSThread()
	if err := verifyProxyTUNCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: proxy TUN helper lacks its temporary network capability: %v\n", err)
		return 125, true
	}
	if err := setNoNewPrivs(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot set no_new_privs before proxy TUN setup: %v\n", err)
		return 125, true
	}
	if err := disableIPv6InNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot disable IPv6 in the proxy network namespace: %v\n", err)
		return 125, true
	}
	if err := verifyNoIPv6Routes(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: IPv6 routes remain in the proxy network namespace: %v\n", err)
		return 125, true
	}
	tunFile, err := configureProxyTUN()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot configure the proxy TUN interface: %v\n", err)
		return 125, true
	}
	defer tunFile.Close()
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
	program, err := exec.LookPath(args[4])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: application executable %q cannot be found inside the sandbox: %v\n", args[4], err)
		return 127, true
	}
	gate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: args[2], Net: "unix"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot connect to the proxy TUN startup gate: %v\n", err)
		return 125, true
	}
	defer gate.Close()
	if err := sendProxyTUNDescriptor(gate, tunFile); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot send the TUN descriptor to the host supervisor: %v\n", err)
		return 125, true
	}
	if err := waitForStartupGate(gate); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: proxy TUN startup was aborted: %v\n", err)
		return 125, true
	}
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot remove the internal Go runtime setting before application launch: %v\n", err)
		return 125, true
	}
	if err := syscall.Exec(program, args[4:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot execute application %q: %v\n", args[4], err)
		return 126, true
	}
	return 0, true
}

func verifyProxyTUNCapabilities() error {
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
	const expected = capNetAdminMask
	if eff != expected || prm != expected || inh&^expected != 0 || amb&^expected != 0 || amb&^inh != 0 {
		return fmt.Errorf("expected only CAP_NET_ADMIN for proxy TUN setup, effective=%s permitted=%s inheritable=%s ambient=%s", effective, permitted, inheritable, ambient)
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

func verifyNoIPv6Routes() error {
	routes, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return fmt.Errorf("read IPv6 route table: %w", err)
	}
	if strings.TrimSpace(string(routes)) != "" {
		return fmt.Errorf("IPv6 route table is not empty")
	}
	return nil
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
