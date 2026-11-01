package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runWeb executes the CLI in an isolated home with the given stdin.
func runWeb(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"web"}, args...))
	err := root.Execute()
	return out.String(), err
}

func isolateCLI(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "TOKENMANAGER_CONFIG", "APPDATA"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	return home
}

func TestWebCommandOutputs(t *testing.T) {
	home := isolateCLI(t)
	tty := false
	stdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdinIsTerminal = func() bool { return false } })

	// Default location (with the normal incremental scan), replaced on rerun.
	def := filepath.Join(home, "data", "tokenmanager", "dashboard.html")
	for i := 0; i < 2; i++ {
		out, err := runWeb(t, "")
		if err != nil {
			t.Fatalf("default run %d: %v\n%s", i, err, out)
		}
		if !strings.Contains(out, def) {
			t.Fatalf("success message lacks absolute path:\n%s", out)
		}
	}
	if b, err := os.ReadFile(def); err != nil || !strings.Contains(string(b), `id="tm-data"`) {
		t.Fatalf("default dashboard not written: %v", err)
	}

	// Custom location.
	custom := filepath.Join(home, "out", "dash.html")
	if out, err := runWeb(t, "", "--no-scan", "--output", custom); err != nil {
		t.Fatalf("custom: %v\n%s", err, out)
	}
	if err := os.WriteFile(custom, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Existing custom file: refused without a terminal or --force.
	if _, err := runWeb(t, "", "--no-scan", "-o", custom); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected refusal mentioning --force, got %v", err)
	}
	// Interactive "no" keeps the file; "yes" replaces it.
	tty = true
	if _, err := runWeb(t, "n\n", "--no-scan", "-o", custom); err == nil {
		t.Fatal("declined confirmation should fail")
	}
	if b, _ := os.ReadFile(custom); string(b) != "mine" {
		t.Fatalf("declined overwrite changed the file: %q", b)
	}
	if out, err := runWeb(t, "y\n", "--no-scan", "-o", custom); err != nil || !strings.Contains(out, "Overwrite?") {
		t.Fatalf("confirmed overwrite: %v\n%s", err, out)
	}
	tty = false
	os.WriteFile(custom, []byte("mine"), 0o600)
	if out, err := runWeb(t, "", "--no-scan", "-o", custom, "--force"); err != nil {
		t.Fatalf("--force: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(custom); !strings.HasPrefix(string(b), "<!doctype html>") {
		t.Fatal("--force did not replace the file")
	}
}
