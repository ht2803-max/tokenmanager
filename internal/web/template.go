package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ht2803-max/tokenmanager/internal/config"
)

//go:embed assets/dashboard.html assets/dashboard.css assets/dashboard.js
var assets embed.FS

// Placeholders in assets/dashboard.html.
const (
	cssSlot  = "/*TM:CSS*/"
	jsSlot   = "/*TM:JS*/"
	dataSlot = "/*TM:DATA*/"
)

// Render produces the standalone page: the template with CSS, JS and the
// dashboard JSON inlined.
//
// The JSON sits in a <script type="application/json"> block. encoding/json
// escapes <, > and & (and U+2028/U+2029), so no value can close the script
// element or inject markup; the page reads it with JSON.parse.
func Render(d *Dashboard) ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	read := func(name string) (string, error) {
		b, err := assets.ReadFile("assets/" + name)
		return string(b), err
	}
	tpl, err := read("dashboard.html")
	if err != nil {
		return nil, err
	}
	css, err := read("dashboard.css")
	if err != nil {
		return nil, err
	}
	js, err := read("dashboard.js")
	if err != nil {
		return nil, err
	}
	// One pass over the template only: text inserted for one slot is never
	// scanned for another slot.
	page := strings.NewReplacer(cssSlot, css, jsSlot, js, dataSlot, string(data)).Replace(tpl)
	return []byte(page), nil
}

// DefaultPath is where "tokenmanager web" writes when --output is not given.
func DefaultPath() string { return filepath.Join(config.DataDir(), "dashboard.html") }

// ResolveOutput turns the --output value into an absolute file path. An
// existing directory gets dashboard.html inside it. custom reports whether
// the user chose the location (and so must confirm overwriting it).
func ResolveOutput(out string) (path string, custom bool, err error) {
	if out == "" {
		return DefaultPath(), false, nil
	}
	if out == "~" || strings.HasPrefix(out, "~/") || strings.HasPrefix(out, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", true, err
		}
		out = filepath.Join(home, out[1:])
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", true, err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		abs = filepath.Join(abs, "dashboard.html")
	}
	return abs, true, nil
}

// ErrExists is returned by Write when the target exists and overwrite is false.
var ErrExists = errors.New("output file already exists")

// Write stores page at path atomically with user-only permissions, creating
// parent directories. An existing file is replaced only when overwrite is set.
func Write(path string, page []byte, overwrite bool) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
		if !overwrite {
			return fmt.Errorf("%s: %w", path, ErrExists)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tokenmanager-dashboard-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(page); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// startCommand launches a process without waiting; replaced in tests.
var startCommand = func(name string, args ...string) error {
	return exec.Command(name, args...).Start()
}

// Open asks the operating system to show path in the default browser.
func Open(path string) error {
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = startCommand("open", path)
	case "windows":
		err = startCommand("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		err = startCommand("xdg-open", path)
	}
	if err != nil {
		return fmt.Errorf("could not open a browser: %w", err)
	}
	return nil
}
