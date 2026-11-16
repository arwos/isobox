//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// proxyTUNStartupGate reuses the private, owner-only Unix listener lifecycle
// while adding a single TUN descriptor transfer from the bwrap helper.
type proxyTUNStartupGate struct {
	*offGuardStartupGate
}

func newProxyTUNStartupGate() (*proxyTUNStartupGate, error) {
	gate, err := newOffGuardStartupGate()
	if err != nil {
		return nil, err
	}
	return &proxyTUNStartupGate{offGuardStartupGate: gate}, nil
}

func (gate *proxyTUNStartupGate) WaitForTUN(hostNamespace uint64, processPID int, tunNames ...string) (int, *os.File, error) {
	if gate == nil || gate.listener == nil {
		return 0, nil, fmt.Errorf("proxy TUN startup gate is unavailable")
	}
	if err := gate.listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, nil, err
	}
	connection, err := gate.listener.AcceptUnix()
	if err != nil {
		return 0, nil, fmt.Errorf("accept proxy TUN descriptor: %w", err)
	}
	gate.connection = connection
	peerPID, err := unixPeerPID(connection)
	if err != nil {
		return 0, nil, fmt.Errorf("inspect proxy TUN helper credentials: %w", err)
	}
	if peerPID != processPID {
		parentPID, err := processParentPID(peerPID)
		if err != nil {
			return 0, nil, fmt.Errorf("inspect proxy TUN helper parent PID: %w", err)
		}
		if parentPID != processPID {
			return 0, nil, fmt.Errorf("proxy TUN helper peer PID %d has parent PID %d, want bwrap child PID %d", peerPID, parentPID, processPID)
		}
	}
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, nil, err
	}
	message, descriptors, err := readProxyTUNDescriptor(connection)
	if err != nil {
		return 0, nil, fmt.Errorf("receive proxy TUN readiness: %w", err)
	}
	if string(message) != "ready\n" || len(descriptors) != 1 {
		closeDescriptors(descriptors)
		return 0, nil, fmt.Errorf("proxy TUN helper sent an invalid readiness message or descriptor count")
	}
	descriptor := descriptors[0]
	syscall.CloseOnExec(descriptor)
	tunName := proxyTUNName
	if len(tunNames) != 0 && tunNames[0] != "" {
		tunName = tunNames[0]
	}
	if err := verifyTUNDescriptor(descriptor, tunName); err != nil {
		_ = syscall.Close(descriptor)
		return 0, nil, err
	}
	namespacePID, err := findNetworkNamespaceThread(peerPID, hostNamespace)
	if err != nil {
		_ = syscall.Close(descriptor)
		return 0, nil, fmt.Errorf("verify proxy TUN helper network namespace: %w", err)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = syscall.Close(descriptor)
		return 0, nil, fmt.Errorf("clear proxy TUN startup deadline: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), filepath.Join("/dev/net", tunName))
	if file == nil {
		_ = syscall.Close(descriptor)
		return 0, nil, fmt.Errorf("wrap received proxy TUN descriptor")
	}
	return namespacePID, file, nil
}

// PrepareIngress asks the trusted in-namespace helper to install its marked
// pasta return route after the host forwarder has configured the tap link.
func (gate *proxyTUNStartupGate) PrepareIngress() error {
	if gate == nil || gate.connection == nil {
		return fmt.Errorf("proxy TUN startup gate is not connected")
	}
	if err := gate.connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if _, err := io.WriteString(gate.connection, "forwarder-ready\n"); err != nil {
		return fmt.Errorf("signal pasta ingress readiness: %w", err)
	}
	response := make([]byte, len("prepared\n"))
	if _, err := io.ReadFull(gate.connection, response); err != nil {
		return fmt.Errorf("read proxy ingress preparation result: %w", err)
	}
	if string(response) != "prepared\n" {
		return fmt.Errorf("proxy ingress helper rejected pasta route setup")
	}
	if err := gate.connection.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear proxy ingress startup deadline: %w", err)
	}
	return nil
}

func readProxyTUNDescriptor(connection *net.UnixConn) ([]byte, []int, error) {
	message := make([]byte, 0, len("ready\n"))
	descriptors := make([]int, 0, 1)
	data := make([]byte, len("ready\n"))
	oob := make([]byte, syscall.CmsgSpace(4*4))
	for len(message) < len("ready\n") {
		n, oobn, flags, _, err := connection.ReadMsgUnix(data, oob)
		if err != nil {
			closeDescriptors(descriptors)
			return nil, nil, err
		}
		if flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 {
			if oobn > 0 {
				received, _ := parseProxyTUNRights(oob[:oobn])
				closeDescriptors(received)
			}
			closeDescriptors(descriptors)
			return nil, nil, fmt.Errorf("truncated proxy TUN readiness message or descriptor control message")
		}
		message = append(message, data[:n]...)
		if oobn == 0 {
			continue
		}
		received, err := parseProxyTUNRights(oob[:oobn])
		if err != nil {
			closeDescriptors(descriptors)
			return nil, nil, err
		}
		descriptors = append(descriptors, received...)
	}
	return message, descriptors, nil
}

func parseProxyTUNRights(control []byte) ([]int, error) {
	controlMessages, err := syscall.ParseSocketControlMessage(control)
	if err != nil {
		return nil, fmt.Errorf("parse proxy TUN control message: %w", err)
	}
	var descriptors []int
	invalid := false
	for _, controlMessage := range controlMessages {
		if controlMessage.Header.Type != syscall.SCM_RIGHTS {
			invalid = true
			continue
		}
		received, err := syscall.ParseUnixRights(&controlMessage)
		if err != nil {
			closeDescriptors(descriptors)
			return nil, fmt.Errorf("parse transferred TUN descriptors: %w", err)
		}
		descriptors = append(descriptors, received...)
		if controlMessage.Header.Level != syscall.SOL_SOCKET {
			invalid = true
		}
	}
	if invalid {
		closeDescriptors(descriptors)
		return nil, fmt.Errorf("unexpected control message on proxy TUN startup gate")
	}
	return descriptors, nil
}

func verifyTUNDescriptor(descriptor int, tunNames ...string) error {
	tunName := proxyTUNName
	if len(tunNames) != 0 && tunNames[0] != "" {
		tunName = tunNames[0]
	}
	var request tunInterfaceRequest
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(descriptor), tunGetIFF, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(&request)
	if errno != 0 {
		return fmt.Errorf("verify transferred TUN descriptor: %w", errno)
	}
	if string(request.name[:len(tunName)]) != tunName || request.flags&tunIFFTun == 0 || request.flags&tunIFFTap != 0 || request.flags&tunIFFNoPI == 0 {
		return fmt.Errorf("transferred descriptor is not the expected IPv4 TUN interface")
	}
	return nil
}

func closeDescriptors(descriptors []int) {
	for _, descriptor := range descriptors {
		_ = syscall.Close(descriptor)
	}
}
