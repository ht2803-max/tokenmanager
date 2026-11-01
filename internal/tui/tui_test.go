package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

func fixtureModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := config.Default()
	empty := t.TempDir()
	for _, a := range []string{"claude-code", "codex", "gemini-cli", "aider", "continue"} {
		cfg.Agents[a] = config.AgentConfig{Paths: []string{empty}}
	}
	cfg.Budgets.Global.Monthly = 100
	cfg.Budgets.PerAgent = map[string]config.Budget{"claude-code": {Daily: 5}}
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	var recs []model.Record
	agentsList := []string{"claude-code", "codex", "aider"}
	models := []string{"claude-opus-5-5", "gpt-5.6-terra", "claude-sonnet-4-5"}
	providers := []string{"anthropic", "openai", "anthropic"}
	for i := 0; i < 400; i++ {
		a := i % 3
		recs = append(recs, model.Record{
			Key: fmt.Sprint("k", i), Agent: agentsList[a], Provider: providers[a], Model: models[a],
			SessionID: fmt.Sprint("session-", i/20), Project: fmt.Sprintf("/Users/me/code/a-rather-long-project-name-%d/sub/dir", i%7),
			Timestamp: now.Add(-time.Duration(i) * 97 * time.Minute), Input: int64(100 + i), Output: int64(300 + i*3),
			CacheRead: int64(50_000 + i*900), CacheWrite5m: int64(2000 + i), Accuracy: model.Actual,
		})
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	// A current Codex snapshot so the Limits view renders reported windows.
	err = db.SaveRateLimits(model.RateLimits{Provider: "openai", Agent: "codex", Plan: "plus", Observed: now,
		Primary:   &model.RateLimitWindow{UsedPercent: 42, WindowMinutes: 300, ResetsAt: now.Add(2 * time.Hour)},
		Secondary: &model.RateLimitWindow{UsedPercent: 12, WindowMinutes: 10080, ResetsAt: now.Add(72 * time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	m := &Model{cfg: cfg, db: db, eng: analytics.New(cfg, db), search: textinput.New(), periodIdx: 3}
	m.view = vOptimize // load findings too
	m.data = m.load()().(dataMsg)
	if m.data.err != nil {
		t.Fatal(m.data.err)
	}
	m.view = vOverview
	return m
}

// Every view must fill the terminal exactly: no overflowing or wrapped lines.
func TestViewsFitTerminal(t *testing.T) {
	m := fixtureModel(t)
	sizes := [][2]int{{60, 18}, {80, 24}, {100, 30}, {132, 40}, {220, 60}}
	for _, sz := range sizes {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		for v := range viewNames {
			for _, detail := range []bool{false, true} {
				m.view, m.detail = v, detail
				m.cursor[v] = 1
				out := m.View()
				lines := strings.Split(out, "\n")
				if len(lines) != sz[1] {
					t.Errorf("%s %dx%d detail=%v: %d lines, want %d", viewNames[v], sz[0], sz[1], detail, len(lines), sz[1])
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz[0] {
						t.Errorf("%s %dx%d detail=%v: line %d is %d cells wide", viewNames[v], sz[0], sz[1], detail, i, w)
						break
					}
				}
			}
		}
	}
}

func TestKeyboardNavigation(t *testing.T) {
	m := fixtureModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	key := func(s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
	key("4")
	if m.view != vSessions {
		t.Fatalf("view = %d", m.view)
	}
	key("j")
	key("j")
	if m.cursor[vSessions] != 2 {
		t.Fatalf("cursor = %d", m.cursor[vSessions])
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.detail {
		t.Fatal("enter should open details")
	}
	key("/")
	for _, r := range "sub/dir" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.search.Value() != "sub/dir" || len(m.filteredSessions()) == 0 {
		t.Fatalf("search = %q, %d results", m.search.Value(), len(m.filteredSessions()))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	key("0")
	if m.view != vLimits {
		t.Fatalf("0 should open Limits, view = %d", m.view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.view != vOverview {
		t.Fatalf("→ from Limits should wrap to Overview, view = %d", m.view)
	}
	key("?")
	if !m.help {
		t.Fatal("? should open help")
	}
}

// TestRenderDump prints every view for manual inspection:
// TOKENMANAGER_DUMP=1 go test ./internal/tui -run Dump -v
func TestRenderDump(t *testing.T) {
	if testing.Short() || !envSet("TOKENMANAGER_DUMP") {
		t.Skip("set TOKENMANAGER_DUMP=1 to print views")
	}
	m := fixtureModel(t)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
	for v := range viewNames {
		m.view = v
		fmt.Println(m.View())
	}
}
func envSet(k string) bool { return os.Getenv(k) != "" }
