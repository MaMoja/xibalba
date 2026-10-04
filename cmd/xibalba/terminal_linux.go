package main

import (
	"os"
	"syscall"
	"unsafe"
)

// hideInput stops the terminal from showing what is typed. It returns the
// function that turns it back on, and whether f is a terminal at all.
func hideInput(f *os.File) (restore func(), terminal bool) {
	var state syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&state))); errno != 0 {
		return func() {}, false
	}
	hidden := state
	hidden.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&hidden))); errno != 0 {
		return func() {}, true
	}
	return func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&state)))
	}, true
}
