//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"encoding/json"
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

const slirpGuestAddress = "10.0.2.100"

type slirpProcess struct {
	command    *exec.Cmd
	done       chan struct{}
	waitErr    error
	ready      chan error
	exitWriter *os.File
	tempDir    string
	apiSocket  string
}

func startSlirp(slirpPath string, namespacePID int, rules []profile.PortRule, stdout, stderr io.Writer) (*slirpProcess, error) {
	if slirpPath == "" {
		return nil, fmt.Errorf("slirp4netns executable is required for direct network mode")
	}
	if namespacePID <= 0 {
		return nil, fmt.Errorf("bwrap did not report a valid namespace process ID")
	}
	tempDir, err := os.MkdirTemp("", "isobox-slirp-")
	if err != nil {
		return nil, fmt.Errorf("create slirp4netns runtime directory: %w", err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create slirp4netns ready pipe: %w", err)
	}
	exitReader, exitWriter, err := os.Pipe()
	if err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create slirp4netns exit pipe: %w", err)
	}
	process := &slirpProcess{
		done:       make(chan struct{}),
		ready:      make(chan error, 1),
		exitWriter: exitWriter,
		tempDir:    tempDir,
	}
	args := []string{
		"--configure",
		"--ready-fd=3", "--exit-fd=4",
	}
	if len(rules) != 0 {
		process.apiSocket = filepath.Join(tempDir, "api.sock")
		args = append(args, "--api-socket="+process.apiSocket)
	}
	args = append(args, strconv.Itoa(namespacePID), "tap0")
	command := exec.Command(slirpPath, args...)
	command.ExtraFiles = []*os.File{readyWriter, exitReader}
	if stdout != nil {
		command.Stdout = stdout
	}
	if stderr != nil {
		command.Stderr = stderr
	}
	if err := command.Start(); err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = exitReader.Close()
		_ = exitWriter.Close()
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("start slirp4netns: %w", err)
	}
	process.command = command
	_ = readyWriter.Close()
	_ = exitReader.Close()
	go func() {
		data, err := io.ReadAll(readyReader)
		_ = readyReader.Close()
		if err == nil && strings.TrimSpace(string(data)) != "1" {
			err = fmt.Errorf("slirp4netns returned invalid readiness data %q", string(data))
		}
		process.ready <- err
	}()
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	return process, nil
}

func (process *slirpProcess) waitReady() error {
	timer := time.NewTimer(forwarderReadyTimeout)
	defer timer.Stop()
	select {
	case err := <-process.ready:
		if err != nil {
			return fmt.Errorf("wait for slirp4netns network setup: %w", err)
		}
		select {
		case <-process.done:
			return fmt.Errorf("slirp4netns exited during setup: %w", process.waitErr)
		default:
			return nil
		}
	case <-process.done:
		return fmt.Errorf("slirp4netns exited before network setup: %w", process.waitErr)
	case <-timer.C:
		return fmt.Errorf("slirp4netns did not configure the network within %s", forwarderReadyTimeout)
	}
}

func (process *slirpProcess) configureForwards(rules []profile.PortRule) error {
	if len(rules) == 0 {
		return nil
	}
	forwards, err := expandSlirpHostForwards(rules)
	if err != nil {
		return err
	}
	if process.apiSocket == "" {
		return fmt.Errorf("slirp4netns API socket is unavailable for declared host ports")
	}
	if err := waitForSlirpAPI(process.apiSocket); err != nil {
		return err
	}
	for _, forward := range forwards {
		if err := slirpAddHostForward(process.apiSocket, forward); err != nil {
			return fmt.Errorf("add slirp4netns %s host forward %s:%d: %w", forward.Protocol, forward.HostAddr, forward.HostPort, err)
		}
	}
	select {
	case <-process.done:
		return fmt.Errorf("slirp4netns exited while configuring host port forwards: %w", process.waitErr)
	default:
		return nil
	}
}

