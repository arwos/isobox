//go:build linux

package sandbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/profile"
)

type bwrapStatus struct {
	ChildPID int  `json:"child-pid"`
	ExitCode *int `json:"exit-code"`
}

// Run executes the application in bwrap and returns the application's
// shell-compatible exit status. The JSON status FD gives isobox the host PID
// of the sandboxed command so interactive signals can cross --new-session.
func Run(command *exec.Cmd) (int, error) {
	return run(command, nil, nil, profile.ResourceLimits{}, "")
}

// RunWithPasta keeps the legacy off-mode entry point while delegating to the
// selected-backend supervisor.
func RunWithPasta(command *exec.Cmd, pastaPath string, rules []profile.PortRule) (int, error) {
	network := NetworkRuntime{
		Mode:          profile.NetworkModeOff,
		PortForwarder: "pasta",
		Executable:    pastaPath,
	}
	return run(command, &network, append([]profile.PortRule(nil), rules...), profile.ResourceLimits{}, "")
}

// RunWithLimits holds the application behind bwrap's startup gate until all
// requested limits have been applied and verified.
func RunWithLimits(command *exec.Cmd, requested profile.ResourceLimits, systemdRun string) (int, error) {
	return run(command, nil, nil, requested, systemdRun)
}

// RunWithNetwork holds the application behind bwrap's startup gate until the
// selected backend and all requested resource limits are ready.
func RunWithNetwork(command *exec.Cmd, network NetworkRuntime, rules []profile.PortRule, requested profile.ResourceLimits, systemdRun string) (int, error) {
	return run(command, &network, append([]profile.PortRule(nil), rules...), requested, systemdRun)
}

