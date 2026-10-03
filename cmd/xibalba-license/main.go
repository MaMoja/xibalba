// Command xibalba-license is the project's tool for sponsor licenses. It is
// used by the maintainer, not by operators of Xibalba.
//
//	xibalba-license keygen
//	    Print a new key pair. Done once for the project.
//
//	xibalba-license issue -key FILE -licensee NAME [-days N | -expires YYYY-MM-DD] [-sponsor ACCOUNT]
//	    Print a license file for a sponsor.
//
//	xibalba-license show FILE [-public HEX]
//	    Check a license file and print what it states.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MaMoja/xibalba/internal/license"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now()))
}

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "keygen":
		public, private, err := license.NewKeyPair()
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintf(stdout, "public key (build into the program, internal/license):\n%s\n\nprivate key (keep secret, never commit):\n%s\n", public, private)
		return 0

	case "issue":
		flags := flag.NewFlagSet("issue", flag.ContinueOnError)
		flags.SetOutput(stderr)
		keyFile := flags.String("key", "", "file holding the project's private key")
		licensee := flags.String("licensee", "", "organisation the license is issued to")
		sponsor := flags.String("sponsor", "", "sponsoring account (optional)")
		expires := flags.String("expires", "", "last day the license is valid, YYYY-MM-DD; instead of -days")
		days := flags.Int("days", 365, "how many days the license is valid from today")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if *keyFile == "" || *licensee == "" {
			_, _ = fmt.Fprintln(stderr, "issue needs -key and -licensee")
			return 2
		}
		if *expires == "" {
			if *days < 1 || *days > 36500 {
				_, _ = fmt.Fprintln(stderr, "-days must be from 1 to 36500")
				return 2
			}
			*expires = now.UTC().AddDate(0, 0, *days).Format("2006-01-02")
		}
		data, err := os.ReadFile(*keyFile)
		if err != nil {
			return fail(stderr, err)
		}
		private, err := license.ParsePrivateKey(string(data))
		if err != nil {
			return fail(stderr, err)
		}
		text, err := license.Issue(private, license.License{
			Licensee: *licensee, Sponsor: *sponsor, Issued: now.UTC().Format("2006-01-02"), Expires: *expires,
		})
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprint(stdout, text)
		return 0

	case "show":
		flags := flag.NewFlagSet("show", flag.ContinueOnError)
		flags.SetOutput(stderr)
		publicHex := flags.String("public", "", "public key to check against (default: the one built into this program)")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if flags.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "show needs one license file")
			return 2
		}
		key, err := license.PublicKey()
		if *publicHex != "" {
			key, err = license.ParsePublicKey(*publicHex)
		}
		if err != nil {
			return fail(stderr, err)
		}
		data, err := os.ReadFile(flags.Arg(0))
		if err != nil {
			return fail(stderr, err)
		}
		l, err := license.Parse(string(data), key)
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintf(stdout, "licensee: %s\nsponsor:  %s\nissued:   %s\nexpires:  %s\nstate:    %s\nid:       %s\n",
			l.Licensee, l.Sponsor, l.Issued, l.Expires, l.State(now), l.ID)
		return 0
	}
	usage(stderr)
	return 2
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "error:", err)
	return 1
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `xibalba-license: sponsor licenses for Xibalba (maintainer's tool)

  xibalba-license keygen
  xibalba-license issue -key FILE -licensee NAME [-days N | -expires YYYY-MM-DD] [-sponsor ACCOUNT]
  xibalba-license show FILE [-public HEX]
`)
}
