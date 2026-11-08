// Package agents defines the adapter architecture. Each adapter knows where an
// agent keeps its local data, which files hold usage, and how to parse them
// into normalized records.
package agents

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/parser"
)

// ReadMode tells the collector how a source file grows.
type ReadMode int

const (
	// Append files (JSONL) are read incrementally from the saved offset.
	Append ReadMode = iota
	// Whole files are rewritten in place and re-parsed in full on change.
	Whole
)

// ParseFunc parses a source file starting at the reader's position.
type ParseFunc func(r io.Reader, path string, st parser.State) ([]model.Record, int64, error)

// Adapter describes one coding agent integration.
type Adapter struct {
	Name     string // stable id used in config and records
	Display  string
	Provider string
	// Roots are directories searched (and watched) for usage files.
	Roots func() []string
	// Match reports whether a file under a root is a usage source.
	Match func(path string) bool
	Mode  ReadMode
	Parse ParseFunc
	// ParseTools, when set, is used instead of Parse and also reports tool calls.
	ParseTools parser.ToolParseFunc
	// Detect locates an installation even when no usage files exist.
	Detect func() []string
	// Availability documents what the agent exposes locally.
	Availability model.Accuracy
	Note         string
}

// Status is the discovery result for one adapter.
type Status struct {
	Adapter   *Adapter
	Enabled   bool
	Installed bool
	Roots     []string // existing roots
	Files     []string
}

// All returns every built-in adapter, Claude Code first.
func All() []*Adapter {
	return []*Adapter{claudeCode(), codex(), geminiCLI(), aider(), continueDev(), cursor()}
}

// ByName finds an adapter.
func ByName(name string) *Adapter {
	for _, a := range All() {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// Discover resolves roots (config overrides win) and lists usage files.
func Discover(cfg *config.Config, a *Adapter) Status {
	ac := cfg.Agent(a.Name)
	st := Status{Adapter: a, Enabled: ac.IsEnabled()}
	roots := ac.ExpandedPaths()
	if len(roots) == 0 && a.Roots != nil {
		roots = a.Roots()
	}
	for _, r := range uniq(roots) {
		info, err := os.Stat(r)
		if err != nil {
			continue
		}
		st.Roots = append(st.Roots, r)
		if !info.IsDir() {
			if a.Match == nil || a.Match(r) {
				st.Files = append(st.Files, r)
			}
			continue
		}
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, keep going
			}
			if !d.IsDir() && a.Match != nil && a.Match(p) {
				st.Files = append(st.Files, p)
			}
			return nil
		})
	}
	st.Installed = len(st.Roots) > 0
	if !st.Installed && a.Detect != nil {
		for _, p := range a.Detect() {
			if _, err := os.Stat(p); err == nil {
				st.Installed = true
				break
			}
		}
	}
	sort.Strings(st.Files)
	return st
}

func home(parts ...string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(append([]string{h}, parts...)...)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func claudeCode() *Adapter {
	return &Adapter{
		Name: "claude-code", Display: "Claude Code", Provider: "anthropic",
		Roots: func() []string {
			var r []string
			for _, d := range strings.Split(os.Getenv("CLAUDE_CONFIG_DIR"), ",") {
				if d = strings.TrimSpace(d); d != "" {
					r = append(r, filepath.Join(d, "projects"))
				}
			}
			return append(r, home(".claude", "projects"), home(".config", "claude", "projects"))
		},
		Match:        func(p string) bool { return strings.HasSuffix(p, ".jsonl") },
		Mode:         Append,
		Parse:        parser.ParseClaude,
		ParseTools:   parser.ParseClaudeTools,
		Availability: model.Actual,
		Note:         "Per-request usage from session transcripts (incl. subagents and cache tokens).",
	}
}

func codex() *Adapter {
	return &Adapter{
		Name: "codex", Display: "OpenAI Codex", Provider: "openai",
		Roots: func() []string {
			base := os.Getenv("CODEX_HOME")
			if base == "" {
				base = home(".codex")
			}
			return []string{filepath.Join(base, "sessions"), filepath.Join(base, "archived_sessions")}
		},
		Match: func(p string) bool {
			b := filepath.Base(p)
			return strings.HasPrefix(b, "rollout-") && strings.HasSuffix(b, ".jsonl")
		},
		Mode:         Append,
		Parse:        parser.ParseCodex,
		ParseTools:   parser.ParseCodexTools,
		Availability: model.Actual,
		Note:         "Per-request usage from rollout token_count events. Costs need a price for the model in config.yaml if it is not built in.",
	}
}

func geminiCLI() *Adapter {
	return &Adapter{
		Name: "gemini-cli", Display: "Gemini CLI", Provider: "google",
		Roots: func() []string { return []string{home(".gemini", "tmp")} },
		Match: func(p string) bool {
			return filepath.Base(filepath.Dir(p)) == "chats" && strings.HasSuffix(p, ".json")
		},
		Mode:         Whole,
		Parse:        parser.ParseGemini,
		ParseTools:   parser.ParseGeminiTools,
		Detect:       func() []string { return []string{home(".gemini")} },
		Availability: model.Actual,
		Note:         "Reads chat files saved by Gemini CLI (~/.gemini/tmp/*/chats). Projects are identified by hash.",
	}
}

func aider() *Adapter {
	return &Adapter{
		Name: "aider", Display: "Aider", Provider: "mixed",
		Roots: func() []string {
			wd, _ := os.Getwd()
			return []string{home(".aider.chat.history.md"), filepath.Join(wd, ".aider.chat.history.md")}
		},
		Match:        func(p string) bool { return filepath.Base(p) == ".aider.chat.history.md" },
		Mode:         Append,
		Parse:        parser.ParseAider,
		Detect:       func() []string { return []string{home(".aider.conf.yml"), home(".aider")} },
		Availability: model.Estimated,
		Note:         "Aider logs rounded token counts per project; add project directories under agents.aider.paths.",
	}
}

func continueDev() *Adapter {
	return &Adapter{
		Name: "continue", Display: "Continue", Provider: "mixed",
		Roots: func() []string { return []string{home(".continue", "dev_data")} },
		Match: func(p string) bool {
			b := filepath.Base(p)
			return b == "tokensGenerated.jsonl" || b == "tokens_generated.jsonl"
		},
		Mode:         Append,
		Parse:        parser.ParseContinue,
		Detect:       func() []string { return []string{home(".continue")} },
		Availability: model.Actual,
		Note:         "Reads Continue's local dev-data token log (prompt/generated tokens; no cache data).",
	}
}

func cursor() *Adapter {
	return &Adapter{
		Name: "cursor", Display: "Cursor", Provider: "mixed",
		Detect: func() []string {
			switch runtime.GOOS {
			case "darwin":
				return []string{home("Library", "Application Support", "Cursor"), home(".cursor")}
			case "windows":
				return []string{filepath.Join(os.Getenv("APPDATA"), "Cursor"), home(".cursor")}
			default:
				return []string{home(".config", "Cursor"), home(".cursor")}
			}
		},
		Availability: model.Unavailable,
		Note:         "Cursor keeps token usage server-side; it is not available from local files.",
	}
}
