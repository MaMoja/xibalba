package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/config"
)

// setPassword asks for the password of the web interface and writes its
// stored form to the file the configuration names. The password itself is
// never written anywhere.
func setPassword(configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	path, err := config.PasswordFile(configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitFailed
	}
	restore := func() {}
	typed := false
	if f, ok := stdin.(*os.File); ok {
		restore, typed = hideInput(f) // typed: a person at a terminal, not a pipe
	}
	reader := bufio.NewReader(io.LimitReader(stdin, 64<<10))
	ask := func(prompt string) string {
		if typed {
			_, _ = fmt.Fprint(stderr, prompt)
		}
		line, _ := reader.ReadString('\n')
		if typed {
			_, _ = fmt.Fprintln(stderr)
		}
		return strings.TrimRight(line, "\r\n")
	}
	password := ask(fmt.Sprintf("New password for the web interface (at least %d characters): ", admin.MinPasswordLength))
	if typed {
		if again := ask("The same password again: "); again != password {
			restore()
			_, _ = fmt.Fprintln(stderr, "The two entries differ. Nothing was changed.")
			return exitFailed
		}
	}
	restore()

	line, err := admin.HashPassword(password)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Nothing was changed: %v.\n", err)
		return exitFailed
	}
	// Written beside and moved into place, so a half-written file never
	// stands where the old one was. Readable by Xibalba's user only.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".password-*")
	if err == nil {
		_, err = tmp.WriteString(line + "\n")
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o600)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "The file %s could not be written. Nothing was changed.\n", path)
		return exitFailed
	}
	_, _ = fmt.Fprintf(stdout, "The password is set (%s). Restart Xibalba for it to take effect.\n", path)
	return exitOK
}
