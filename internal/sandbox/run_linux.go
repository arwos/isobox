//go:build linux

package sandbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

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
	return run(command, nil)
}

// RunWithPasta starts pasta with only the profile's declared host forwards.
// The bwrap child remains blocked until pasta owns every announced host
// socket; its first command then installs the egress filter before execing the
// application.
func RunWithPasta(command *exec.Cmd, pastaPath string, rules []profile.PortRule) (int, error) {
	return run(command, &pastaLaunch{path: pastaPath, rules: append([]profile.PortRule(nil), rules...)})
}

type pastaLaunch struct {
	path  string
	rules []profile.PortRule
}

func run(command *exec.Cmd, forwarding *pastaLaunch) (int, error) {
	if command == nil {
		return 0, fmt.Errorf("bwrap command is required")
	}
	var gateReader, gateWriter *os.File
	if forwarding != nil {
		var err error
		gateReader, gateWriter, err = os.Pipe()
		if err != nil {
			return 0, fmt.Errorf("create bwrap network setup gate: %w", err)
		}
		gateFD := 3 + len(command.ExtraFiles)
		if err := setOptionFD(command.Args, "--block-fd", gateFD); err != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
			return 0, err
		}
		command.ExtraFiles = append(command.ExtraFiles, gateReader)
	}
	statusFD := 3 + len(command.ExtraFiles)
	if err := setStatusFD(command.Args, statusFD); err != nil {
		if gateReader != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
		}
		return 0, err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		if gateReader != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
		}
		return 0, fmt.Errorf("create bwrap status pipe: %w", err)
	}
	defer statusReader.Close()
	command.ExtraFiles = append(command.ExtraFiles, statusWriter)
	if err := command.Start(); err != nil {
		_ = statusWriter.Close()
		if gateReader != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
		}
		return 0, fmt.Errorf("start bwrap: %w", err)
	}
	_ = statusWriter.Close()
	if gateReader != nil {
		_ = gateReader.Close()
	}

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
	var forwarder *pastaProcess
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
				if forwarding != nil && forwarder == nil {
					forwarder, err = startPasta(forwarding.path, childPID, forwarding.rules, command.Stdout, command.Stderr)
					if err == nil {
						forwarderDone = forwarder.done
						err = forwarder.waitReady()
					}
					if err != nil {
						if forwarder != nil {
							forwarder.stop()
						}
						_ = command.Process.Kill()
						_ = gateWriter.Close()
						<-waitResult
						return 125, fmt.Errorf("prepare host port forwarding: %w", err)
					}
					if _, err := gateWriter.Write([]byte{1}); err != nil {
						forwarder.stop()
						_ = command.Process.Kill()
						_ = gateWriter.Close()
						<-waitResult
						return 125, fmt.Errorf("release bwrap after network setup: %w", err)
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
				forwardingFailure = fmt.Errorf("pasta stopped while the application was running: %v", forwarder.waitErr)
				_ = command.Process.Kill()
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
	if groupID, err := syscall.Getpgid(pid); err == nil && groupID > 0 {
		if err := syscall.Kill(-groupID, sig); err == nil {
			return
		}
	}
	_ = syscall.Kill(pid, sig)
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
