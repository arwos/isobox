//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unsafe"
)

func readProxyCredentials() (string, string, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", "", fmt.Errorf("open interactive terminal; run this command from a terminal")
	}
	defer func() { _ = terminal.Close() }()
	reader := bufio.NewReaderSize(terminal, 1)
	if _, err := fmt.Fprint(terminal, "SOCKS5 username: "); err != nil {
		return "", "", fmt.Errorf("write credential prompt")
	}
	username, err := readTerminalLine(reader, terminal, false)
	if err != nil {
		return "", "", fmt.Errorf("read username")
	}
	if _, err := fmt.Fprint(terminal, "SOCKS5 password: "); err != nil {
		return "", "", fmt.Errorf("write password prompt")
	}
	password, err := readTerminalLine(reader, terminal, true)
	_, _ = fmt.Fprintln(terminal)
	if err != nil {
		return "", "", fmt.Errorf("read password")
	}
	return username, password, nil
}

func readTerminalLine(reader *bufio.Reader, terminal *os.File, hidden bool) (string, error) {
	restore := func() {}
	if hidden {
		var err error
		restore, err = disableTerminalEcho(terminal.Fd())
		if err != nil {
			return "", err
		}
	}
	defer restore()
	var value []byte
	for {
		character, err := reader.ReadByte()
		if err != nil {
			if errorsIsEOF(err) && len(value) > 0 {
				return string(value), nil
			}
			return "", err
		}
		if character == '\n' || character == '\r' {
			return string(value), nil
		}
		value = append(value, character)
	}
}

func errorsIsEOF(err error) bool { return errors.Is(err, io.EOF) }

func disableTerminalEcho(fd uintptr) (func(), error) {
	var original syscall.Termios
	if err := terminalIoctl(fd, syscall.TCGETS, unsafe.Pointer(&original)); err != nil {
		return nil, err
	}
	quiet := original
	quiet.Lflag &^= syscall.ECHO
	if err := terminalIoctl(fd, syscall.TCSETS, unsafe.Pointer(&quiet)); err != nil {
		return nil, err
	}
	var restoreOnce sync.Once
	restoreEcho := func() {
		restoreOnce.Do(func() { _ = terminalIoctl(fd, syscall.TCSETS, unsafe.Pointer(&original)) })
	}
	signals := make(chan os.Signal, 1)
	finished := make(chan struct{})
	var finishOnce sync.Once
	stop := func() {
		finishOnce.Do(func() {
			signal.Stop(signals)
			close(finished)
			restoreEcho()
		})
	}
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		select {
		case received := <-signals:
			restoreEcho()
			signal.Stop(signals)
			if unixSignal, ok := received.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), unixSignal)
			}
		case <-finished:
		}
	}()
	return stop, nil
}

func terminalIoctl(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}
