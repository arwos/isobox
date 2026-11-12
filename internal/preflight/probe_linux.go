//go:build linux

package preflight

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	probeCommand        = "__isobox_internal_probe"
	probeNonceEnv       = "ISOBOX_INTERNAL_PROBE_NONCE"
	probeUserNSIDEnv    = "ISOBOX_PROBE_PARENT_USERNS"
	probeNetworkNSIDEnv = "ISOBOX_PROBE_PARENT_NETNS"
	tunSetIFF           = uintptr(0x400454ca) // _IOW('T', 202, int)
	tunIFFTun           = uint16(0x0001)
	tunIFFNoPI          = uint16(0x1000)
)

type namespaceIdentity struct {
	device uint64
	inode  uint64
}

type tunInterfaceRequest struct {
	name  [16]byte
	flags uint16
	pad   [22]byte
}

func probeUserNamespace() error {
	return runNamespaceProbe("userns", false)
}

func probeTUN() error {
	return runNamespaceProbe("tun", true)
}

func runNamespaceProbe(kind string, network bool) error {
	userNS, err := currentNamespaceIdentity("/proc/self/ns/user")
	if err != nil {
		return fmt.Errorf("read current user namespace: %w", err)
	}
	netNS, err := currentNamespaceIdentity("/proc/self/ns/net")
	if err != nil {
		return fmt.Errorf("read current network namespace: %w", err)
	}
	program, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate isobox executable: %w", err)
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("create internal probe token: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, probeCommand, kind, nonce)
	command.Env = appendFilteredEnv(os.Environ(), probeNonceEnv, probeUserNSIDEnv, probeNetworkNSIDEnv)
	command.Env = append(command.Env,
		probeNonceEnv+"="+nonce,
		probeUserNSIDEnv+"="+formatNamespaceIdentity(userNS),
		probeNetworkNSIDEnv+"="+formatNamespaceIdentity(netNS),
	)
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 uintptr(syscall.CLONE_NEWUSER),
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	if network {
		command.SysProcAttr.Cloneflags |= uintptr(syscall.CLONE_NEWNET)
	}
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("probe exceeded %s", 5*time.Second)
	}
	message := strings.TrimSpace(string(output))
	if message != "" {
		return fmt.Errorf("%w (%s)", err, message)
	}
	return err
}

// RunProbeHelper handles only the private subprocess mode used by the parent
// preflight. A direct invocation without its random per-run token is rejected.
func RunProbeHelper(args []string) (int, bool) {
	if len(args) == 0 || args[0] != probeCommand {
		return 0, false
	}
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "invalid internal probe arguments")
		return 125, true
	}
	provided := []byte(args[2])
	expected := []byte(os.Getenv(probeNonceEnv))
	if len(expected) != len(provided) || subtle.ConstantTimeCompare(expected, provided) != 1 {
		fmt.Fprintln(os.Stderr, "internal probe token is invalid")
		return 125, true
	}
	parentUserNS, err := parseNamespaceIdentity(os.Getenv(probeUserNSIDEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid parent user namespace identity: %v\n", err)
		return 125, true
	}
	childUserNS, err := currentNamespaceIdentity("/proc/self/ns/user")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read child user namespace: %v\n", err)
		return 125, true
	}
	if childUserNS == parentUserNS {
		fmt.Fprintln(os.Stderr, "probe is not running in a new user namespace")
		return 125, true
	}
	switch args[1] {
	case "userns":
		return 0, true
	case "tun":
		parentNetNS, err := parseNamespaceIdentity(os.Getenv(probeNetworkNSIDEnv))
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid parent network namespace identity: %v\n", err)
			return 125, true
		}
		childNetNS, err := currentNamespaceIdentity("/proc/self/ns/net")
		if err != nil {
			fmt.Fprintf(os.Stderr, "read child network namespace: %v\n", err)
			return 125, true
		}
		if childNetNS == parentNetNS {
			fmt.Fprintln(os.Stderr, "probe is not running in a new network namespace")
			return 125, true
		}
		if err := createAndCloseTUN(); err != nil {
			fmt.Fprintf(os.Stderr, "TUNSETIFF failed: %v\n", err)
			return 1, true
		}
		return 0, true
	default:
		fmt.Fprintf(os.Stderr, "unknown internal probe %q\n", args[1])
		return 125, true
	}
}

func createAndCloseTUN() error {
	device, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/net/tun: %w", err)
	}
	var request tunInterfaceRequest
	copy(request.name[:], "isobox%d")
	request.flags = tunIFFTun | tunIFFNoPI
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, device.Fd(), tunSetIFF, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(&request)
	if errno != 0 {
		_ = device.Close()
		return errno
	}
	if err := device.Close(); err != nil {
		return fmt.Errorf("close TUN descriptor: %w", err)
	}
	return nil
}

func currentNamespaceIdentity(path string) (namespaceIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return namespaceIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return namespaceIdentity{}, fmt.Errorf("unexpected stat type %T", info.Sys())
	}
	return namespaceIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func formatNamespaceIdentity(identity namespaceIdentity) string {
	return strconv.FormatUint(identity.device, 10) + ":" + strconv.FormatUint(identity.inode, 10)
}

func parseNamespaceIdentity(value string) (namespaceIdentity, error) {
	deviceText, inodeText, ok := strings.Cut(value, ":")
	if !ok {
		return namespaceIdentity{}, fmt.Errorf("expected device:inode")
	}
	device, err := strconv.ParseUint(deviceText, 10, 64)
	if err != nil {
		return namespaceIdentity{}, err
	}
	inode, err := strconv.ParseUint(inodeText, 10, 64)
	if err != nil {
		return namespaceIdentity{}, err
	}
	return namespaceIdentity{device: device, inode: inode}, nil
}

func appendFilteredEnv(environment []string, keys ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		remove := false
		for _, key := range keys {
			if name == key {
				remove = true
				break
			}
		}
		if !remove {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