func run(command *exec.Cmd, network *NetworkRuntime, rules []profile.PortRule, requested profile.ResourceLimits, systemdRun string) (int, error) {
	if command == nil {
		return 0, fmt.Errorf("bwrap command is required")
	}
	if network != nil && network.FallbackReason != "" {
		writer := command.Stderr
		if writer == nil {
			writer = os.Stderr
		}
		_, _ = fmt.Fprintf(writer, "isobox: %s\n", network.FallbackReason)
	}
	if network != nil && network.activeBackend() == "none" {
		return 0, fmt.Errorf("network supervisor requires a selected backend")
	}
	lateNetworkNamespace := network != nil && network.Mode == profile.NetworkModeOff && len(rules) != 0
	var hostNetworkNamespace uint64
	if lateNetworkNamespace {
		var err error
		hostNetworkNamespace, err = networkNamespaceInode("/proc/self/ns/net")
		if err != nil {
			return 0, fmt.Errorf("inspect host network namespace before off-mode launch: %w", err)
		}
	}
	needsGate := network != nil || limits.Any(requested)
	var gateReader, gateWriter *os.File
	var offGuardGate *offGuardStartupGate
	if needsGate {
		if lateNetworkNamespace {
			var err error
			offGuardGate, err = newOffGuardStartupGate()
			if err != nil {
				closeExtraFiles(command.ExtraFiles)
				return 0, fmt.Errorf("create off-mode startup gate: %w", err)
			}
			command.Args, err = addBwrapMount(command.Args, "--ro-bind", offGuardGate.directory, "/run/.isobox-control")
			if err != nil {
				_ = offGuardGate.Close()
				closeExtraFiles(command.ExtraFiles)
				return 0, err
			}
		} else {
			var err error
			gateReader, gateWriter, err = os.Pipe()
			if err != nil {
				closeExtraFiles(command.ExtraFiles)
				return 0, fmt.Errorf("create bwrap network setup gate: %w", err)
			}
			gateFD := 3 + len(command.ExtraFiles)
			if err := setOptionFD(command.Args, "--block-fd", gateFD); err != nil {
				_ = gateReader.Close()
				_ = gateWriter.Close()
				closeExtraFiles(command.ExtraFiles)
				return 0, err
			}
			command.ExtraFiles = append(command.ExtraFiles, gateReader)
		}
	}
	if offGuardGate != nil {
		defer offGuardGate.Close()
	}
	statusFD := 3 + len(command.ExtraFiles)
	if err := setStatusFD(command.Args, statusFD); err != nil {
		closeExtraFiles(command.ExtraFiles)
		if gateWriter != nil {
			_ = gateWriter.Close()
		}
		return 0, err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		closeExtraFiles(command.ExtraFiles)
		if gateWriter != nil {
			_ = gateWriter.Close()
		}
		return 0, fmt.Errorf("create bwrap status pipe: %w", err)
	}
	defer statusReader.Close()
	command.ExtraFiles = append(command.ExtraFiles, statusWriter)
	if limits.NeedsCgroup(requested) {
		command, err = limits.ScopeCommand(command, systemdRun, requested)
		if err != nil {
			closeExtraFiles(command.ExtraFiles)
			if gateWriter != nil {
				_ = gateWriter.Close()
			}
			return 0, err
		}
	}
	if err := command.Start(); err != nil {
		closeExtraFiles(command.ExtraFiles)
		if gateWriter != nil {
			_ = gateWriter.Close()
		}
		return 0, fmt.Errorf("start sandbox process: %w", err)
	}
	closeExtraFiles(command.ExtraFiles)
	command.ExtraFiles = nil

	statuses := make(chan bwrapStatus, 2)
	go readStatuses(statusReader, statuses)
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)

	var childPID int
	var pendingSignals []os.Signal
	statusChannel := (<-chan bwrapStatus)(statuses)
	var forwarder managedNetworkProcess
	var forwarderDone <-chan struct{}
	gateReleased := false
	sandboxExitReported := false
	var forwardingFailure error
	for {
		select {
		case status, ok := <-statusChannel:
			if !ok {
				statusChannel = nil
				continue
			}
			if status.ChildPID > 0 {
				childPID = status.ChildPID
				if network != nil && forwarder == nil {
					networkPID := childPID
					if lateNetworkNamespace {
						networkPID, err = offGuardGate.WaitForNamespace(hostNetworkNamespace, childPID)
					}
					if err == nil {
						forwarder, err = setupNetworkBackendWithFallback(network, networkPID, rules, command.Stdout, command.Stderr, startNetworkBackend)
					}
					if err == nil && forwarder != nil {
						forwarderDone = forwarder.doneChannel()
					}
				}
				if !gateReleased && needsGate {
					if err == nil {
						err = limits.VerifyCgroupPID(childPID, requested)
					}
					if err == nil {
						err = limits.ApplyOpenFilesPID(childPID, requested.OpenFiles)
					}
					if err != nil {
						if forwarder != nil {
							forwarder.stop()
						}
						terminateLaunch(command, childPID)
						if gateWriter != nil {
							_ = gateWriter.Close()
						}
						<-waitResult
						if network != nil {
							return 125, fmt.Errorf("prepare %s network and profile limits: %w", network.activeBackend(), err)
						}
						return 125, fmt.Errorf("apply profile resource limits: %w", err)
					}
					if offGuardGate != nil {
						err = offGuardGate.Release()
					} else {
						_, err = gateWriter.Write([]byte{1})
					}
					if err != nil {
						if forwarder != nil {
							forwarder.stop()
						}
						terminateLaunch(command, childPID)
						if gateWriter != nil {
							_ = gateWriter.Close()
						}
						<-waitResult
						return 125, fmt.Errorf("release application after startup checks: %w", err)
					}
					if gateWriter != nil {
						_ = gateWriter.Close()
					}
					gateReleased = true
				}
				for _, pending := range pendingSignals {
					forwardSignal(childPID, pending)
				}
				pendingSignals = nil
			}
			if status.ExitCode != nil {
				childPID = 0
				pendingSignals = nil
				sandboxExitReported = true
			}
		case received := <-signals:
			if childPID == 0 {
				pendingSignals = append(pendingSignals, received)
			} else {
				forwardSignal(childPID, received)
			}
		case <-forwarderDone:
			forwarderDone = nil
			if gateReleased && !sandboxExitReported {
				forwardingFailure = fmt.Errorf("%s stopped while the application was running: %v", network.activeBackend(), forwarder.error())
				terminateLaunch(command, childPID)
			}
		case waitErr := <-waitResult:
			if forwarder != nil {
				forwarder.stop()
			}
			if gateWriter != nil {
				_ = gateWriter.Close()
			}
			if forwardingFailure != nil {
				return 125, forwardingFailure
			}
			return processExitCode(waitErr)
		}
	}
}

func networkNamespaceInode(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%s did not return Linux namespace metadata", path)
	}
	return stat.Ino, nil
}

type offGuardStartupGate struct {
	listener   *net.UnixListener
	connection *net.UnixConn
	directory  string
}

