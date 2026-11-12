//go:build linux

package sandbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

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
	if network != nil && network.activeBackend() == "none" {
		return 0, fmt.Errorf("network supervisor requires a selected backend")
	}
	needsGate := network != nil || limits.Any(requested)
	var gateReader, gateWriter *os.File
	if needsGate {
		var err error
		gateReader, gateWriter, err = os.Pipe()
		if err != nil {
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
					forwarder, err = startNetworkBackend(*network, childPID, rules, command.Stdout, command.Stderr)
					if err == nil {
						forwarderDone = forwarder.doneChannel()
						err = forwarder.waitReady()
						if err == nil {
							err = forwarder.configureForwards(rules)
						}
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
						_ = gateWriter.Close()
						<-waitResult
						if network != nil {
							return 125, fmt.Errorf("prepare %s network and profile limits: %w", network.activeBackend(), err)
						}
						return 125, fmt.Errorf("apply profile resource limits: %w", err)
					}
					if _, err := gateWriter.Write([]byte{1}); err != nil {
						if forwarder != nil {
							forwarder.stop()
						}
						terminateLaunch(command, childPID)
						_ = gateWriter.Close()
						<-waitResult
						return 125, fmt.Errorf("release bwrap after startup checks: %w", err)
					}
					_ = gateWriter.Close()
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
