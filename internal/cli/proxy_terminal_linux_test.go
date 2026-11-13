//go:build linux

package cli

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestDisableTerminalEchoRestoresOriginalMode(t *testing.T) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("pseudo-terminal is unavailable: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	defer master.Close()

	var unlock int32
	if err := ptyIoctl(masterFD, 0x40045431, unsafe.Pointer(&unlock)); err != nil { // TIOCSPTLCK
		t.Skipf("cannot unlock pseudo-terminal: %v", err)
	}
	var number uint32
	if err := ptyIoctl(masterFD, 0x80045430, unsafe.Pointer(&number)); err != nil { // TIOCGPTN
		t.Skipf("cannot inspect pseudo-terminal: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open pseudo-terminal slave: %v", err)
	}
	defer slave.Close()

	var before syscall.Termios
	if err := terminalIoctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&before)); err != nil {
		t.Skipf("cannot inspect pseudo-terminal mode: %v", err)
	}
	if before.Lflag&syscall.ECHO == 0 {
		t.Skip("pseudo-terminal starts with echo disabled")
	}
	restore, err := disableTerminalEcho(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	var hidden syscall.Termios
	if err := terminalIoctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&hidden)); err != nil {
		restore()
		t.Fatal(err)
	}
	if hidden.Lflag&syscall.ECHO != 0 {
		restore()
		t.Fatal("password input left terminal echo enabled")
	}
	restore()
	var after syscall.Termios
	if err := terminalIoctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&after)); err != nil {
		t.Fatal(err)
	}
	if after.Lflag&syscall.ECHO == 0 {
		t.Fatal("terminal echo was not restored after password input")
	}
}

func ptyIoctl(fd int, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}
