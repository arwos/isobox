//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package networkmanager manages persistent rootless bridge networks.
package networkmanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/arwos/isobox/internal/integration"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/proxy"
)

const (
	stateVersion       = 1
	managerStatusQuery = "status"
	maxStateFileSize   = 64 << 10
	maxControlMessage  = 64 << 10
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
	Operation        string         `json:"operation"`
	Name             string         `json:"name,omitempty"`
	AppID            string         `json:"app_id,omitempty"`
	Aliases          []string       `json:"aliases,omitempty"`
	Groups           []string       `json:"groups,omitempty"`
	Arguments        []string       `json:"arguments,omitempty"`
	Environment      []string       `json:"environment,omitempty"`
	WorkingDirectory string         `json:"working_directory,omitempty"`
	HostUID          int            `json:"host_uid,omitempty"`
	HostGID          int            `json:"host_gid,omitempty"`
	DNSQuery         []byte         `json:"dns_query,omitempty"`
	MemberTUNInfo    *MemberTUNInfo `json:"member_tun_info,omitempty"`
}

type controlResponse struct {
	Status        managerStatus  `json:"status,omitempty"`
	Error         string         `json:"error,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	DNSResponse   []byte         `json:"dns_response,omitempty"`
	DNSFound      bool           `json:"dns_found,omitempty"`
	Operation     string         `json:"operation,omitempty"`
	MemberTUNInfo *MemberTUNInfo `json:"member_tun_info,omitempty"`
}

type sharedManager struct {
	mu        sync.Mutex
	networks  map[string]networkConfig
	listeners map[string]*net.UnixListener
	ctx       context.Context
	serveErr  chan error
	members   map[int]*memberState
}

type memberState struct {
	holder      *exec.Cmd
	netNSInode  uint64
	names       []string
	allocations []memberAllocation
	parent      *net.UnixConn
	tunnelSent  bool
}

type memberAllocation struct {
	Group         string `json:"group"`
	Interface     string `json:"interface"`
	Address       string `json:"address"`
	Prefix        int    `json:"prefix"`
	HostInterface string `json:"host_interface"`
	NamespacePID  int    `json:"namespace_pid"`
}

// MemberAttachment describes one interface assigned by the manager to a
// sandbox launch.
type MemberAttachment struct {
	Group        string `json:"group"`
	Interface    string `json:"interface"`
	Address      string `json:"address"`
	Prefix       int    `json:"prefix"`
	NamespacePID int    `json:"namespace_pid"`
}

type MemberTUNInfo struct {
	InterfaceName       string `json:"interface_name"`
	Subnet              string `json:"subnet"`
	GuestAddress        string `json:"guest_address"`
	ResolverEndpoint    string `json:"resolver_endpoint"`
	HostGatewayEndpoint string `json:"host_gateway_endpoint"`
	BackendNamespacePID int    `json:"backend_namespace_pid,omitempty"`
}

const (
	memberEnvKey           = "ISOBOX_INTERNAL_GROUP_MEMBER"
	memberEnvAssignments   = "ISOBOX_INTERNAL_GROUP_ASSIGNMENTS"
	memberEnvHostUID       = "ISOBOX_INTERNAL_GROUP_HOST_UID"
	memberEnvHostGID       = "ISOBOX_INTERNAL_GROUP_HOST_GID"
	memberEnvControlSocket = "ISOBOX_INTERNAL_GROUP_CONTROL_SOCKET"
)

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
	_, err = integration.InstallAndStartNetworkManager(integration.NetworkManagerUnitOptions{Name: name})
	if err != nil {
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return Network{}, fmt.Errorf("create network manager: %w; remove incomplete configuration: %w", err, removeErr)
		}
		return Network{}, err
	}
	managerSocket := cfg.ControlSocket
	configs, err := readAllNetworkConfigs()
	if err != nil {
		return Network{}, fmt.Errorf("inspect configured networks: %w", err)
	}
	for _, existing := range configs {
		if existing.Name != cfg.Name {
			managerSocket = existing.ControlSocket
			break
		}
	}
	if err := sendManagerRequest(managerSocket, controlRequest{Operation: "create", Name: cfg.Name}); err != nil {
		return Network{}, fmt.Errorf("activate network %q in the shared manager: %w", cfg.Name, err)
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
			status, err := queryManager(cfg.ControlSocket, cfg.Name)
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
		status, err := queryManager(cfg.ControlSocket, cfg.Name)
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
	if state == "active" {
		if err := sendManagerRequest(cfg.ControlSocket, controlRequest{Operation: "remove", Name: name}); err != nil {
			return fmt.Errorf("remove network %q from the shared manager: %w", name, err)
		}
	}
	path, err := configPath(name, false)
	if err != nil {
		return err
	}
	if err := removeOwnedConfig(path); err != nil {
		return fmt.Errorf("remove network configuration: %w", err)
	}
	configs, err := readAllNetworkConfigs()
	if err != nil {
		return fmt.Errorf("inspect remaining network configurations: %w", err)
	}
	if len(configs) == 0 {
		if _, err := integration.RemoveNetworkManagerUnit(name); err != nil {
			return fmt.Errorf("remove final network manager service: %w", err)
		}
	}
	return nil
}

// StartManager enters a private rootless user and network namespace, then
// replaces that namespace's network table with the named bridge manager.
func StartManager(path string) error {
	if err := requireOrdinaryUser(); err != nil {
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
	command := exec.Command(unshare,
		"--user", "--map-root-user", "--net", "--fork", "--kill-child", "--",
		executable, "network", "__manager-worker",
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
	configs, err := readAllNetworkConfigs()
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		return fmt.Errorf("no named networks are configured")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manager := &sharedManager{
		networks:  make(map[string]networkConfig, len(configs)),
		listeners: make(map[string]*net.UnixListener, len(configs)),
		ctx:       ctx, serveErr: make(chan error, 1), members: make(map[int]*memberState),
	}
	cleanup := func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		for _, listener := range manager.listeners {
			_ = listener.Close()
		}
		for _, cfg := range manager.networks {
			if link, linkErr := netlink.LinkByName(cfg.Bridge); linkErr == nil {
				_ = netlink.LinkDel(link)
			}
		}
	}
	for _, cfg := range configs {
		if err := manager.addNetwork(cfg); err != nil {
			cleanup()
			return err
		}
	}
	defer cleanup()

	if err := notifySystemd(fmt.Sprintf("READY=1\nSTATUS=%d rootless network bridge(s) are active", len(manager.networks))); err != nil {
		return fmt.Errorf("notify user systemd that network is ready: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-manager.serveErr:
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("network manager control socket stopped unexpectedly")
	}
}

// MemberTunnel supervises a network data plane owned by the outer launcher.
type MemberTunnel interface {
	Done() <-chan struct{}
	Err() error
	Close() error
}

type MemberTunnelStarter func(*os.File, MemberTUNInfo) (MemberTunnel, error)

// RunJoined asks the active rootless manager to allocate interfaces and launch
// the supplied isobox run arguments in their member network namespace.
func RunJoined(groups []string, aliases []string, appID string, arguments, environment []string, stdin, stdout, stderr *os.File, startTunnel MemberTunnelStarter) (int, error) {
	if len(groups) == 0 || len(groups) > 16 {
		return 0, fmt.Errorf("grouped sandbox must select between 1 and 16 networks")
	}
	for _, stream := range []*os.File{stdin, stdout, stderr} {
		if stream == nil {
			return 0, fmt.Errorf("grouped sandbox requires file-backed stdin, stdout, and stderr")
		}
	}
	cfg, _, err := readConfig(groups[0])
	if err != nil {
		return 0, err
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: cfg.ControlSocket, Net: "unix"})
	if err != nil {
		return 0, fmt.Errorf("connect to rootless network manager: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(24 * time.Hour))
	request := controlRequest{
		Operation: "run", Groups: append([]string(nil), groups...), Aliases: append([]string(nil), aliases...),
		AppID: appID, Arguments: append([]string(nil), arguments...), Environment: append([]string(nil), environment...),
		HostUID: os.Getuid(), HostGID: os.Getgid(),
	}
	request.WorkingDirectory, err = os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("determine grouped launch working directory: %w", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	if len(data) > maxControlMessage {
		return 0, fmt.Errorf("grouped launch request exceeds the manager message size limit")
	}
	oob := syscall.UnixRights(int(stdin.Fd()), int(stdout.Fd()), int(stderr.Fd()))
	n, _, err := conn.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return 0, fmt.Errorf("send grouped launch request: %w", err)
	}
	if n != len(data) {
		return 0, fmt.Errorf("send grouped launch request: incomplete control message")
	}
	var tunnel MemberTunnel
	defer func() {
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}()
	for {
		response, receivedFile, err := readGroupedResponse(conn, tunnel)
		if err != nil {
			return 125, fmt.Errorf("read grouped launch result: %w", err)
		}
		if response.Operation == "member-tun" {
			if receivedFile == nil || response.MemberTUNInfo == nil || startTunnel == nil {
				if receivedFile != nil {
					_ = receivedFile.Close()
				}
				err = fmt.Errorf("unexpected policy TUN descriptor from grouped launch")
			} else {
				tunnel, err = startTunnel(receivedFile, *response.MemberTUNInfo)
			}
			ack := controlResponse{Operation: "member-tun-ack"}
			if err != nil {
				ack.Error = err.Error()
			}
			if encodeErr := json.NewEncoder(conn).Encode(ack); encodeErr != nil {
				return 125, fmt.Errorf("acknowledge grouped policy TUN: %w", encodeErr)
			}
			if err != nil {
				return 125, fmt.Errorf("start grouped policy TUN data plane: %w", err)
			}
			continue
		}
		if response.Operation == "member-backend" {
			if receivedFile != nil || response.MemberTUNInfo == nil || startTunnel == nil {
				if receivedFile != nil {
					_ = receivedFile.Close()
				}
				err = fmt.Errorf("unexpected grouped network backend handoff")
			} else {
				tunnel, err = startTunnel(nil, *response.MemberTUNInfo)
			}
			ack := controlResponse{Operation: "member-backend-ack"}
			if err != nil {
				ack.Error = err.Error()
			}
			if encodeErr := json.NewEncoder(conn).Encode(ack); encodeErr != nil {
				return 125, fmt.Errorf("acknowledge grouped network backend: %w", encodeErr)
			}
			if err != nil {
				return 125, fmt.Errorf("start grouped network backend: %w", err)
			}
			continue
		}
		if receivedFile != nil {
			_ = receivedFile.Close()
			return 125, fmt.Errorf("network manager attached an unexpected descriptor")
		}
		if response.Error != "" {
			return 125, fmt.Errorf("network manager: %s", response.Error)
		}
		if response.ExitCode == nil || *response.ExitCode < 0 || *response.ExitCode > 255 {
			return 125, fmt.Errorf("network manager returned an invalid application exit status")
		}
		if tunnel != nil {
			select {
			case <-tunnel.Done():
				if tunnel.Err() != nil {
					return 125, fmt.Errorf("grouped policy TUN stopped: %w", tunnel.Err())
				}
			default:
			}
		}
		return *response.ExitCode, nil
	}
}

type groupedResponseResult struct {
	response controlResponse
	file     *os.File
	err      error
}

func readGroupedResponse(connection *net.UnixConn, tunnel MemberTunnel) (controlResponse, *os.File, error) {
	if tunnel == nil {
		return readControlResponseWithFD(connection)
	}
	result := make(chan groupedResponseResult, 1)
	go func() {
		response, file, err := readControlResponseWithFD(connection)
		result <- groupedResponseResult{response: response, file: file, err: err}
	}()
	select {
	case received := <-result:
		return received.response, received.file, received.err
	case <-tunnel.Done():
		if err := tunnel.Err(); err != nil {
			_ = connection.Close()
			return controlResponse{}, nil, fmt.Errorf("grouped policy TUN stopped: %w", err)
		}
		_ = connection.Close()
		return controlResponse{}, nil, fmt.Errorf("grouped policy TUN stopped unexpectedly")
	}
}

// SubmitMemberTUN transfers a configured TUN descriptor to the outer grouped
// launcher, which starts its data plane in the host network namespace.
func SubmitMemberTUN(file *os.File, info MemberTUNInfo) error {
	controlSocket := os.Getenv(memberEnvControlSocket)
	if file == nil || !filepath.IsAbs(controlSocket) {
		return fmt.Errorf("grouped policy TUN handoff is unavailable")
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: controlSocket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect to network manager for policy TUN handoff: %w", err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	data, err := json.Marshal(controlRequest{Operation: "member-tun", MemberTUNInfo: &info})
	if err != nil {
		return err
	}
	oob := syscall.UnixRights(int(file.Fd()))
	n, _, err := connection.WriteMsgUnix(append(data, '\n'), oob, nil)
	if err != nil {
		return fmt.Errorf("send policy TUN descriptor to network manager: %w", err)
	}
	if n != len(data)+1 {
		return fmt.Errorf("send policy TUN descriptor to network manager: incomplete control message")
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&response); err != nil {
		return fmt.Errorf("read policy TUN handoff result: %w", err)
	}
	if response.Error != "" {
		return fmt.Errorf("network manager policy TUN handoff: %s", response.Error)
	}
	return nil
}

// SubmitMemberBackend asks the outer launcher to start a direct network
// backend from the host network namespace before the sandbox starts.
func SubmitMemberBackend(info MemberTUNInfo) error {
	controlSocket := os.Getenv(memberEnvControlSocket)
	if !filepath.IsAbs(controlSocket) || info.BackendNamespacePID <= 0 {
		return fmt.Errorf("grouped network backend handoff is unavailable")
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: controlSocket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect to network manager for backend handoff: %w", err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	data, err := json.Marshal(controlRequest{Operation: "member-backend", MemberTUNInfo: &info})
	if err != nil {
		return err
	}
	n, _, err := connection.WriteMsgUnix(append(data, '\n'), nil, nil)
	if err != nil {
		return fmt.Errorf("send grouped network backend handoff: %w", err)
	}
	if n != len(data)+1 {
		return fmt.Errorf("send grouped network backend handoff: incomplete control message")
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&response); err != nil {
		return fmt.Errorf("read grouped network backend handoff result: %w", err)
	}
	if response.Error != "" {
		return fmt.Errorf("network manager backend handoff: %s", response.Error)
	}
	return nil
}

// MemberRuntime verifies that the current process was launched by the network
// manager in its rootless user namespace and returns the assigned interfaces.
func MemberRuntime() (bool, []MemberAttachment, int, int, error) {
	if os.Getenv(memberEnvKey) != "1" {
		return false, nil, 0, 0, nil
	}
	if err := requireChildUserNamespace(); err != nil {
		return true, nil, 0, 0, fmt.Errorf("invalid grouped launch context: %w", err)
	}
	controlSocket := os.Getenv(memberEnvControlSocket)
	if !filepath.IsAbs(controlSocket) {
		return true, nil, 0, 0, fmt.Errorf("invalid grouped launch manager endpoint")
	}
	if err := verifyManagerMember(controlSocket); err != nil {
		return true, nil, 0, 0, fmt.Errorf("manager did not verify this network namespace: %w", err)
	}
	uid, err := strconv.Atoi(os.Getenv(memberEnvHostUID))
	if err != nil || uid < 0 {
		return true, nil, 0, 0, fmt.Errorf("invalid grouped launch host UID")
	}
	gid, err := strconv.Atoi(os.Getenv(memberEnvHostGID))
	if err != nil || gid < 0 || !mappedHostIdentity(uid, gid) {
		return true, nil, 0, 0, fmt.Errorf("grouped launch host identity does not match the manager's rootless mapping")
	}
	var attachments []MemberAttachment
	if err := json.Unmarshal([]byte(os.Getenv(memberEnvAssignments)), &attachments); err != nil || len(attachments) == 0 || len(attachments) > 16 {
		return true, nil, 0, 0, fmt.Errorf("invalid grouped launch interface assignments")
	}
	seen := make(map[string]struct{}, len(attachments))
	namespacePID := attachments[0].NamespacePID
	if namespacePID <= 0 {
		return true, nil, 0, 0, fmt.Errorf("invalid grouped launch network namespace holder PID")
	}
	currentInode, err := networkNamespaceInode(os.Getpid())
	if err != nil {
		return true, nil, 0, 0, fmt.Errorf("inspect grouped launch network namespace: %w", err)
	}
	holderInode, err := networkNamespaceInode(namespacePID)
	if err != nil || holderInode != currentInode {
		return true, nil, 0, 0, fmt.Errorf("grouped launch namespace holder does not reference the member network namespace")
	}
	for _, attachment := range attachments {
		if !validNetworkName.MatchString(attachment.Group) || !validLinuxInterfaceName(attachment.Interface) || attachment.Prefix < 1 || attachment.Prefix > 30 || attachment.NamespacePID != namespacePID {
			return true, nil, 0, 0, fmt.Errorf("invalid grouped launch interface assignment")
		}
		if _, exists := seen[attachment.Group]; exists {
			return true, nil, 0, 0, fmt.Errorf("duplicate grouped launch interface assignment")
		}
		seen[attachment.Group] = struct{}{}
		address, parseErr := netip.ParseAddr(attachment.Address)
		if parseErr != nil || !address.Is4() {
			return true, nil, 0, 0, fmt.Errorf("invalid IPv4 address in grouped launch assignment")
		}
	}
	return true, attachments, uid, gid, nil
}

func verifyManagerMember(path string) error {
	connection, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(connection).Encode(controlRequest{Operation: "verify-member"}); err != nil {
		return err
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&response); err != nil {
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("%s", response.Error)
	}
	return nil
}

// MemberDNSConfig configures a per-sandbox loopback resolver.
type MemberDNSConfig struct {
	Mode             profile.NetworkMode
	Upstreams        []string
	ResolverEndpoint string
}

// StartMemberDNS runs a per-sandbox resolver backed by the manager's live group
// membership table. The caller must stop it when the app exits.
func StartMemberDNS(config MemberDNSConfig) (func(), error) {
	controlSocket := os.Getenv(memberEnvControlSocket)
	if !filepath.IsAbs(controlSocket) {
		return nil, fmt.Errorf("invalid grouped launch manager endpoint")
	}
	address, err := net.ResolveUDPAddr("udp4", "127.0.0.1:53")
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenUDP("udp4", address)
	if err != nil {
		return nil, fmt.Errorf("start group DNS resolver on loopback: %w", err)
	}
	tcpListener, err := net.Listen("tcp4", "127.0.0.1:53")
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("start group DNS TCP resolver on loopback: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var requests sync.WaitGroup
	var loops sync.WaitGroup
	semaphore := make(chan struct{}, 16)
	loops.Add(2)
	go func() {
		defer loops.Done()
		buffer := make([]byte, 4096)
		for {
			_ = listener.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			count, client, err := listener.ReadFromUDP(buffer)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				var networkError net.Error
				if errors.As(err, &networkError) && networkError.Timeout() {
					continue
				}
				return
			}
			query := append([]byte(nil), buffer[:count]...)
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			requests.Add(1)
			go func(client *net.UDPAddr, query []byte) {
				defer requests.Done()
				defer func() { <-semaphore }()
				response, resolveErr := resolveMemberDNSRequest(controlSocket, config, query)
				if resolveErr != nil {
					response, resolveErr = proxy.ServerFailureDNSResponse(query)
					if resolveErr != nil {
						return
					}
				}
				_, _ = listener.WriteToUDP(response, client)
			}(client, query)
		}
	}()
	go func() {
		defer loops.Done()
		for {
			connection, err := tcpListener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				return
			}
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				_ = connection.Close()
				return
			}
			requests.Add(1)
			go func(connection net.Conn) {
				defer requests.Done()
				defer func() { <-semaphore }()
				defer func() { _ = connection.Close() }()
				serveMemberDNSTCP(controlSocket, config, connection)
			}(connection)
		}
	}()
	return func() {
		cancel()
		_ = listener.Close()
		_ = tcpListener.Close()
		loops.Wait()
		requests.Wait()
	}, nil
}

func resolveMemberDNSRequest(controlSocket string, config MemberDNSConfig, query []byte) ([]byte, error) {
	response, err := requestManager(controlSocket, controlRequest{Operation: "resolve-member-dns", DNSQuery: query})
	if err != nil {
		return nil, err
	}
	if len(response.DNSResponse) == 0 {
		return nil, fmt.Errorf("network manager returned an empty DNS response")
	}
	if response.DNSFound {
		return response.DNSResponse, nil
	}
	singleLabel, err := proxy.IsSingleLabelDNSQuery(query)
	if err != nil {
		return nil, err
	}
	if config.Mode == profile.NetworkModeOff || singleLabel {
		return response.DNSResponse, nil
	}
	return forwardMemberDNSQuery(config, query)
}

func forwardMemberDNSQuery(config MemberDNSConfig, query []byte) ([]byte, error) {
	upstreams := append([]string(nil), config.Upstreams...)
	if config.ResolverEndpoint != "" {
		upstreams = []string{config.ResolverEndpoint}
	}
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no external DNS resolver is configured")
	}
	var lastErr error
	for _, upstream := range upstreams {
		connection, err := net.DialTimeout("udp4", net.JoinHostPort(upstream, "53"), 2*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := connection.Write(query); err != nil {
			_ = connection.Close()
			lastErr = err
			continue
		}
		buffer := make([]byte, 4096)
		count, err := connection.Read(buffer)
		_ = connection.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if count < 12 {
			lastErr = fmt.Errorf("DNS upstream returned a response shorter than the header")
			continue
		}
		return append([]byte(nil), buffer[:count]...), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all external DNS resolvers failed")
	}
	return nil, lastErr
}

func serveMemberDNSTCP(controlSocket string, config MemberDNSConfig, connection net.Conn) {
	for {
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		var size [2]byte
		if _, err := io.ReadFull(connection, size[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(size[:]))
		if length < 12 || length > 4096 {
			return
		}
		query := make([]byte, length)
		if _, err := io.ReadFull(connection, query); err != nil {
			return
		}
		response, err := resolveMemberDNSRequest(controlSocket, config, query)
		if err != nil {
			response, err = proxy.ServerFailureDNSResponse(query)
		}
		if err != nil || len(response) > 65535 {
			return
		}
		binary.BigEndian.PutUint16(size[:], uint16(len(response)))
		if _, err := connection.Write(size[:]); err != nil {
			return
		}
		if _, err := connection.Write(response); err != nil {
			return
		}
	}
}

func validLinuxInterfaceName(name string) bool {
	if len(name) == 0 || len(name) > 15 || name == "lo" || name == "." || name == ".." {
		return false
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
		default:
			return false
		}
	}
	return true
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

func (manager *sharedManager) addNetwork(cfg networkConfig) error {
	if _, err := validateConfig(cfg); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if existing, exists := manager.networks[cfg.Name]; exists {
		if existing == cfg {
			return nil
		}
		return fmt.Errorf("network %q has conflicting configuration in the shared manager", cfg.Name)
	}
	if err := createBridge(cfg); err != nil {
		return err
	}
	listener, err := listenControl(cfg.ControlSocket)
	if err != nil {
		if link, linkErr := netlink.LinkByName(cfg.Bridge); linkErr == nil {
			_ = netlink.LinkDel(link)
		}
		return err
	}
	manager.networks[cfg.Name] = cfg
	manager.listeners[cfg.Name] = listener
	if manager.ctx != nil {
		go func() {
			if err := serveControl(manager.ctx, listener, manager); err != nil && manager.ctx.Err() == nil {
				select {
				case manager.serveErr <- err:
				case <-manager.ctx.Done():
				}
			}
		}()
	}
	return nil
}

func (manager *sharedManager) networkStatus(name string) (managerStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	cfg, exists := manager.networks[name]
	if !exists {
		return managerStatus{}, fmt.Errorf("network %q is not active in the shared manager", name)
	}
	members, err := bridgePortCount(cfg.Bridge)
	if err != nil {
		return managerStatus{}, err
	}
	return managerStatus{Name: cfg.Name, Subnet: cfg.Subnet, Gateway: cfg.Gateway, Bridge: cfg.Bridge, ActiveMembers: members}, nil
}

func (manager *sharedManager) removeNetwork(name string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	cfg, exists := manager.networks[name]
	if !exists {
		return fmt.Errorf("network %q is not active in the shared manager", name)
	}
	members, err := bridgePortCount(cfg.Bridge)
	if err != nil {
		return err
	}
	if members != 0 {
		return fmt.Errorf("network %q has %d active member interface(s)", name, members)
	}
	link, err := netlink.LinkByName(cfg.Bridge)
	if err != nil {
		return fmt.Errorf("inspect bridge %q: %w", cfg.Bridge, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("remove bridge %q: %w", cfg.Bridge, err)
	}
	if listener := manager.listeners[name]; listener != nil {
		_ = listener.Close()
		delete(manager.listeners, name)
	}
	delete(manager.networks, name)
	return nil
}

func serveControl(ctx context.Context, listener *net.UnixListener, manager *sharedManager) error {
	for {
		if err := listener.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				continue
			}
			return fmt.Errorf("accept network manager control request: %w", err)
		}
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		go func(connection *net.UnixConn) {
			defer func() { _ = connection.Close() }()
			request, files, err := readControlRequest(connection)
			if err == nil {
				_ = connection.SetDeadline(time.Now().Add(24 * time.Hour))
				err = handleControl(connection, manager, request, files)
			}
			for _, file := range files {
				_ = file.Close()
			}
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "isobox network manager: control request failed: %v\n", err)
			}
		}(connection)
	}
}

func handleControl(connection *net.UnixConn, manager *sharedManager, request controlRequest, files []*os.File) error {
	var response controlResponse
	switch request.Operation {
	case managerStatusQuery:
		status, err := manager.networkStatus(request.Name)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Status = status
		}
	case "create":
		cfg, _, err := readConfig(request.Name)
		if err == nil {
			err = manager.addNetwork(cfg)
		}
		if err != nil {
			response.Error = err.Error()
		}
	case "remove":
		if err := manager.removeNetwork(request.Name); err != nil {
			response.Error = err.Error()
		}
	case "run":
		code, err := manager.runMember(connection, request, files)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.ExitCode = &code
		}
	case "verify-member":
		if err := manager.verifyMember(connection); err != nil {
			response.Error = err.Error()
		}
	case "resolve-member-dns":
		var err error
		response.DNSResponse, response.DNSFound, err = manager.resolveMemberDNS(connection, request.DNSQuery)
		if err != nil {
			response.Error = err.Error()
		}
	case "member-tun":
		response.Error = manager.relayMemberTUN(connection, request.MemberTUNInfo, files)
	case "member-backend":
		response.Error = manager.relayMemberBackend(connection, request.MemberTUNInfo, files)
	default:
		response.Error = "unsupported operation"
	}
	return json.NewEncoder(connection).Encode(response)
}

func (manager *sharedManager) relayMemberBackend(connection *net.UnixConn, info *MemberTUNInfo, files []*os.File) string {
	if info == nil || len(files) != 0 || info.BackendNamespacePID <= 0 {
		return "invalid grouped network backend handoff"
	}
	member, err := manager.memberForConnection(connection)
	if err != nil {
		return err.Error()
	}
	if info.BackendNamespacePID != member.holder.Process.Pid {
		return "grouped network backend target does not match its active namespace holder"
	}
	manager.mu.Lock()
	if member.tunnelSent {
		manager.mu.Unlock()
		return "grouped network data plane was already submitted"
	}
	member.tunnelSent = true
	parent := member.parent
	manager.mu.Unlock()
	if parent == nil {
		return "grouped launch control connection is unavailable"
	}
	data, err := json.Marshal(controlResponse{Operation: "member-backend", MemberTUNInfo: info})
	if err != nil {
		return fmt.Sprintf("encode grouped network backend handoff: %v", err)
	}
	n, _, err := parent.WriteMsgUnix(append(data, '\n'), nil, nil)
	if err != nil {
		return fmt.Sprintf("forward grouped network backend handoff: %v", err)
	}
	if n != len(data)+1 {
		return "forward grouped network backend handoff: incomplete control message"
	}
	_ = parent.SetReadDeadline(time.Now().Add(30 * time.Second))
	var ack controlResponse
	if err := json.NewDecoder(io.LimitReader(parent, maxControlMessage)).Decode(&ack); err != nil {
		return fmt.Sprintf("read grouped network backend acknowledgement: %v", err)
	}
	_ = parent.SetReadDeadline(time.Time{})
	if ack.Operation != "member-backend-ack" {
		return "outer launcher returned an invalid network backend acknowledgement"
	}
	return ack.Error
}

func (manager *sharedManager) relayMemberTUN(connection *net.UnixConn, info *MemberTUNInfo, files []*os.File) string {
	if info == nil || len(files) != 1 || info.InterfaceName == "" || info.Subnet == "" || info.GuestAddress == "" || info.ResolverEndpoint == "" || info.HostGatewayEndpoint == "" {
		return "invalid grouped policy TUN handoff"
	}
	member, err := manager.memberForConnection(connection)
	if err != nil {
		return err.Error()
	}
	manager.mu.Lock()
	if member.tunnelSent {
		manager.mu.Unlock()
		return "grouped policy TUN descriptor was already submitted"
	}
	member.tunnelSent = true
	parent := member.parent
	manager.mu.Unlock()
	if parent == nil {
		return "grouped launch control connection is unavailable"
	}
	data, err := json.Marshal(controlResponse{Operation: "member-tun", MemberTUNInfo: info})
	if err != nil {
		return fmt.Sprintf("encode grouped policy TUN handoff: %v", err)
	}
	oob := syscall.UnixRights(int(files[0].Fd()))
	n, _, err := parent.WriteMsgUnix(append(data, '\n'), oob, nil)
	if err != nil {
		return fmt.Sprintf("forward grouped policy TUN descriptor: %v", err)
	}
	if n != len(data)+1 {
		return "forward grouped policy TUN descriptor: incomplete control message"
	}
	_ = parent.SetReadDeadline(time.Now().Add(30 * time.Second))
	var ack controlResponse
	if err := json.NewDecoder(io.LimitReader(parent, maxControlMessage)).Decode(&ack); err != nil {
		return fmt.Sprintf("read grouped policy TUN acknowledgement: %v", err)
	}
	_ = parent.SetReadDeadline(time.Time{})
	if ack.Operation != "member-tun-ack" {
		return "outer launcher returned an invalid policy TUN acknowledgement"
	}
	return ack.Error
}

func (manager *sharedManager) runMember(connection *net.UnixConn, request controlRequest, streams []*os.File) (int, error) {
	if len(streams) != 3 {
		return 0, fmt.Errorf("grouped launch requires exactly three standard stream descriptors")
	}
	if len(request.Groups) == 0 || len(request.Groups) > 16 || len(request.Arguments) == 0 {
		return 0, fmt.Errorf("invalid grouped launch request")
	}
	if request.HostUID < 0 || request.HostGID < 0 || !mappedHostIdentity(request.HostUID, request.HostGID) {
		return 0, fmt.Errorf("grouped launch host identity does not match the manager's rootless mapping")
	}
	for _, argument := range request.Arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return 0, fmt.Errorf("grouped launch arguments contain a NUL byte")
		}
	}
	names, err := normalizedMemberNames(request.AppID, request.Aliases)
	if err != nil {
		return 0, err
	}

	manager.mu.Lock()
	configs := make([]networkConfig, 0, len(request.Groups))
	prefixes := make([]netip.Prefix, 0, len(request.Groups))
	seen := make(map[string]struct{}, len(request.Groups))
	for _, group := range request.Groups {
		if _, duplicate := seen[group]; duplicate {
			manager.mu.Unlock()
			return 0, fmt.Errorf("group %q is repeated in the launch request", group)
		}
		seen[group] = struct{}{}
		cfg, exists := manager.networks[group]
		if !exists {
			manager.mu.Unlock()
			return 0, fmt.Errorf("network group %q is not active", group)
		}
		prefix, err := validateConfig(cfg)
		if err != nil {
			manager.mu.Unlock()
			return 0, err
		}
		for _, previous := range prefixes {
			if prefix.Overlaps(previous) {
				manager.mu.Unlock()
				return 0, fmt.Errorf("network group %q overlaps another selected group subnet", group)
			}
		}
		configs = append(configs, cfg)
		prefixes = append(prefixes, prefix)
	}
	for _, cfg := range configs {
		for _, member := range manager.members {
			if !memberHasGroup(member, cfg.Name) {
				continue
			}
			for _, name := range names {
				for _, existing := range member.names {
					if name == existing {
						manager.mu.Unlock()
						return 0, fmt.Errorf("DNS name %q is already in use in network group %q", name, cfg.Name)
					}
				}
			}
		}
	}
	allocations, err := manager.allocateMemberAddresses(configs, prefixes)
	if err != nil {
		manager.mu.Unlock()
		return 0, err
	}
	holder, err := startMemberNamespace()
	if err != nil {
		manager.mu.Unlock()
		return 0, err
	}
	netNSInode, err := networkNamespaceInode(holder.Process.Pid)
	if err != nil {
		manager.mu.Unlock()
		stopMemberNamespace(holder)
		return 0, fmt.Errorf("inspect member network namespace: %w", err)
	}
	state := &memberState{holder: holder, netNSInode: netNSInode, names: names, allocations: allocations, parent: connection}
	for index := range state.allocations {
		state.allocations[index].NamespacePID = holder.Process.Pid
	}
	allocations = state.allocations
	manager.members[holder.Process.Pid] = state
	if err := setupMemberInterfaces(holder.Process.Pid, configs, allocations); err != nil {
		delete(manager.members, holder.Process.Pid)
		manager.mu.Unlock()
		stopMemberNamespace(holder)
		return 0, err
	}
	manager.mu.Unlock()

	defer func() {
		manager.mu.Lock()
		delete(manager.members, holder.Process.Pid)
		for _, allocation := range allocations {
			if link, linkErr := netlink.LinkByName(allocation.HostInterface); linkErr == nil {
				_ = netlink.LinkDel(link)
			}
		}
		manager.mu.Unlock()
		stopMemberNamespace(holder)
	}()

	assignments, err := json.Marshal(allocations)
	if err != nil {
		return 0, err
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate isobox for grouped launch: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return 0, fmt.Errorf("resolve isobox for grouped launch: %w", err)
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return 0, fmt.Errorf("nsenter is required for grouped launches: %w", err)
	}
	commandArgs := []string{"-t", strconv.Itoa(holder.Process.Pid), "-n", "--", executable, "run"}
	commandArgs = append(commandArgs, request.Arguments...)
	launchContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(launchContext, nsenter, commandArgs...)
	if !filepath.IsAbs(request.WorkingDirectory) {
		return 0, fmt.Errorf("grouped launch working directory must be absolute")
	}
	command.Dir = filepath.Clean(request.WorkingDirectory)
	command.Stdin, command.Stdout, command.Stderr = streams[0], streams[1], streams[2]
	command.Env = filteredLaunchEnvironment(request.Environment)
	command.Env = setEnv(command.Env, memberEnvKey, "1")
	command.Env = setEnv(command.Env, memberEnvAssignments, string(assignments))
	command.Env = setEnv(command.Env, memberEnvHostUID, strconv.Itoa(request.HostUID))
	command.Env = setEnv(command.Env, memberEnvHostGID, strconv.Itoa(request.HostGID))
	command.Env = setEnv(command.Env, memberEnvControlSocket, configs[0].ControlSocket)
	_ = connection.SetReadDeadline(time.Time{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-launchContext.Done():
				return
			case <-ticker.C:
				if unixConnectionClosed(connection) {
					cancel()
					return
				}
			}
		}
	}()
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), nil
		}
		return 0, fmt.Errorf("run sandbox in its group network namespace: %w", err)
	}
	return 0, nil
}

func unixConnectionClosed(connection *net.UnixConn) bool {
	raw, err := connection.SyscallConn()
	if err != nil {
		return true
	}
	closed := false
	controlErr := raw.Control(func(fd uintptr) {
		var buffer [1]byte
		n, _, receiveErr := syscall.Recvfrom(int(fd), buffer[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		if receiveErr == nil && n == 0 {
			closed = true
			return
		}
		if receiveErr != nil && !errors.Is(receiveErr, syscall.EAGAIN) && !errors.Is(receiveErr, syscall.EWOULDBLOCK) && !errors.Is(receiveErr, syscall.EINTR) {
			closed = true
		}
	})
	return controlErr != nil || closed
}

func normalizedMemberNames(appID string, aliases []string) ([]string, error) {
	if appID == "" {
		return nil, fmt.Errorf("--app-id is required for grouped networks because it is published in group DNS")
	}
	values := make([]string, 0, len(aliases)+1)
	values = append(values, appID)
	values = append(values, aliases...)
	names := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name, err := profile.NormalizeDomain(value)
		if err != nil {
			return nil, fmt.Errorf("group DNS name %q is invalid: %w", value, err)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("group DNS name %q is repeated", name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func memberHasGroup(member *memberState, group string) bool {
	for _, allocation := range member.allocations {
		if allocation.Group == group {
			return true
		}
	}
	return false
}

func (manager *sharedManager) resolveMemberDNS(connection *net.UnixConn, query []byte) ([]byte, bool, error) {
	if len(query) < 12 || len(query) > 4096 {
		return nil, false, fmt.Errorf("DNS query length must be between 12 and 4096 bytes")
	}
	member, err := manager.memberForConnection(connection)
	if err != nil {
		return nil, false, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	records := make(map[string][]net.IP)
	seenIPs := make(map[string]map[string]struct{})
	for _, target := range manager.members {
		for _, targetAllocation := range target.allocations {
			if !memberHasGroup(member, targetAllocation.Group) {
				continue
			}
			for _, name := range target.names {
				if seenIPs[name] == nil {
					seenIPs[name] = make(map[string]struct{})
				}
				if _, exists := seenIPs[name][targetAllocation.Address]; exists {
					continue
				}
				seenIPs[name][targetAllocation.Address] = struct{}{}
				records[name] = append(records[name], net.ParseIP(targetAllocation.Address).To4())
			}
		}
	}
	response, found, err := proxy.StaticDNSResponse(query, records)
	if err != nil {
		return nil, false, fmt.Errorf("build group DNS response: %w", err)
	}
	return response, found, nil
}

func (manager *sharedManager) memberForConnection(connection *net.UnixConn) (*memberState, error) {
	var peerPID int
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("inspect DNS client credentials: %w", err)
	}
	controlErr := raw.Control(func(fd uintptr) {
		credential, credentialErr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if credentialErr != nil {
			err = credentialErr
			return
		}
		peerPID = int(credential.Pid)
	})
	if controlErr != nil || err != nil {
		return nil, fmt.Errorf("inspect DNS client credentials: %w", errors.Join(controlErr, err))
	}
	inode, err := networkNamespaceInode(peerPID)
	if err != nil {
		return nil, fmt.Errorf("inspect DNS client network namespace: %w", err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, member := range manager.members {
		if member.netNSInode == inode {
			return member, nil
		}
	}
	return nil, fmt.Errorf("DNS client is not in an active isobox group namespace")
}

func (manager *sharedManager) allocateMemberAddresses(configs []networkConfig, prefixes []netip.Prefix) ([]memberAllocation, error) {
	used := make(map[string]map[netip.Addr]struct{}, len(configs))
	for _, member := range manager.members {
		for _, allocation := range member.allocations {
			if used[allocation.Group] == nil {
				used[allocation.Group] = make(map[netip.Addr]struct{})
			}
			used[allocation.Group][netip.MustParseAddr(allocation.Address)] = struct{}{}
		}
	}
	allocations := make([]memberAllocation, 0, len(configs))
	for index, cfg := range configs {
		prefix := prefixes[index]
		base := binary.BigEndian.Uint32(prefix.Addr().AsSlice())
		mask := ^uint32(0) << (32 - prefix.Bits())
		last := base | ^mask
		hostCount := int64(last - base - 2)
		if hostCount <= 0 {
			return nil, fmt.Errorf("network group %q has no member addresses", cfg.Name)
		}
		start, err := rand.Int(rand.Reader, big.NewInt(hostCount))
		if err != nil {
			return nil, fmt.Errorf("choose a random IPv4 address in network group %q: %w", cfg.Name, err)
		}
		var address netip.Addr
		for step := int64(0); step < hostCount; step++ {
			candidateOffset := (start.Int64() + step) % hostCount
			host := base + 2 + uint32(candidateOffset)
			candidate := netip.AddrFrom4([4]byte{byte(host >> 24), byte(host >> 16), byte(host >> 8), byte(host)})
			if _, occupied := used[cfg.Name][candidate]; !occupied {
				address = candidate
				break
			}
		}
		if !address.IsValid() {
			return nil, fmt.Errorf("network group %q has no free IPv4 addresses", cfg.Name)
		}
		var interfaceToken [5]byte
		if _, err := rand.Read(interfaceToken[:]); err != nil {
			return nil, fmt.Errorf("generate a unique member interface name: %w", err)
		}
		allocations = append(allocations, memberAllocation{
			Group: cfg.Name, Interface: fmt.Sprintf("g%d", index), Address: address.String(),
			Prefix: prefix.Bits(), HostInterface: fmt.Sprintf("ih%x%02x", interfaceToken, index),
		})
	}
	return allocations, nil
}

func (manager *sharedManager) verifyMember(connection *net.UnixConn) error {
	var peerPID int
	raw, err := connection.SyscallConn()
	if err != nil {
		return fmt.Errorf("inspect manager client credentials: %w", err)
	}
	controlErr := raw.Control(func(fd uintptr) {
		credential, credentialErr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if credentialErr != nil {
			err = credentialErr
			return
		}
		peerPID = int(credential.Pid)
	})
	if controlErr != nil {
		return fmt.Errorf("inspect manager client socket: %w", controlErr)
	}
	if err != nil {
		return fmt.Errorf("inspect manager client credentials: %w", err)
	}
	if peerPID <= 0 {
		return fmt.Errorf("manager client has an invalid process ID")
	}
	inode, err := networkNamespaceInode(peerPID)
	if err != nil {
		return fmt.Errorf("inspect manager client network namespace: %w", err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, member := range manager.members {
		if member.netNSInode == inode {
			return nil
		}
	}
	return fmt.Errorf("client process is not running in an active isobox group namespace")
}

func networkNamespaceInode(pid int) (uint64, error) {
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "ns", "net"))
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return 0, fmt.Errorf("network namespace inode is unavailable")
	}
	return stat.Ino, nil
}

func startMemberNamespace() (*exec.Cmd, error) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return nil, fmt.Errorf("unshare is required for grouped network namespaces: %w", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		return nil, fmt.Errorf("sleep is required to hold grouped network namespaces: %w", err)
	}
	command := exec.Command(unshare, "--net", "--", sleep, "infinity")
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("create member network namespace: %w", err)
	}
	managerInode, err := networkNamespaceInode(os.Getpid())
	if err != nil {
		stopMemberNamespace(command)
		return nil, fmt.Errorf("inspect manager network namespace: %w", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		memberInode, inspectErr := networkNamespaceInode(command.Process.Pid)
		if inspectErr != nil {
			stopMemberNamespace(command)
			return nil, fmt.Errorf("inspect new member network namespace: %w", inspectErr)
		}
		if memberInode != managerInode {
			return command, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopMemberNamespace(command)
	return nil, fmt.Errorf("member network namespace did not become distinct from the manager namespace")
}

func setupMemberInterfaces(pid int, configs []networkConfig, allocations []memberAllocation) error {
	ip, err := exec.LookPath("ip")
	if err != nil {
		return fmt.Errorf("iproute2 is required to configure grouped network interfaces: %w", err)
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return fmt.Errorf("nsenter is required to configure grouped network interfaces: %w", err)
	}
	sysctl, err := exec.LookPath("sysctl")
	if err != nil {
		return fmt.Errorf("sysctl is required to disable IPv6 in grouped network namespaces: %w", err)
	}
	if err := runNamespaceCommand(nsenter, pid, sysctl, "-q", "-w", "net.ipv6.conf.all.disable_ipv6=1", "net.ipv6.conf.default.disable_ipv6=1"); err != nil {
		return fmt.Errorf("disable IPv6 in member network namespace: %w", err)
	}
	cleanup := func() {
		for _, allocation := range allocations {
			if link, linkErr := netlink.LinkByName(allocation.HostInterface); linkErr == nil {
				_ = netlink.LinkDel(link)
			}
		}
	}
	for index, allocation := range allocations {
		peerName := fmt.Sprintf("p%x", pid)
		link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: allocation.HostInterface}, PeerName: peerName}
		if err := netlink.LinkAdd(link); err != nil {
			cleanup()
			return fmt.Errorf("create veth for network group %q: %w", allocation.Group, err)
		}
		host, err := netlink.LinkByName(allocation.HostInterface)
		if err != nil {
			cleanup()
			return fmt.Errorf("inspect host veth for network group %q: %w", allocation.Group, err)
		}
		peer, err := netlink.LinkByName(link.PeerName)
		if err != nil {
			cleanup()
			return fmt.Errorf("inspect member veth for network group %q: %w", allocation.Group, err)
		}
		bridge, err := netlink.LinkByName(configs[index].Bridge)
		if err != nil {
			cleanup()
			return fmt.Errorf("inspect bridge for network group %q: %w", allocation.Group, err)
		}
		if err := netlink.LinkSetMaster(host, bridge); err != nil {
			cleanup()
			return fmt.Errorf("attach veth to network group %q: %w", allocation.Group, err)
		}
		if err := netlink.LinkSetUp(host); err != nil {
			cleanup()
			return fmt.Errorf("bring up host veth for group %q: %w", allocation.Group, err)
		}
		if err := netlink.LinkSetNsPid(peer, pid); err != nil {
			cleanup()
			return fmt.Errorf("move veth into member network namespace for group %q: %w", allocation.Group, err)
		}
		if err := runIP(nsenter, ip, pid, "link", "set", "lo", "up"); err != nil {
			cleanup()
			return fmt.Errorf("enable member loopback: %w", err)
		}
		if err := runIP(nsenter, ip, pid, "link", "set", peerName, "name", allocation.Interface); err != nil {
			cleanup()
			return fmt.Errorf("rename group interface %q: %w", allocation.Group, err)
		}
		if err := runIP(nsenter, ip, pid, "-6", "addr", "flush", "dev", allocation.Interface); err != nil {
			cleanup()
			return fmt.Errorf("remove IPv6 addresses from group interface %q: %w", allocation.Group, err)
		}
		if err := runIP(nsenter, ip, pid, "addr", "add", allocation.Address+"/"+strconv.Itoa(allocation.Prefix), "dev", allocation.Interface); err != nil {
			cleanup()
			return fmt.Errorf("assign address to group interface %q: %w", allocation.Group, err)
		}
		if err := runIP(nsenter, ip, pid, "link", "set", "dev", allocation.Interface, "up"); err != nil {
			cleanup()
			return fmt.Errorf("bring up group interface %q: %w", allocation.Group, err)
		}
	}
	return nil
}

func runIP(nsenter, ip string, pid int, args ...string) error {
	return runNamespaceCommand(nsenter, pid, ip, args...)
}

func runNamespaceCommand(nsenter string, pid int, executable string, args ...string) error {
	commandArgs := []string{"-t", strconv.Itoa(pid), "-n", "--", executable}
	commandArgs = append(commandArgs, args...)
	output, err := exec.Command(nsenter, commandArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func stopMemberNamespace(holder *exec.Cmd) {
	if holder == nil || holder.Process == nil {
		return
	}
	_ = holder.Process.Kill()
	_ = holder.Wait()
}

func mappedHostIdentity(uid, gid int) bool {
	uidMap, uidErr := os.ReadFile("/proc/self/uid_map")
	gidMap, gidErr := os.ReadFile("/proc/self/gid_map")
	if uidErr != nil || gidErr != nil {
		return false
	}
	return mappingContains(string(uidMap), uid) && mappingContains(string(gidMap), gid)
}

func mappingContains(value string, id int) bool {
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		_, errInside := strconv.Atoi(fields[0])
		outside, errOutside := strconv.Atoi(fields[1])
		count, errCount := strconv.Atoi(fields[2])
		if errInside == nil && errOutside == nil && errCount == nil && id >= outside && id < outside+count {
			return true
		}
	}
	return false
}

func filteredLaunchEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, memberEnvKey+"=") || strings.HasPrefix(value, memberEnvAssignments+"=") || strings.HasPrefix(value, memberEnvHostUID+"=") || strings.HasPrefix(value, memberEnvHostGID+"=") || strings.HasPrefix(value, memberEnvControlSocket+"=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func setEnv(environment []string, name, value string) []string {
	prefix := name + "="
	for index, existing := range environment {
		if strings.HasPrefix(existing, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func readControlRequest(connection *net.UnixConn) (controlRequest, []*os.File, error) {
	data := make([]byte, maxControlMessage)
	oob := make([]byte, syscall.CmsgSpace(3*4))
	n, oobn, _, _, err := connection.ReadMsgUnix(data, oob)
	if err != nil {
		return controlRequest{}, nil, err
	}
	if n == len(data) {
		return controlRequest{}, nil, fmt.Errorf("network manager control request exceeds size limit")
	}
	var request controlRequest
	if err := json.Unmarshal(data[:n], &request); err != nil {
		return controlRequest{}, nil, fmt.Errorf("decode control request: %w", err)
	}
	var files []*os.File
	if oobn != 0 {
		messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return request, nil, fmt.Errorf("parse control descriptors: %w", err)
		}
		for _, message := range messages {
			fds, err := syscall.ParseUnixRights(&message)
			if err != nil {
				return request, files, fmt.Errorf("parse control descriptors: %w", err)
			}
			for _, fd := range fds {
				files = append(files, os.NewFile(uintptr(fd), "isobox-stream"))
			}
		}
	}
	return request, files, nil
}

func readControlResponseWithFD(connection *net.UnixConn) (controlResponse, *os.File, error) {
	data := make([]byte, maxControlMessage+1)
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := connection.ReadMsgUnix(data, oob)
	if err != nil {
		return controlResponse{}, nil, err
	}
	if n == len(data) {
		return controlResponse{}, nil, fmt.Errorf("network manager response exceeds size limit")
	}
	var response controlResponse
	if err := json.Unmarshal(bytes.TrimSpace(data[:n]), &response); err != nil {
		return controlResponse{}, nil, fmt.Errorf("decode network manager response: %w", err)
	}
	if oobn == 0 {
		return response, nil, nil
	}
	messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return response, nil, fmt.Errorf("parse response control descriptor: %w", err)
	}
	var received []*os.File
	for _, message := range messages {
		fds, err := syscall.ParseUnixRights(&message)
		if err != nil {
			for _, file := range received {
				_ = file.Close()
			}
			return response, nil, fmt.Errorf("parse response control descriptor: %w", err)
		}
		for _, fd := range fds {
			received = append(received, os.NewFile(uintptr(fd), "isobox-policy-tun"))
		}
	}
	if len(received) != 1 {
		for _, file := range received {
			_ = file.Close()
		}
		return response, nil, fmt.Errorf("expected one policy TUN descriptor, received %d", len(received))
	}
	return response, received[0], nil
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

func queryManager(path, name string) (managerStatus, error) {
	response, err := requestManager(path, controlRequest{Operation: managerStatusQuery, Name: name})
	return response.Status, err
}

func sendManagerRequest(path string, request controlRequest) error {
	_, err := requestManager(path, request)
	return err
}

func requestManager(path string, request controlRequest) (controlResponse, error) {
	connection, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return controlResponse{}, err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return controlResponse{}, err
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxControlMessage)).Decode(&response); err != nil {
		return controlResponse{}, err
	}
	if response.Error != "" {
		return controlResponse{}, fmt.Errorf("network manager: %s", response.Error)
	}
	return response, nil
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
	defer func() { _ = connection.Close() }()
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
		// #nosec G302 -- directories need execute permission for owner-only traversal.
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
	defer func() { _ = os.Remove(temporary) }()
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

func readAllNetworkConfigs() ([]networkConfig, error) {
	directory, err := networkDirectory(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []networkConfig{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list network configurations: %w", err)
	}
	configs := make([]networkConfig, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("unexpected entry %q in network configuration directory", entry.Name())
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		cfg, _, err := readConfig(name)
		if err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	return configs, nil
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
