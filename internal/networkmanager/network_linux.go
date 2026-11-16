//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package networkmanager manages persistent rootless bridge networks.
package networkmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arwos/isobox/internal/integration"
	"github.com/vishvananda/netlink"
)

const (
	stateVersion       = 1
	managerStatusQuery = "status"
	maxStateFileSize   = 64 << 10
	maxControlMessage  = 4 << 10
)

var validNetworkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// Network is a persisted named bridge network and its current manager state.
type Network struct {
	Name          string
	Subnet        netip.Prefix
	Gateway       netip.Addr
	Bridge        string
	State         string
	ActiveMembers int
}

type networkConfig struct {
	Version       int    `json:"version"`
	Name          string `json:"name"`
	Subnet        string `json:"subnet"`
	Gateway       string `json:"gateway"`
	Bridge        string `json:"bridge"`
	RuntimeDir    string `json:"runtime_dir"`
	ControlSocket string `json:"control_socket"`
}

type managerStatus struct {
	Name          string `json:"name"`
	Subnet        string `json:"subnet"`
	Gateway       string `json:"gateway"`
	Bridge        string `json:"bridge"`
	ActiveMembers int    `json:"active_members"`
}

type controlRequest struct {
	Operation string `json:"operation"`
}

type controlResponse struct {
	Status managerStatus `json:"status,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// Create validates and persists a network, then installs, enables, and starts
// its user-level manager. A successful return means systemd received READY=1
// after the bridge and its control socket were created.
func Create(name, subnet string) (Network, error) {
	if err := requireOrdinaryUser(); err != nil {
		return Network{}, err
	}
	cfg, prefix, err := newConfig(name, subnet)
	if err != nil {
		return Network{}, err
	}
	if err := integration.CheckUserSystemdManager(); err != nil {
		return Network{}, err
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		return Network{}, fmt.Errorf("unshare is required for a rootless network manager: %w", err)
	}
	control, err := controlSocketPath(name, true)
	if err != nil {
		return Network{}, err
	}
	cfg.RuntimeDir = filepath.Dir(filepath.Dir(control))
	cfg.ControlSocket = control
	path, err := configPath(name, true)
	if err != nil {
		return Network{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		return Network{}, fmt.Errorf("network %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Network{}, fmt.Errorf("inspect network %q: %w", name, err)
	}
	if err := writeConfigExclusive(path, cfg); err != nil {
		return Network{}, fmt.Errorf("store network configuration: %w", err)
	}
	_, err = integration.InstallAndStartNetworkManager(integration.NetworkManagerUnitOptions{
		Name: name, ConfigPath: path,
	})
	if err != nil {
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return Network{}, fmt.Errorf("create network manager: %w; remove incomplete configuration: %v", err, removeErr)
		}
		return Network{}, err
	}
	return Network{Name: name, Subnet: prefix, Gateway: netip.MustParseAddr(cfg.Gateway), Bridge: cfg.Bridge, State: "active"}, nil
}

// List returns all configured networks and asks systemd and each live manager
// for their current state. An unreadable or inconsistent record fails closed.
func List() ([]Network, error) {
	if err := requireOrdinaryUser(); err != nil {
		return nil, err
	}
	directory, err := networkDirectory(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Network{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list network configurations: %w", err)
	}
	result := make([]Network, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" || entry.IsDir() {
			return nil, fmt.Errorf("unexpected entry %q in network configuration directory", entry.Name())
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		cfg, prefix, err := readConfig(name)
		if err != nil {
			return nil, err
		}
		state, err := integration.NetworkManagerUnitState(name)
		if err != nil {
			return nil, fmt.Errorf("inspect network %q: %w", name, err)
		}
		network := Network{Name: name, Subnet: prefix, Gateway: netip.MustParseAddr(cfg.Gateway), Bridge: cfg.Bridge, State: state}
		if state == "active" {
			status, err := queryManager(cfg.ControlSocket)
			if err != nil {
				return nil, fmt.Errorf("query active network %q: %w", name, err)
			}
			if status.Name != cfg.Name || status.Subnet != cfg.Subnet || status.Gateway != cfg.Gateway || status.Bridge != cfg.Bridge || status.ActiveMembers < 0 {
				return nil, fmt.Errorf("active network manager %q does not match the saved configuration", name)
			}
			network.ActiveMembers = status.ActiveMembers
		}
		result = append(result, network)
	}
	return result, nil
}

// Remove stops and removes one configured network. If its bridge still has
// attached member interfaces, the manager refuses removal.
func Remove(name string) error {
	if err := requireOrdinaryUser(); err != nil {
		return err
	}
	cfg, _, err := readConfig(name)
	if err != nil {
		return err
	}
	state, err := integration.NetworkManagerUnitState(name)
	if err != nil {
		return fmt.Errorf("inspect network %q: %w", name, err)
	}
	if state == "active" {
		status, err := queryManager(cfg.ControlSocket)
		if err != nil {
			return fmt.Errorf("cannot verify network membership; refusing to remove network %q: %w", name, err)
		}
		if status.Name != cfg.Name || status.Subnet != cfg.Subnet || status.Gateway != cfg.Gateway || status.Bridge != cfg.Bridge || status.ActiveMembers < 0 {
			return fmt.Errorf("network manager state does not match saved configuration; refusing to remove network %q", name)
		}
		if status.ActiveMembers != 0 {
			return fmt.Errorf("network %q has %d active member interface(s); stop its containers before removing it", name, status.ActiveMembers)
		}
	} else if _, err := os.Lstat(cfg.ControlSocket); err == nil {
		return fmt.Errorf("network %q manager is %s but its control socket remains; refusing removal because membership cannot be verified", name, state)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect network %q control socket: %w", name, err)
	}
	if _, err := integration.RemoveNetworkManagerUnit(name); err != nil {
		return err
	}
	path, err := configPath(name, false)
	if err != nil {
		return err
	}
	if err := removeOwnedConfig(path); err != nil {
		return fmt.Errorf("remove network configuration: %w", err)
	}
	return nil
}

// StartManager enters a private rootless user and network namespace, then
// replaces that namespace's network table with the named bridge manager.
func StartManager(path string) error {
	if err := requireOrdinaryUser(); err != nil {
		return err
	}
	cfg, _, err := readConfigByPath(path)
	if err != nil {
		return err
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return fmt.Errorf("unshare is required for a rootless network manager: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate isobox executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve isobox executable: %w", err)
	}
	configFile, err := configPath(cfg.Name, false)
	if err != nil {
		return err
	}
	command := exec.Command(unshare,
		"--user", "--map-root-user", "--net", "--fork", "--kill-child", "--",
		executable, "network", "__manager-worker", "--config="+configFile,
	)
	command.Stdin = nil
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run rootless network namespace manager: %w", err)
	}
	return nil
}

// RunManagerWorker configures the bridge and remains alive until the user
// systemd manager stops the service. It must only be called inside a child
// user namespace created by StartManager.
func RunManagerWorker(path string) error {
	if err := requireChildUserNamespace(); err != nil {
		return err
	}
	cfg, _, err := readConfigByPath(path)
	if err != nil {
		return err
	}
	if err := createBridge(cfg); err != nil {
		return err
	}
	listener, err := listenControl(cfg.ControlSocket)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- serveControl(ctx, listener, cfg) }()
	if err := notifySystemd("READY=1\nSTATUS=bridge " + cfg.Bridge + " is active"); err != nil {
		return fmt.Errorf("notify user systemd that network is ready: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		if err != nil {
			return err
		}
		return fmt.Errorf("network manager control socket stopped unexpectedly")
	}
}

func createBridge(cfg networkConfig) error {
	link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: cfg.Bridge}}
	if err := netlink.LinkAdd(link); err != nil {
		return fmt.Errorf("create rootless bridge %q: %w", cfg.Bridge, err)
	}
	created, err := netlink.LinkByName(cfg.Bridge)
	if err != nil {
		return fmt.Errorf("inspect created bridge %q: %w", cfg.Bridge, err)
	}
	cleanup := func() { _ = netlink.LinkDel(created) }
	address, err := netip.ParseAddr(cfg.Gateway)
	if err != nil {
		cleanup()
		return fmt.Errorf("parse configured bridge gateway: %w", err)
	}
	prefix, err := netip.ParsePrefix(cfg.Subnet)
	if err != nil {
		cleanup()
		return fmt.Errorf("parse configured bridge subnet: %w", err)
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: net.IP(address.AsSlice()), Mask: net.CIDRMask(prefix.Bits(), 32)}}
	if err := netlink.AddrAdd(created, addr); err != nil {
		cleanup()
		return fmt.Errorf("assign gateway %s to bridge: %w", cfg.Gateway, err)
	}
	if err := netlink.LinkSetUp(created); err != nil {
		cleanup()
		return fmt.Errorf("bring bridge %q up: %w", cfg.Bridge, err)
	}
	return nil
}

func serveControl(ctx context.Context, listener *net.UnixListener, cfg networkConfig) error {
	for {
		if err := listener.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return err
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				continue
			}
			return fmt.Errorf("accept network manager control request: %w", err)
		}
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		if err := handleControl(connection, cfg); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "isobox network manager: control request failed: %v\n", err)
		}
		_ = connection.Close()
	}
}

func handleControl(connection *net.UnixConn, cfg networkConfig) error {
	var request controlRequest
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&request); err != nil {
		return json.NewEncoder(connection).Encode(controlResponse{Error: "invalid request"})
	}
	var response controlResponse
	if request.Operation != managerStatusQuery {
		response.Error = "unsupported operation"
	} else {
		members, err := bridgePortCount(cfg.Bridge)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Status = managerStatus{
				Name: cfg.Name, Subnet: cfg.Subnet, Gateway: cfg.Gateway,
				Bridge: cfg.Bridge, ActiveMembers: members,
			}
		}
	}
	return json.NewEncoder(connection).Encode(response)
}

func bridgePortCount(name string) (int, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return 0, fmt.Errorf("list manager network links: %w", err)
	}
	var bridgeIndex int
	for _, link := range links {
		if link.Attrs().Name == name {
			bridgeIndex = link.Attrs().Index
			break
		}
	}
	if bridgeIndex == 0 {
		return 0, fmt.Errorf("managed bridge %q is missing", name)
	}
	count := 0
	for _, link := range links {
		if link.Attrs().MasterIndex == bridgeIndex {
			count++
		}
	}
	return count, nil
}

func queryManager(path string) (managerStatus, error) {
	connection, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return managerStatus{}, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(connection).Encode(controlRequest{Operation: managerStatusQuery}); err != nil {
		return managerStatus{}, err
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&response); err != nil {
		return managerStatus{}, err
	}
	if response.Error != "" {
		return managerStatus{}, fmt.Errorf("network manager: %s", response.Error)
	}
	return response.Status, nil
}

func listenControl(path string) (*net.UnixListener, error) {
	if err := ensureControlSocketParent(path); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(info) {
			return nil, fmt.Errorf("refusing to replace unsafe network control socket path %q", path)
		}
		connection, dialErr := net.DialTimeout("unix", path, 150*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, fmt.Errorf("network control socket %q is already active", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale network control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect network control socket: %w", err)
	}
	address := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("listen on network control socket: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("secure network control socket: %w", err)
	}
	return listener, nil
}

func notifySystemd(message string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return fmt.Errorf("NOTIFY_SOCKET is not set")
	}
	connection, err := net.Dial("unixgram", socket)
	if err != nil {
		return err
	}
	defer connection.Close()
	_, err = connection.Write([]byte(message))
	return err
}

func newConfig(name, subnet string) (networkConfig, netip.Prefix, error) {
	if !validNetworkName.MatchString(name) {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network name must start with an ASCII letter or digit and contain only letters, digits, '_' or '-' (maximum 63 characters)")
	}
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil || !prefix.Addr().Is4() {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("subnet must be an IPv4 CIDR")
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 8 || prefix.Bits() > 29 {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("subnet must be between /8 and /29 so the bridge gateway and at least two members have usable addresses")
	}
	gateway := prefix.Addr().Next()
	if !gateway.IsValid() || !prefix.Contains(gateway) {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("subnet has no usable gateway address")
	}
	bridge := bridgeName(name)
	control, err := controlSocketPath(name, true)
	if err != nil {
		return networkConfig{}, netip.Prefix{}, err
	}
	return networkConfig{
		Version: stateVersion, Name: name, Subnet: prefix.String(),
		Gateway: gateway.String(), Bridge: bridge,
		RuntimeDir: filepath.Dir(filepath.Dir(control)), ControlSocket: control,
	}, prefix, nil
}

func validateConfig(cfg networkConfig) (netip.Prefix, error) {
	if cfg.Version != stateVersion {
		return netip.Prefix{}, fmt.Errorf("unsupported network configuration version %d", cfg.Version)
	}
	if !validNetworkName.MatchString(cfg.Name) {
		return netip.Prefix{}, fmt.Errorf("invalid network name %q", cfg.Name)
	}
	prefix, err := netip.ParsePrefix(cfg.Subnet)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 8 || prefix.Bits() > 29 || prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("invalid canonical IPv4 subnet %q", cfg.Subnet)
	}
	if prefix.Addr().Next().String() != cfg.Gateway || bridgeName(cfg.Name) != cfg.Bridge {
		return netip.Prefix{}, fmt.Errorf("network configuration %q has inconsistent derived values", cfg.Name)
	}
	hash := sha256.Sum256([]byte(cfg.Name))
	expectedSocketName := "network-" + hex.EncodeToString(hash[:6]) + ".sock"
	expectedSocket := filepath.Join(cfg.RuntimeDir, "isobox", expectedSocketName)
	if !filepath.IsAbs(cfg.RuntimeDir) || filepath.Base(cfg.ControlSocket) != expectedSocketName || filepath.Base(filepath.Dir(cfg.ControlSocket)) != "isobox" || filepath.Clean(cfg.ControlSocket) != filepath.Clean(expectedSocket) || len(cfg.ControlSocket) >= 108 {
		return netip.Prefix{}, fmt.Errorf("network configuration %q has an invalid control socket path", cfg.Name)
	}
	return prefix, nil
}

func bridgeName(name string) string {
	hash := sha256.Sum256([]byte("isobox-network:" + name))
	return "ib" + hex.EncodeToString(hash[:6])
}

func configPath(name string, create bool) (string, error) {
	if !validNetworkName.MatchString(name) {
		return "", fmt.Errorf("invalid network name %q", name)
	}
	directory, err := networkDirectory(create)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, name+".json"), nil
}

func networkDirectory(create bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot determine the user's home directory")
	}
	root := filepath.Join(home, ".isobox")
	if err := ensurePrivateDirectory(root, create); err != nil {
		return "", fmt.Errorf("prepare isobox directory: %w", err)
	}
	directory := filepath.Join(root, "networks")
	if err := ensurePrivateDirectory(directory, create); err != nil {
		return "", fmt.Errorf("prepare network configuration directory: %w", err)
	}
	return directory, nil
}

func controlSocketPath(name string, create bool) (string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtimeDir) {
		return "", fmt.Errorf("XDG_RUNTIME_DIR must be set to an absolute user runtime directory")
	}
	info, err := os.Lstat(runtimeDir)
	if err != nil {
		return "", fmt.Errorf("inspect XDG_RUNTIME_DIR: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("XDG_RUNTIME_DIR must be a private real directory owned by the current user")
	}
	directory := filepath.Join(runtimeDir, "isobox")
	if create {
		if err := ensurePrivateDirectory(directory, true); err != nil {
			return "", fmt.Errorf("prepare network runtime directory: %w", err)
		}
	} else if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o700 {
			return "", fmt.Errorf("network runtime directory must be an owned private directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect network runtime directory: %w", err)
	}
	hash := sha256.Sum256([]byte(name))
	path := filepath.Join(directory, "network-"+hex.EncodeToString(hash[:6])+".sock")
	if len(path) >= 108 {
		return "", fmt.Errorf("XDG_RUNTIME_DIR path is too long for a Unix control socket")
	}
	return path, nil
}

func ensureControlSocketParent(path string) error {
	runtimeDir := filepath.Dir(filepath.Dir(path))
	runtimeInfo, err := os.Lstat(runtimeDir)
	if err != nil {
		return fmt.Errorf("inspect user runtime directory: %w", err)
	}
	if !runtimeInfo.IsDir() || runtimeInfo.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(runtimeInfo) || runtimeInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("user runtime directory must be a private real directory owned by the current user")
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect network runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("network runtime directory must be an owned private directory")
	}
	return nil
}

func ensurePrivateDirectory(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%q must be a real directory owned by the current user", path)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("set directory mode to 0700: %w", err)
		}
		info, err = os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("directory owner or mode changed while securing %q", path)
		}
	}
	return nil
}

func writeConfigExclusive(path string, cfg networkConfig) error {
	directory := filepath.Dir(path)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(directory, ".network-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		_ = os.Remove(path)
		return err
	}
	if err := dir.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func readConfig(name string) (networkConfig, netip.Prefix, error) {
	path, err := configPath(name, false)
	if err != nil {
		return networkConfig{}, netip.Prefix{}, err
	}
	return readConfigFile(path, name)
}

func readConfigByPath(path string) (networkConfig, netip.Prefix, error) {
	if !filepath.IsAbs(path) {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration path must be absolute")
	}
	path = filepath.Clean(path)
	name := strings.TrimSuffix(filepath.Base(path), ".json")
	expected, err := configPath(name, false)
	if err != nil {
		return networkConfig{}, netip.Prefix{}, err
	}
	if path != expected {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration must be stored inside ~/.isobox/networks")
	}
	return readConfigFile(path, name)
}

func readConfigFile(path, name string) (networkConfig, netip.Prefix, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return networkConfig{}, netip.Prefix{}, fmt.Errorf("network %q does not exist", name)
		}
		return networkConfig{}, netip.Prefix{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o600 {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration %q must be an owned regular file with mode 0600", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return networkConfig{}, netip.Prefix{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxStateFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return networkConfig{}, netip.Prefix{}, readErr
	}
	if closeErr != nil {
		return networkConfig{}, netip.Prefix{}, closeErr
	}
	if len(data) > maxStateFileSize {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration %q exceeds size limit", path)
	}
	var cfg networkConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("decode network configuration %q: %w", path, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration %q contains trailing data", path)
	}
	if cfg.Name != name {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("network configuration filename and name do not match")
	}
	prefix, err := validateConfig(cfg)
	if err != nil {
		return networkConfig{}, netip.Prefix{}, fmt.Errorf("invalid network configuration %q: %w", path, err)
	}
	return cfg, prefix, nil
}

func removeOwnedConfig(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("refusing to remove unsafe network configuration %q", path)
	}
	return os.Remove(path)
}

func requireOrdinaryUser() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("network group commands must run as an ordinary user, not root")
	}
	return nil
}

func requireChildUserNamespace() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("network manager worker must run as root inside its user namespace")
	}
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return fmt.Errorf("inspect network manager UID mapping: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return fmt.Errorf("network manager must run in the dedicated rootless UID mapping")
	}
	inner, innerErr := strconv.ParseUint(fields[0], 10, 32)
	outer, outerErr := strconv.ParseUint(fields[1], 10, 32)
	count, countErr := strconv.ParseUint(fields[2], 10, 32)
	if innerErr != nil || outerErr != nil || countErr != nil || inner != 0 || outer == 0 || count != 1 {
		return fmt.Errorf("network manager must map only guest UID 0 to the ordinary host user")
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
