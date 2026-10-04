package main

import (
	"os"
	"syscall"
	"unsafe"
)

// hideInput stops the terminal from showing what is typed. It returns the
// function that turns it back on, and whether the typing is hidden.
func hideInput(f *os.File) (restore func(), hidden bool) {
	var state syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&state))); errno != 0 {
		return func() {}, false
	}
	quiet := state
	quiet.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&quiet))); errno != 0 {
		return func() {}, false
	}
	return func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&state)))
	}, true
}
