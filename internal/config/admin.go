package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/MaMoja/xibalba/internal/admin"
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

	// Password is the content of PasswordFile, parsed. Only set if Enabled.
	Password admin.Hash `yaml:"-"`
}

func defaultAdmin() Admin {
	return Admin{Enabled: false, Listen: "127.0.0.1:9091", PasswordFile: "admin.password", SessionLifetime: 12 * time.Hour}
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
	for name, listen := range others {
		if a.Enabled && a.Listen == listen && !strings.HasSuffix(listen, ":0") {
			add("admin.listen", fmt.Sprintf("%q is already used by %s", a.Listen, name), "give every listener its own port")
		}
	}
	if a.SessionLifetime < 5*time.Minute || a.SessionLifetime > 30*24*time.Hour {
		add("admin.session_lifetime", fmt.Sprintf("%s is out of range", a.SessionLifetime), `use a duration from "5m" to "720h"; "12h" is a working day`)
	}
	if strings.TrimSpace(a.PasswordFile) == "" {
		add("admin.password_file", "no file is named", `name the file that holds the stored password, for example "admin.password"`)
		return
	}
	if !a.Enabled {
		return
	}
	const set = "set a password with: xibalba -set-password -config <this file>"
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
