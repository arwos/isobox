//go:build linux

package sandbox

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arwos/isobox/internal/profile"
)

const forwarderReadyTimeout = 8 * time.Second

type managedNetworkProcess interface {
	waitReady() error
	configureForwards([]profile.PortRule) error
	stop()
	doneChannel() <-chan struct{}
	error() error
}

type pastaProcess struct {
	command  *exec.Cmd
	done     chan struct{}
	waitErr  error
	expected map[string]struct{}
	pidFile  string
	tempDir  string
}

func startPasta(pastaPath string, namespacePID int, rules []profile.PortRule, mode profile.NetworkMode, dnsArgs []string, stdout, stderr io.Writer) (*pastaProcess, error) {
	if pastaPath == "" {
		return nil, fmt.Errorf("pasta executable is required for the selected network mode")
	}
	if namespacePID <= 0 {
		return nil, fmt.Errorf("bwrap did not report a valid namespace process ID")
	}
	tcpSpec, udpSpec, err := portForwardSpecs(rules)
	if err != nil {
		return nil, err
	}
	expected, hasTCP, hasUDP, err := expectedForwardedSockets(rules)
	if err != nil {
		return nil, err
	}
	offMode := mode == profile.NetworkModeOff
	proxyIngress := mode == profile.NetworkModeProxy
	if offMode && !hasTCP && !hasUDP {
		return nil, fmt.Errorf("at least one TCP or UDP host port must be announced")
	}
	if tcpSpec == "" {
		tcpSpec = "none"
	}
	if udpSpec == "" {
		udpSpec = "none"
	}

	tempDir, err := os.MkdirTemp("", "isobox-pasta-")
	if err != nil {
		return nil, fmt.Errorf("create pasta readiness directory: %w", err)
	}
	process := &pastaProcess{
		expected: expected,
		tempDir:  tempDir,
		pidFile:  filepath.Join(tempDir, "ready.pid"),
	}
	args := []string{
		"--foreground", "--quiet",
		"--ipv4-only",
		"--pid", process.pidFile,
		"--config-net",
		"--tcp-ns=none", "--udp-ns=none",
		"--tcp-ports=" + tcpSpec, "--udp-ports=" + udpSpec,
	}
	if offMode {
		args = append(args, "--no-splice", "--no-icmp", "--no-dhcp", "--no-dhcpv6", "--no-ndp", "--no-ra")
		if !hasTCP {
			args = append(args, "--no-tcp")
		}
		if !hasUDP {
			args = append(args, "--no-udp")
		}
	} else if proxyIngress {
		// Proxy mode uses pasta only as a published-port ingress transport. Its
		// fixed link and gateway are separated from the SOCKS TUN; a policy route
		// installed by the trusted helper sends only marked replies through it.
		args = append(args,
			"--ns-ifname=isoboxp0",
			"--address=10.89.1.2/24",
			"--gateway=10.89.1.1",
			"--no-splice", "--no-icmp", "--no-dhcp", "--no-dhcpv6", "--no-ndp", "--no-ra",
		)
		if !hasTCP {
			args = append(args, "--no-tcp")
		}
		if !hasUDP {
			args = append(args, "--no-udp")
		}
	} else {
		args = append(args, dnsArgs...)
	}
	args = append(args, strconv.Itoa(namespacePID))
	command := exec.Command(pastaPath, args...)
	if stdout != nil {
		command.Stdout = stdout
	}
	if stderr != nil {
		command.Stderr = stderr
	}
	if err := command.Start(); err != nil {
		if process.tempDir != "" {
			_ = os.RemoveAll(process.tempDir)
		}
		return nil, fmt.Errorf("start pasta for host port forwarding: %w", err)
	}
	process.command = command
	process.done = make(chan struct{})
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	return process, nil
}

func (process *pastaProcess) stop() {
	if process == nil {
		return
	}
	if process.command != nil && process.command.Process != nil {
		select {
		case <-process.done:
		default:
			_ = process.command.Process.Signal(os.Interrupt)
			timer := time.NewTimer(time.Second)
			select {
			case <-process.done:
			case <-timer.C:
				_ = process.command.Process.Kill()
				<-process.done
			}
			timer.Stop()
		}
	}
	if process.tempDir != "" {
		_ = os.RemoveAll(process.tempDir)
		process.tempDir = ""
	}
}

func (process *pastaProcess) waitReady() error {
	deadline := time.NewTimer(forwarderReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if process.pidFileReady() {
			ready, inspectErr := process.ownsForwardedSockets()
			if inspectErr != nil || ready {
				select {
				case <-process.done:
					return fmt.Errorf("pasta exited during startup: %w", process.waitErr)
				default:
					return nil
				}
			}
		}
		select {
		case <-process.done:
			if process.waitErr != nil {
				return fmt.Errorf("pasta exited during startup: %w", process.waitErr)
			}
			return fmt.Errorf("pasta exited before its network was ready")
		case <-ticker.C:
		case <-deadline.C:
			missing := process.missingForwardedSockets()
			return fmt.Errorf("pasta did not confirm all forwarded sockets within %s (missing: %s; PID file: %s)", forwarderReadyTimeout, missing, process.pidFile)
		}
	}
}

func (process *pastaProcess) configureForwards([]profile.PortRule) error { return nil }

func (process *pastaProcess) doneChannel() <-chan struct{} { return process.done }

func (process *pastaProcess) error() error { return process.waitErr }

