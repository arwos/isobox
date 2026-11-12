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
)

type bwrapStatus struct {
	ChildPID int  `json:"child-pid"`
	ExitCode *int `json:"exit-code"`
}

// Run executes the application in bwrap and returns the application's
// shell-compatible exit status. The JSON status FD gives isobox the host PID
// of the sandboxed command so interactive signals can cross --new-session.
func Run(command *exec.Cmd) (int, error) {
	if command == nil {
		return 0, fmt.Errorf("bwrap command is required")
	}
	statusFD := 3 + len(command.ExtraFiles)
	if err := setStatusFD(command.Args, statusFD); err != nil {
		return 0, err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return 0, fmt.Errorf("create bwrap status pipe: %w", err)
	}
	defer statusReader.Close()
	command.ExtraFiles = append(command.ExtraFiles, statusWriter)
	if err := command.Start(); err != nil {
		_ = statusWriter.Close()
		return 0, fmt.Errorf("start bwrap: %w", err)
	}
	_ = statusWriter.Close()

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
	for {
		select {
		case status, ok := <-statusChannel:
			if !ok {
				statusChannel = nil
				continue
			}
			if status.ChildPID > 0 {
				childPID = status.ChildPID
				for _, pending := range pendingSignals {
					forwardSignal(childPID, pending)
				}
				pendingSignals = nil
			}
			if status.ExitCode != nil {
				childPID = 0
				pendingSignals = nil
			}
		case received := <-signals:
			if childPID == 0 {
				pendingSignals = append(pendingSignals, received)
			} else {
				forwardSignal(childPID, received)
			}
		case waitErr := <-waitResult:
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
