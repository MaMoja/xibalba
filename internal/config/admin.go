package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/changes"
)

// Admin holds the settings of the web interface. It is documented in
// docs/ADMIN.md and implemented in internal/admin.
type Admin struct {
	// Enabled switches the web interface on. Off, nothing listens.
	Enabled bool `yaml:"enabled"`
	// Listen is the host:port the web interface binds to.
	Listen string `yaml:"listen"`
	// PasswordFile holds the stored password, written by
	// "xibalba -set-password". Relative to the configuration file.
	PasswordFile string `yaml:"password_file"`
	// SessionLifetime is how long a login lasts.
	SessionLifetime time.Duration `yaml:"session_lifetime"`
	// Hostnames are the names the web interface answers to, besides
	// localhost and this machine's own local addresses.
	Hostnames []string `yaml:"hostnames"`
	// SecureCookie marks the login cookie for HTTPS only.
	SecureCookie bool `yaml:"secure_cookie"`

	// AllowChanges lets the web interface change things: switch presets,
	// list addresses. Off, it only shows.
	AllowChanges bool `yaml:"allow_changes"`
	// ChangesFile keeps what was changed in the web interface, relative to
	// the configuration file. What it holds applies even while
	// AllowChanges is off.
	ChangesFile string `yaml:"changes_file"`

	// Changes is the content of ChangesFile; ChangesPath is where it is.
	Changes     changes.State `yaml:"-"`
	ChangesPath string        `yaml:"-"`
	// PasswordOpen says that the password file can be read by other users.
	PasswordOpen bool `yaml:"-"`

	// Password is the content of PasswordFile, parsed. Only set if Enabled.
	Password admin.Hash `yaml:"-"`
}

func defaultAdmin() Admin {
	return Admin{Enabled: false, Listen: "127.0.0.1:9091", PasswordFile: "admin.password", SessionLifetime: 12 * time.Hour, Hostnames: []string{},
		AllowChanges: false, ChangesFile: "admin.changes.json"}
}

func resolve(dir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

func (a *Admin) check(dir string, others map[string]string, add func(path, message, hint string)) {
	a.Password = admin.Hash{}
	if err := checkListen(a.Listen); err != nil {
		add("admin.listen", fmt.Sprintf("%q is not a listen address: %v", a.Listen, err), `use host:port, for example "127.0.0.1:9091"`)
	}
	for i, name := range a.Hostnames {
		if name == "" || len(name) > 253 || strings.ContainsAny(name, "/:@ ?#") && net.ParseIP(name) == nil {
			add(fmt.Sprintf("admin.hostnames[%d]", i), fmt.Sprintf("%q is not a host name", name),
				`give the bare name under which you open the web interface, without "https://" and without a port, for example "xibalba.example.org"`)
		}
	}
	for name, listen := range others {
		if a.Enabled && sameListener(a.Listen, listen) {
			add("admin.listen", fmt.Sprintf("%q is already used by %s", a.Listen, name), "give every listener its own port")
		}
	}
	if a.SessionLifetime < 5*time.Minute || a.SessionLifetime > 30*24*time.Hour {
		add("admin.session_lifetime", fmt.Sprintf("%s is out of range", a.SessionLifetime), `use a duration from "5m" to "720h"; "12h" is a working day`)
	}
	if a.AllowChanges && !a.Enabled {
		add("admin.allow_changes", "changes are allowed, and the web interface they would be made in is switched off",
			"set admin.enabled to true, or admin.allow_changes to false")
	}
	if strings.TrimSpace(a.PasswordFile) == "" {
		add("admin.password_file", "no file is named", `name the file that holds the stored password, for example "admin.password"`)
		return
	}
	if !a.Enabled {
		return
	}
	const set = "set a password with: xibalba -set-password -config <this file>"
	a.PasswordOpen = false
	if info, err := os.Stat(resolve(dir, a.PasswordFile)); err == nil && info.Mode().Perm()&0o077 != 0 {
		a.PasswordOpen = true
	}
	raw, err := os.ReadFile(resolve(dir, a.PasswordFile))
	switch {
	case os.IsNotExist(err):
		add("admin.password_file", fmt.Sprintf("the web interface is switched on and %q does not exist", a.PasswordFile), set)
	case err != nil:
		add("admin.password_file", fmt.Sprintf("%q cannot be read", a.PasswordFile), "make the file readable for the user Xibalba runs as")
	default:
		if a.Password, err = admin.ParseHash(string(raw)); err != nil {
			add("admin.password_file", fmt.Sprintf("%q does not hold a stored password: %v", a.PasswordFile, err), set)
		}
	}
}

// sameListener reports whether two listen addresses would collide: the same
// port, and hosts that are equal or of which one means "all addresses".
func sameListener(a, b string) bool {
	hostA, portA, errA := net.SplitHostPort(a)
	hostB, portB, errB := net.SplitHostPort(b)
	if errA != nil || errB != nil || portA != portB || portA == "0" {
		return false
	}
	all := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	local := func(h string) string {
		if h == "localhost" {
			return "127.0.0.1"
		}
		return h
	}
	return all(hostA) || all(hostB) || local(hostA) == local(hostB)
}

// PasswordFile returns where the configuration at name keeps the stored
// password of the web interface. It reads only that one setting, so it also
// works on a configuration that is not valid yet because the password is
// still missing.
func PasswordFile(name string) (string, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("the configuration %s cannot be read", name)
	}
	file := struct {
		Admin struct {
			PasswordFile string `yaml:"password_file"`
		} `yaml:"admin"`
	}{}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return "", fmt.Errorf("the configuration %s is not valid YAML", name)
	}
	path := file.Admin.PasswordFile
	if strings.TrimSpace(path) == "" {
		path = defaultAdmin().PasswordFile
	}
	return resolve(filepath.Dir(name), path), nil
}
