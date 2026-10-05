package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/MaMoja/xibalba/internal/robots"
)

// robotsToRules writes the rule file made from the robots.txt at path to
// stdout, and what the person should know about it to stderr.
func robotsToRules(path string, opts robots.Options, stdin io.Reader, stdout, stderr io.Writer) int {
	input := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "the file %q cannot be opened; give the path of a robots.txt, or - to read standard input\n", path)
			return exitFailed
		}
		defer func() { _ = file.Close() }()
		input = file
	}
	// Nothing is written unless all of it can be: half a rule file is worse
	// than none.
	var out bytes.Buffer
	notes, err := robots.Convert(input, &out, opts)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "no rules written:", err)
		return exitFailed
	}
	if _, err := stdout.Write(out.Bytes()); err != nil {
		return exitFailed
	}
	for _, note := range notes {
		_, _ = fmt.Fprintln(stderr, "note:", note)
	}
	return exitOK
}