func newOffGuardStartupGate() (*offGuardStartupGate, error) {
	directory, err := os.MkdirTemp("", "isobox-off-gate-")
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(directory, "gate.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("startup gate listener is not a Unix socket")
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = unixListener.Close()
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &offGuardStartupGate{listener: unixListener, directory: directory}, nil
}

func (gate *offGuardStartupGate) WaitForNamespace(hostNamespace uint64, processPID int) (int, error) {
	if gate == nil || gate.listener == nil {
		return 0, fmt.Errorf("off-mode startup gate is unavailable")
	}
	if err := gate.listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, err
	}
	connection, err := gate.listener.AcceptUnix()
	if err != nil {
		return 0, fmt.Errorf("accept internal helper network readiness: %w", err)
	}
	gate.connection = connection
	peerPID, err := unixPeerPID(connection)
	if err != nil {
		return 0, fmt.Errorf("inspect internal helper process credentials: %w", err)
	}
	if peerPID != processPID {
		parentPID, err := processParentPID(peerPID)
		if err != nil {
			return 0, fmt.Errorf("inspect internal helper parent PID: %w", err)
		}
		if parentPID != processPID {
			return 0, fmt.Errorf("internal helper peer PID %d has parent PID %d, want bwrap child PID %d", peerPID, parentPID, processPID)
		}
	}
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, err
	}
	var message []byte
	for len(message) < len("ready\n") {
		var value [1]byte
		if _, err := io.ReadFull(connection, value[:]); err != nil {
			return 0, fmt.Errorf("read internal helper network readiness: %w", err)
		}
		message = append(message, value[0])
	}
	if string(message) != "ready\n" {
		return 0, fmt.Errorf("internal helper sent an invalid network readiness message")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		pid, err := findNetworkNamespaceThread(peerPID, hostNamespace)
		if err == nil {
			if err := connection.SetDeadline(time.Time{}); err != nil {
				return 0, fmt.Errorf("clear internal helper startup deadline: %w", err)
			}
			return pid, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return 0, fmt.Errorf("find internal helper thread in isolated network namespace: %w", err)
		}
	}
}

func unixPeerPID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			socketErr = err
			return
		}
		pid = int(credentials.Pid)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	if pid <= 0 {
		return 0, fmt.Errorf("Unix peer returned invalid PID %d", pid)
	}
	return pid, nil
}

func processParentPID(pid int) (int, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	endCommand := strings.LastIndexByte(string(contents), ')')
	if endCommand < 0 {
		return 0, fmt.Errorf("process stat entry has no command terminator")
	}
	fields := strings.Fields(string(contents)[endCommand+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("process stat entry is incomplete")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent <= 0 {
		return 0, fmt.Errorf("process stat entry has invalid parent PID %q", fields[1])
	}
	return parent, nil
}

func findNetworkNamespaceThread(processPID int, hostNamespace uint64) (int, error) {
	taskDirectory := fmt.Sprintf("/proc/%d/task", processPID)
	threads, err := os.ReadDir(taskDirectory)
	if err != nil {
		return 0, fmt.Errorf("read helper thread list %s: %w", taskDirectory, err)
	}
	for _, thread := range threads {
		pid, err := strconv.Atoi(thread.Name())
		if err != nil || pid <= 0 {
			continue
		}
		path := filepath.Join(taskDirectory, thread.Name(), "ns", "net")
		inode, err := networkNamespaceInode(path)
		if err == nil && inode != hostNamespace {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no helper thread in %s has an isolated network namespace", taskDirectory)
}

func (gate *offGuardStartupGate) Release() error {
	if gate == nil || gate.connection == nil {
		return fmt.Errorf("off-mode startup gate is not connected")
	}
	_, writeErr := gate.connection.Write([]byte{1})
	connectionErr := gate.connection.Close()
	listenerErr := gate.listener.Close()
	removeErr := os.RemoveAll(gate.directory)
	if writeErr != nil {
		return writeErr
	}
	if connectionErr != nil {
		return connectionErr
	}
	if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
		return listenerErr
	}
	return removeErr
}

func (gate *offGuardStartupGate) Close() error {
	if gate == nil {
		return nil
	}
	var closeErr error
	if gate.connection != nil {
		closeErr = gate.connection.Close()
	}
	if gate.listener != nil {
		if err := gate.listener.Close(); closeErr == nil {
			closeErr = err
		}
	}
	removeErr := os.RemoveAll(gate.directory)
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return closeErr
	}
	return removeErr
}

func addBwrapMount(args []string, option, source, destination string) ([]string, error) {
	for index, argument := range args {
		if argument != "--" {
			continue
		}
		mountArgs := []string{option, source, destination}
		result := make([]string, 0, len(args)+len(mountArgs))
		result = append(result, args[:index]...)
		result = append(result, mountArgs...)
		result = append(result, args[index:]...)
		return result, nil
	}
	return nil, fmt.Errorf("bwrap command has no command delimiter for the internal startup gate mount")
}

func terminateLaunch(command *exec.Cmd, childPID int) {
	if childPID > 0 {
		if groupID, err := syscall.Getpgid(childPID); err == nil && shouldSignalProcessGroup(groupID, syscall.Getpgrp()) {
			_ = syscall.Kill(-groupID, syscall.SIGKILL)
		}
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	}
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
}

func startNetworkBackend(network NetworkRuntime, namespacePID int, rules []profile.PortRule, stdout, stderr io.Writer) (managedNetworkProcess, error) {
	switch network.activeBackend() {
	case "pasta":
		return startPasta(network.Executable, namespacePID, rules, network.Mode, network.PastaDNSArgs, stdout, stderr)
	case "slirp4netns":
		return startSlirp(network.Executable, namespacePID, rules, stdout, stderr)
	default:
		return nil, fmt.Errorf("unsupported network backend %q", network.activeBackend())
	}
}

type networkBackendStarter func(NetworkRuntime, int, []profile.PortRule, io.Writer, io.Writer) (managedNetworkProcess, error)

func setupNetworkBackendWithFallback(
	network *NetworkRuntime,
	namespacePID int,
	rules []profile.PortRule,
	stdout, stderr io.Writer,
	start networkBackendStarter,
) (managedNetworkProcess, error) {
	primary := *network
	process, primaryErr := startAndPrepareNetworkBackend(primary, namespacePID, rules, stdout, stderr, start)
	if primaryErr == nil {
		return process, nil
	}
	if primary.Fallback == nil {
		return nil, fmt.Errorf("%s setup failed: %w", primary.activeBackend(), primaryErr)
	}

	fallback := *primary.Fallback
	fallback.Fallback = nil
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "isobox: %s setup failed; trying compatible %s fallback: %v\n",
		primary.activeBackend(), fallback.activeBackend(), primaryErr)
	fallbackProcess, fallbackErr := startAndPrepareNetworkBackend(fallback, namespacePID, rules, stdout, stderr, start)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%s setup failed: %v; compatible %s fallback failed: %w",
			primary.activeBackend(), primaryErr, fallback.activeBackend(), fallbackErr)
	}
	*network = fallback
	return fallbackProcess, nil
}

