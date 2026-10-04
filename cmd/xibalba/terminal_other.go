//go:build !linux

package main

import "os"

// hideInput cannot hide what is typed on this system. Pipe the password in.
func hideInput(*os.File) (restore func(), terminal bool) { return func() {}, false }
