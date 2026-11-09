package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/status"
)

// statusRefresh is how often `status --watch` re-scans sessions.
const statusRefresh = 2 * time.Second

func statusCmd() *cobra.Command {
	var watch, stale bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what each agent session is doing: working, waiting, done or idle",
		Long: `Show the live state of every Claude Code and Codex session on this machine,
grouped by project:

  waiting   blocked on you (permission prompt or question)
  working   generating or running tools
  done      finished its turn; the next move is yours
  idle      no activity for status.idle_after (default 10m)

State is inferred from the agents' session files. "waiting" is a guess
(marked "?"): a tool call with no result and no writes for
status.waiting_after looks the same as a slow tool. Sessions inactive for
status.stale_after (default 2h) are hidden unless --stale is given.

Run "tokenmanager daemon start" to get a notification when a session starts
waiting. (Not to be confused with "tokenmanager daemon status".)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch && (fl.json || fl.csv) {
				return errors.New("--watch cannot be combined with --json or --csv")
			}
			// No collector scan and no database: this reads session file tails only.
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			sc := status.NewScanner(cfg)
			sc.IncludeStale = stale
			if watch {
				_, err := tea.NewProgram(&statusModel{scan: sc, now: time.Now}, tea.WithAltScreen()).Run()
				return err
			}
			evs := filterStatus(sc.Scan())
			w := cmd.OutOrStdout()
			switch {
			case fl.json:
				return writeJSON(w, statusJSON{GeneratedAt: sc.Now(), Sessions: nonNil(evs)})
			case fl.csv:
				return statusTable(evs, sc.Now(), false).csv(w)
			}
			renderStatus(w, evs, sc.Now())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "live view, refreshed every 2s (q to quit)")
	cmd.Flags().BoolVar(&stale, "stale", false, "include sessions inactive for longer than status.stale_after")
	return cmd
}

type statusJSON struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Sessions    []model.StatusEvent `json:"sessions"`
}

func nonNil(evs []model.StatusEvent) []model.StatusEvent {
	if evs == nil {
		return []model.StatusEvent{}
	}
	return evs
}

// filterStatus applies --agent and --project.
func filterStatus(evs []model.StatusEvent) []model.StatusEvent {
	var out []model.StatusEvent
	for _, ev := range evs {
		if fl.agent != "" && ev.Agent != fl.agent {
			continue
		}
		if fl.project != "" && !strings.Contains(ev.Project, fl.project) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// groupByProject orders sessions so each project's rows are together, with
// the project holding the most urgent session first.
func groupByProject(evs []model.StatusEvent) []model.StatusEvent {
	first := map[string]int{} // evs arrive sorted by urgency
	for i, ev := range evs {
		if _, ok := first[ev.Project]; !ok {
			first[ev.Project] = i
		}
	}
	out := append([]model.StatusEvent(nil), evs...)
	sort.SliceStable(out, func(i, j int) bool { return first[out[i].Project] < first[out[j].Project] })
	return out
}

func statusTable(evs []model.StatusEvent, now time.Time, styled bool) *table {
	t := newTable("Project", "Agent", "State", "Last activity", "Detail")
	t.alignRight(3)
	prev := "\x00"
	for _, ev := range groupByProject(evs) {
		proj := shortPath(ev.Project)
		if styled && ev.Project == prev {
			proj = ""
		}
		prev = ev.Project
		t.add(proj, ev.Agent, stateCell(ev, styled), format.Duration(now.Sub(ev.At))+" ago", ev.Detail)
	}
	return t
}

func stateCell(ev model.StatusEvent, styled bool) string {
	s := string(ev.State)
	if ev.Confidence == model.Low {
		s += "?"
	}
	if !styled {
		return s
	}
	switch ev.State {
	case model.StateWaiting:
		return sWarn.Bold(true).Render(s)
	case model.StateWorking:
		return sAccent.Render(s)
	case model.StateDone:
		return sGood.Render(s)
	}
	return sDim.Render(s)
}

func statusSummary(evs []model.StatusEvent) string {
	n := map[model.State]int{}
	for _, ev := range evs {
		n[ev.State]++
	}
	var parts []string
	for _, s := range []model.State{model.StateWaiting, model.StateWorking, model.StateDone, model.StateIdle, model.StateStale} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
		}
	}
	if len(parts) == 0 {
		return "no active sessions"
	}
	return strings.Join(parts, " · ")
}

func renderStatus(w io.Writer, evs []model.StatusEvent, now time.Time) {
	heading(w, "agent status", statusSummary(evs))
	fmt.Fprintln(w)
	if len(evs) == 0 {
		fmt.Fprintln(w, sDim.Render("  No Claude Code or Codex session was active recently (use --stale to see older ones)."))
		fmt.Fprintln(w)
		return
	}
	statusTable(evs, now, true).render(w)
	for _, ev := range evs {
		if ev.Confidence == model.Low {
			fmt.Fprintln(w)
			fmt.Fprintln(w, sDim.Render("  ? inferred from session files: a slow tool can look like a permission prompt."))
			break
		}
	}
	fmt.Fprintln(w)
}

// shortPath replaces the home directory with ~.
func shortPath(p string) string {
	if p == "" {
		return "—"
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// ---------------------------------------------------------------- --watch

type statusTick time.Time

type statusModel struct {
	scan *status.Scanner
	evs  []model.StatusEvent
	now  func() time.Time
}

func (m *statusModel) refresh() { m.evs = filterStatus(m.scan.Scan()) }

func (m *statusModel) Init() tea.Cmd {
	m.refresh()
	return statusTickCmd()
}

func statusTickCmd() tea.Cmd {
	return tea.Tick(statusRefresh, func(t time.Time) tea.Msg { return statusTick(t) })
}

func (m *statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.refresh()
		}
	case statusTick:
		m.refresh()
		return m, statusTickCmd()
	}
	return m, nil
}

func (m *statusModel) View() string {
	var b strings.Builder
	renderStatus(&b, m.evs, m.now())
	b.WriteString(sDim.Render("  refreshes every 2s · r refresh · q quit") + "\n")
	return b.String()
}