func startAndPrepareNetworkBackend(
	network NetworkRuntime,
	namespacePID int,
	rules []profile.PortRule,
	stdout, stderr io.Writer,
	start networkBackendStarter,
) (managedNetworkProcess, error) {
	process, err := start(network, namespacePID, rules, stdout, stderr)
	if err == nil && process == nil {
		return nil, fmt.Errorf("%s startup returned no network process", network.activeBackend())
	}
	if err == nil {
		err = process.waitReady()
	}
	if err == nil {
		err = process.configureForwards(rules)
	}
	if err != nil && process != nil {
		process.stop()
	}
	return process, err
}

func closeExtraFiles(files []*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func readStatuses(reader *os.File, statuses chan<- bwrapStatus) {
	defer close(statuses)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var status bwrapStatus
		if err := json.Unmarshal(scanner.Bytes(), &status); err != nil {
			continue
		}
		if status.ChildPID > 0 || status.ExitCode != nil {
			statuses <- status
		}
	}
}

func forwardSignal(pid int, received os.Signal) {
	sig, ok := received.(syscall.Signal)
	if !ok || pid <= 0 {
		return
	}
	if groupID, err := syscall.Getpgid(pid); err == nil && shouldSignalProcessGroup(groupID, syscall.Getpgrp()) {
		if err := syscall.Kill(-groupID, sig); err == nil {
			return
		}
	}
	_ = syscall.Kill(pid, sig)
}

func shouldSignalProcessGroup(groupID, supervisorGroupID int) bool {
	return groupID > 0 && groupID != supervisorGroupID
}

func setStatusFD(args []string, fd int) error {
	for index, arg := range args {
		if arg != "--json-status-fd" {
			continue
		}
		if index+1 >= len(args) {
			return fmt.Errorf("bwrap --json-status-fd is missing its descriptor number")
		}
		args[index+1] = strconv.Itoa(fd)
		return nil
	}
	return fmt.Errorf("bwrap command does not include --json-status-fd for signal forwarding")
}

func setOptionFD(args []string, option string, fd int) error {
	for index, arg := range args {
		if arg != option {
			continue
		}
		if index+1 >= len(args) {
			return fmt.Errorf("bwrap %s is missing its descriptor number", option)
		}
		args[index+1] = strconv.Itoa(fd)
		return nil
	}
	return fmt.Errorf("bwrap command does not include %s for network startup coordination", option)
}

func processExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 0, err
	}
	if code := exitError.ExitCode(); code >= 0 {
		return code, nil
	}
	if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return 1, nil
}