func (process *pastaProcess) pidFileReady() bool {
	data, err := os.ReadFile(process.pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err == nil && process.command != nil && process.command.Process != nil && pid == process.command.Process.Pid
}

func (process *pastaProcess) ownsForwardedSockets() (bool, error) {
	owned, err := process.socketInodes()
	if err != nil {
		return false, err
	}
	found, err := process.netNamespaceSockets(owned)
	if err != nil {
		return false, err
	}
	for endpoint := range process.expected {
		if _, ok := found[endpoint]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (process *pastaProcess) missingForwardedSockets() string {
	owned, err := process.socketInodes()
	if err != nil {
		return "all configured endpoints (socket inspection unavailable)"
	}
	found, err := process.netNamespaceSockets(owned)
	if err != nil {
		return "all configured endpoints (network socket inspection unavailable)"
	}
	var missing []string
	for endpoint := range process.expected {
		if _, ok := found[endpoint]; !ok {
			missing = append(missing, endpoint)
		}
	}
	if len(missing) > 8 {
		missing = append(missing[:8], "...")
	}
	return strings.Join(missing, ", ")
}

func (process *pastaProcess) socketInodes() (map[string]struct{}, error) {
	fdDir := filepath.Join("/proc", strconv.Itoa(process.command.Process.Pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("read pasta file descriptors: %w", err)
	}
	inodes := make(map[string]struct{})
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = struct{}{}
	}
	return inodes, nil
}

func (process *pastaProcess) netNamespaceSockets(owned map[string]struct{}) (map[string]struct{}, error) {
	base := filepath.Join("/proc", strconv.Itoa(process.command.Process.Pid), "net")
	found := make(map[string]struct{})
	for _, table := range []struct {
		name     string
		protocol string
		state    string
	}{
		{name: "tcp", protocol: "tcp", state: "0A"},
		{name: "tcp6", protocol: "tcp", state: "0A"},
		{name: "udp", protocol: "udp"},
		{name: "udp6", protocol: "udp"},
	} {
		data, err := os.ReadFile(filepath.Join(base, table.name))
		if err != nil {
			if strings.HasSuffix(table.name, "6") && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read pasta network table %s: %w", table.name, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			if table.state != "" && fields[3] != table.state {
				continue
			}
			if _, ok := owned[fields[9]]; !ok {
				continue
			}
			address, port, err := parseProcEndpoint(fields[1])
			if err != nil {
				continue
			}
			found[table.protocol+"|"+address+"|"+strconv.Itoa(int(port))] = struct{}{}
		}
	}
	return found, nil
}

func parseProcEndpoint(value string) (string, uint16, error) {
	return parseProcEndpointWithByteOrder(value, binary.NativeEndian)
}

func parseProcEndpointWithByteOrder(value string, byteOrder binary.ByteOrder) (string, uint16, error) {
	addressText, portText, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, fmt.Errorf("invalid proc socket endpoint %q", value)
	}
	addressBytes, err := hex.DecodeString(addressText)
	if err != nil {
		return "", 0, err
	}
	littleEndian := byteOrder.Uint16([]byte{1, 0}) == 1
	switch len(addressBytes) {
	case net.IPv4len:
		if littleEndian {
			for left, right := 0, len(addressBytes)-1; left < right; left, right = left+1, right-1 {
				addressBytes[left], addressBytes[right] = addressBytes[right], addressBytes[left]
			}
		}
	case net.IPv6len:
		if littleEndian {
			for group := 0; group < len(addressBytes); group += 4 {
				addressBytes[group], addressBytes[group+3] = addressBytes[group+3], addressBytes[group]
				addressBytes[group+1], addressBytes[group+2] = addressBytes[group+2], addressBytes[group+1]
			}
		}
	default:
		return "", 0, fmt.Errorf("invalid proc address length %d", len(addressBytes))
	}
	portNumber, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return "", 0, err
	}
	return net.IP(addressBytes).String(), uint16(portNumber), nil
}

func expectedForwardedSockets(rules []profile.PortRule) (map[string]struct{}, bool, bool, error) {
	expected := make(map[string]struct{})
	var hasTCP, hasUDP bool
	for index, rule := range rules {
		ip := net.ParseIP(rule.HostAddress)
		if ip == nil || ip.To4() == nil || strings.Contains(rule.HostAddress, ":") {
			return nil, false, false, fmt.Errorf("profile.network.port[%d].host_address must be IPv4; IPv6 is disabled", index)
		}
		ip = ip.To4()
		start, end := rule.Port, rule.Port
		if rule.EndPort != 0 {
			end = rule.EndPort
		}
		for port := uint32(start); port <= uint32(end); port++ {
			switch rule.Protocol {
			case profile.PortProtocolBoth:
				hasTCP = true
				expected["tcp|"+ip.String()+"|"+strconv.FormatUint(uint64(port), 10)] = struct{}{}
				hasUDP = true
				expected["udp|"+ip.String()+"|"+strconv.FormatUint(uint64(port), 10)] = struct{}{}
			case profile.PortProtocolTCP:
				hasTCP = true
				expected["tcp|"+ip.String()+"|"+strconv.FormatUint(uint64(port), 10)] = struct{}{}
			case profile.PortProtocolUDP:
				hasUDP = true
				expected["udp|"+ip.String()+"|"+strconv.FormatUint(uint64(port), 10)] = struct{}{}
			default:
				return nil, false, false, fmt.Errorf("profile.network.port[%d].protocol %q is invalid", index, rule.Protocol)
			}
		}
	}
	return expected, hasTCP, hasUDP, nil
}