func expandSlirpHostForwards(rules []profile.PortRule) ([]slirpHostForward, error) {
	var forwards []slirpHostForward
	for index, rule := range rules {
		address := net.ParseIP(rule.HostAddress)
		if address == nil || address.To4() == nil || strings.Contains(rule.HostAddress, ":") {
			return nil, fmt.Errorf("profile.network.port[%d].host_address must be IPv4; IPv6 is disabled", index)
		}
		address = address.To4()
		start, end := rule.Port, rule.Port
		if rule.EndPort != 0 {
			end = rule.EndPort
		}
		var protocols []string
		switch rule.Protocol {
		case profile.PortProtocolBoth:
			protocols = []string{"tcp", "udp"}
		case profile.PortProtocolTCP:
			protocols = []string{"tcp"}
		case profile.PortProtocolUDP:
			protocols = []string{"udp"}
		default:
			return nil, fmt.Errorf("profile.network.port[%d].protocol %q is invalid", index, rule.Protocol)
		}
		for port := uint32(start); port <= uint32(end); port++ {
			for _, protocol := range protocols {
				forwards = append(forwards, slirpHostForward{
					Protocol:  protocol,
					HostAddr:  address.String(),
					HostPort:  int(port),
					GuestAddr: slirpGuestAddress,
					GuestPort: int(port),
				})
			}
		}
	}
	return forwards, nil
}

func (process *slirpProcess) stop() {
	if process == nil {
		return
	}
	select {
	case <-process.done:
	default:
		if process.exitWriter != nil {
			_, _ = process.exitWriter.Write([]byte{1})
			_ = process.exitWriter.Close()
			process.exitWriter = nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-process.done:
		case <-timer.C:
			if process.command != nil && process.command.Process != nil {
				_ = process.command.Process.Kill()
				<-process.done
			}
		}
		timer.Stop()
	}
	if process.exitWriter != nil {
		_ = process.exitWriter.Close()
		process.exitWriter = nil
	}
	if process.tempDir != "" {
		_ = os.RemoveAll(process.tempDir)
		process.tempDir = ""
	}
}

func (process *slirpProcess) doneChannel() <-chan struct{} { return process.done }

func (process *slirpProcess) error() error { return process.waitErr }

type slirpHostForward struct {
	Protocol  string `json:"proto"`
	HostAddr  string `json:"host_addr"`
	HostPort  int    `json:"host_port"`
	GuestAddr string `json:"guest_addr"`
	GuestPort int    `json:"guest_port"`
}

func waitForSlirpAPI(socketPath string) error {
	timer := time.NewTimer(forwarderReadyTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		connection, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ticker.C:
		case <-timer.C:
			return fmt.Errorf("slirp4netns API socket did not become ready: %w", lastErr)
		}
	}
}

func slirpAddHostForward(socketPath string, forward slirpHostForward) error {
	encoded, err := slirpHostForwardRequest(forward)
	if err != nil {
		return err
	}
	connection, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := connection.Write(encoded); err != nil {
		return err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("API connection is not a Unix socket")
	}
	if err := unixConnection.CloseWrite(); err != nil {
		return err
	}
	responseData, err := io.ReadAll(io.LimitReader(connection, 4097))
	if err != nil {
		return err
	}
	if len(responseData) > 4096 {
		return fmt.Errorf("API response exceeds 4096 bytes")
	}
	return validateSlirpHostForwardResponse(responseData)
}

func slirpHostForwardRequest(forward slirpHostForward) ([]byte, error) {
	address := net.ParseIP(forward.HostAddr)
	if address == nil || address.To4() == nil || strings.Contains(forward.HostAddr, ":") {
		return nil, fmt.Errorf("slirp host-forward address %q must be IPv4; IPv6 is disabled", forward.HostAddr)
	}
	request := struct {
		Execute   string           `json:"execute"`
		Arguments slirpHostForward `json:"arguments"`
	}{Execute: "add_hostfwd", Arguments: forward}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(encoded) >= 4096 {
		return nil, fmt.Errorf("API request exceeds the 4096-byte limit")
	}
	return encoded, nil
}

func validateSlirpHostForwardResponse(responseData []byte) error {
	var response struct {
		Return json.RawMessage `json:"return"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(responseData, &response); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	if len(response.Error) != 0 && string(response.Error) != "null" {
		return fmt.Errorf("API rejected forward: %s", response.Error)
	}
	if len(response.Return) == 0 || string(response.Return) == "null" {
		return fmt.Errorf("API response has no return value")
	}
	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(response.Return, &result); err != nil || result.ID <= 0 {
		return fmt.Errorf("API response did not confirm the host forward")
	}
	return nil
}
