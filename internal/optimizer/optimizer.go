// Package optimizer analyzes usage and suggests ways to reduce token spend.
//
// Every saving is an estimate derived from recent usage and list prices; the
// optimizer reads project files to measure them but never modifies anything.
package optimizer

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// Severity orders findings.
type Severity int

const (
	Info Severity = iota
	Low
	Medium
	High
)

func (s Severity) String() string {
	return [...]string{"info", "low", "medium", "high"}[s]
}

// Finding is one recommendation.
type Finding struct {
	Severity       Severity `json:"-"`
	SeverityName   string   `json:"severity"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Project        string   `json:"project,omitempty"`
	File           string   `json:"file,omitempty"`
	Detail         string   `json:"detail"`
	Recommendation string   `json:"recommendation"`
	// TokensPerSession is the estimated per-session token reduction (0 if n/a).
	TokensPerSession int64 `json:"tokens_per_session,omitempty"`
	// MonthlySavings is an estimate in the configured currency (0 if unknown).
	MonthlySavings float64 `json:"estimated_monthly_savings"`
}

// Window is how much history the optimizer looks at.
const Window = 30 * 24 * time.Hour

// Instruction files that agents load into every session.
var instructionFiles = []string{"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "GEMINI.md", ".cursorrules", "CONVENTIONS.md", ".github/copilot-instructions.md"}

// premium models and the cheaper alternative used for "what if" pricing.
var alternatives = map[string]string{
	"claude-fable-5-1": "claude-sonnet-5-5",
	"claude-fable-5":   "claude-sonnet-5-5",
	"claude-opus-5-5":  "claude-sonnet-5-5",
	"claude-opus-5":    "claude-sonnet-5-5",
	"claude-opus-4-8":  "claude-sonnet-5-5",
	"claude-opus-4-7":  "claude-sonnet-4-6",
	"claude-opus-4-6":  "claude-sonnet-4-6",
	"claude-opus-4-1":  "claude-sonnet-4-5",
	"claude-opus-4":    "claude-sonnet-4",
	"gpt-5":            "gpt-5-mini",
	"o3":               "o4-mini",
}

// Analyze runs every check over the last 30 days.
func Analyze(e *analytics.Engine, f storage.Filter) ([]Finding, error) {
	now := e.Now()
	recs, err := e.Records(analytics.Period{From: now.Add(-Window), To: now.Add(time.Minute)}, f)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	a := &analyzer{e: e, recs: recs, now: now}
	days := now.Sub(recs[0].Timestamp).Hours() / 24
	a.monthly = 30 / max(days, 1)
	if a.tools, err = e.Tools(analytics.Period{From: now.Add(-Window), To: now.Add(time.Minute)}, f, recs); err != nil {
		return nil, err
	}

	a.instructionFiles()
	a.cacheEfficiency()
	a.largePrompts()
	a.modelSelection()
	a.outlierSessions()
	a.longSessions()
	a.projectConcentration()
	a.largeToolResults()
	a.rereadResults()
	a.shellFloods()
	a.mcpOverhead()
	a.carriedContext()

	sort.SliceStable(a.out, func(i, j int) bool {
		if a.out[i].Severity != a.out[j].Severity {
			return a.out[i].Severity > a.out[j].Severity
		}
		return a.out[i].MonthlySavings > a.out[j].MonthlySavings
	})
	for i := range a.out {
		a.out[i].SeverityName = a.out[i].Severity.String()
	}
	return a.out, nil
}

type analyzer struct {
	e       *analytics.Engine
	recs    []model.Record
	now     time.Time
	monthly float64 // multiplier from the observed window to 30 days
	tools   analytics.ToolReport
	out     []Finding
}

func (a *analyzer) add(f Finding) { a.out = append(a.out, f) }

func (a *analyzer) price(m string) (in, read, write float64, ok bool) {
	p, ok := a.e.Prices.Lookup(m)
	return p.Input, p.CacheRead, p.CacheWrite5m, ok
}

func severityFor(monthly float64) Severity {
	switch {
	case monthly >= 50:
		return High
	case monthly >= 10:
		return Medium
	case monthly >= 1:
		return Low
	}
	return Info
}

// instructionFiles flags large or repetitive instruction files that are sent
// with every request in a project.
func (a *analyzer) instructionFiles() {
	byProject := a.e.Group(a.recs, analytics.ByProject)
	type target struct {
		dir string
		agg *analytics.Agg
	}
	var targets []target
	for _, g := range byProject {
		if filepath.IsAbs(g.Key) {
			targets = append(targets, target{g.Key, g})
		}
	}
	home, _ := os.UserHomeDir()
	all := a.e.Summary(a.recs)
	globals := []string{filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(home, ".codex", "AGENTS.md"), filepath.Join(home, ".gemini", "GEMINI.md")}

	check := func(path string, agg *analytics.Agg, project string) {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return
		}
		tokens := int64(len(data)) / 4 // ~4 bytes per token (estimate)
		dups := duplicateLines(data)
		const target = 1500
		if tokens < 2500 && dups < 5 {
			return
		}
		excess := max(tokens-target, int64(dups)*12)
		_, read, _, ok := a.price(agg.TopModel())
		monthlyReqs := float64(agg.Requests) * a.monthly
		var savings float64
		if ok {
			// Instruction files sit in the cached prefix, so the saving is
			// at least the cache-read price on every request.
			savings = float64(excess) * monthlyReqs * read / 1e6
		}
		perSession := int64(0)
		if s := agg.Sessions(); s > 0 {
			perSession = excess * agg.Requests / int64(s)
		}
		detail := fmt.Sprintf("~%s tokens loaded into every request", format.Tokens(tokens))
		if dups > 0 {
			detail += fmt.Sprintf("; %d repeated lines", dups)
		}
		a.add(Finding{
			Severity: max(severityFor(savings), Low), Kind: "instruction-file",
			Title: "Large instruction file", Project: project, File: path,
			Detail:           detail,
			Recommendation:   "Remove redundant instructions, consolidate repeated context, and move rarely needed detail into files the agent reads on demand.",
			TokensPerSession: perSession, MonthlySavings: savings,
		})
	}
	for _, t := range targets {
		for _, name := range instructionFiles {
			check(filepath.Join(t.dir, name), t.agg, t.dir)
		}
	}
	for _, g := range globals {
		check(g, &all, "(global)")
	}
}

func duplicateLines(data []byte) int {
	seen := map[string]bool{}
	dups := 0
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if len(l) < 25 || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "```") {
			continue
		}
		if seen[l] {
			dups++
		}
		seen[l] = true
	}
	return dups
}

// cacheEfficiency flags low cache-hit rates and heavy cache re-writing
// (context that keeps changing and must be re-cached: repeated context).
func (a *analyzer) cacheEfficiency() {
	for _, g := range a.e.Group(a.recs, analytics.ByAgent) {
		in, read, write, ok := a.price(g.TopModel())
		if !ok || g.Prompt() < 2_000_000 {
			continue
		}
		if hit := g.CacheHitRate(); hit < 0.5 && g.Key != "aider" && g.Key != "continue" {
			gain := (0.8 - hit) * float64(g.Prompt()) * (in - read) / 1e6 * a.monthly
			a.add(Finding{
				Severity: severityFor(gain), Kind: "cache-hit-rate",
				Title:          "Low prompt-cache hit rate",
				Detail:         fmt.Sprintf("%s: only %.0f%% of %s prompt tokens were served from cache", g.Key, hit*100, format.Tokens(g.Prompt())),
				Recommendation: "Keep the start of the context stable (instructions, tool lists) and avoid editing files the agent has already read early in the session; volatile content placed early invalidates the cache.",
				MonthlySavings: gain,
			})
		}
		if g.CacheRead > 0 && float64(g.CacheWrite) > 0.25*float64(g.CacheRead) && g.CacheWrite > 1_000_000 {
			excess := float64(g.CacheWrite) - 0.1*float64(g.CacheRead)
			gain := excess * (write - read) / 1e6 * a.monthly
			a.add(Finding{
				Severity: severityFor(gain), Kind: "cache-churn",
				Title:          "Repeated context re-cached",
				Detail:         fmt.Sprintf("%s: %s tokens written to cache vs %s read (%.0f%%). Context is being rebuilt instead of reused.", g.Key, format.Tokens(g.CacheWrite), format.Tokens(g.CacheRead), 100*float64(g.CacheWrite)/float64(g.CacheRead)),
				Recommendation: "Long pauses let the cache expire, and switching models or tool sets rebuilds it. Batch related work into one sitting and avoid switching models mid-session.",
				MonthlySavings: gain,
			})
		}
	}
}

// largePrompts flags requests that carry very large contexts.
func (a *analyzer) largePrompts() {
	const big = 150_000
	type agg struct {
		n      int64
		excess float64
	}
	m := map[string]*agg{}
	var total int64
	for _, r := range a.recs {
		if r.PromptTokens() < big {
			continue
		}
		_, read, _, ok := a.price(r.Model)
		g := m[r.Agent]
		if g == nil {
			g = &agg{}
			m[r.Agent] = g
		}
		g.n++
		total++
		if ok {
			g.excess += float64(r.PromptTokens()-100_000) * read / 1e6
		}
	}
	for agent, g := range m {
		share := float64(g.n) / float64(len(a.recs))
		if g.n < 10 {
			continue
		}
		gain := g.excess * a.monthly
		a.add(Finding{
			Severity: max(severityFor(gain), Low), Kind: "excessive-context",
			Title:          "Excessive context per request",
			Detail:         fmt.Sprintf("%s: %d requests (%.0f%%) sent more than %s prompt tokens", agent, g.n, share*100, format.Tokens(big)),
			Recommendation: "Compact or clear the conversation between tasks, start new sessions for unrelated work, and avoid pasting large files when a path or excerpt would do.",
			MonthlySavings: gain,
		})
	}
}

// modelSelection finds premium-model sessions doing light work.
func (a *analyzer) modelSelection() {
	type agg struct {
		cost, alt float64
		sessions  map[string]bool
		reqs      int64
		out       int64
	}
	m := map[string]*agg{}
	// Sessions with little generation per request; heavy generation may
	// justify a premium model. Pricing is per record so mixed-model sessions
	// are compared correctly.
	light := map[string]bool{}
	for _, s := range a.e.Sessions(a.recs) {
		if s.Requests >= 5 && s.Output/s.Requests <= 400 {
			light[s.Key] = true
		}
	}
	for _, r := range a.recs {
		alt, ok := alternatives[normalize(r.Model)]
		if !ok || !light[r.Agent+"/"+r.SessionID] {
			continue
		}
		c, ok1 := a.e.Prices.Cost(r)
		r2 := r
		r2.Model, r2.ReportedCost, r2.Speed = alt, 0, ""
		c2, ok2 := a.e.Prices.Cost(r2)
		if !ok1 || !ok2 {
			continue
		}
		g := m[r.Model+"→"+alt]
		if g == nil {
			g = &agg{sessions: map[string]bool{}}
			m[r.Model+"→"+alt] = g
		}
		g.cost += c
		g.alt += c2
		g.reqs++
		g.out += r.Output
		g.sessions[r.SessionID] = true
	}
	for k, g := range m {
		gain := (g.cost - g.alt) * a.monthly
		if gain < 1 {
			continue
		}
		parts := strings.SplitN(k, "→", 2)
		a.add(Finding{
			Severity: severityFor(gain / 2), Kind: "model-selection",
			Title:          "Premium model used for light tasks",
			Detail:         fmt.Sprintf("%d sessions on %s averaged %s output tokens per request. The same requests on %s would cost ~%s less this period.", len(g.sessions), parts[0], format.Tokens(g.out/max(g.reqs, 1)), parts[1], format.Money(g.cost-g.alt)),
			Recommendation: fmt.Sprintf("Use %s (or lower effort) for quick edits, questions and exploration, and reserve %s for hard reasoning tasks. Savings assume similar quality, which you should verify.", parts[1], parts[0]),
			MonthlySavings: gain,
		})
	}
}

// outlierSessions lists unusually expensive sessions.
func (a *analyzer) outlierSessions() {
	ss := a.e.Sessions(a.recs)
	if len(ss) < 5 {
		return
	}
	costs := make([]float64, 0, len(ss))
	for _, s := range ss {
		costs = append(costs, s.Cost)
	}
	sort.Float64s(costs)
	median := costs[len(costs)/2]
	p95 := costs[int(float64(len(costs)-1)*0.95)]
	limit := max(median*5, p95, 1)
	sort.Slice(ss, func(i, j int) bool { return ss[i].Cost > ss[j].Cost })
	n := 0
	for _, s := range ss {
		if s.Cost <= limit || n >= 3 {
			break
		}
		n++
		a.add(Finding{
			Severity: Medium, Kind: "expensive-session",
			Title:   "Unusually expensive session",
			Project: analytics.ShortProject(s.Project),
			Detail: fmt.Sprintf("%s session %s on %s cost %s (median %s): %d requests, %s tokens, peak prompt %s",
				s.Agent, shortID(s.ID), s.First.Local().Format("Jan 2"), format.Money(s.Cost), format.Money(median), s.Requests, format.Tokens(s.Tokens()), format.Tokens(s.MaxPrompt)),
			Recommendation: "Review what drove this session: long-running loops, large file reads, or a premium model left on. Splitting big tasks into focused sessions keeps context small.",
		})
	}
}

// longSessions flags sessions whose context keeps growing.
func (a *analyzer) longSessions() {
	n := 0
	for _, s := range a.e.Sessions(a.recs) {
		if s.Requests < 150 || s.ContextWindow == 0 {
			continue
		}
		fill := float64(s.MaxPrompt) / float64(s.ContextWindow)
		if fill < 0.4 {
			continue
		}
		n++
		if n > 3 {
			break
		}
		a.add(Finding{
			Severity: Low, Kind: "context-management",
			Title:          "Long session near context limit",
			Project:        analytics.ShortProject(s.Project),
			Detail:         fmt.Sprintf("%s session %s: %d requests, context reached %s of %s (%.0f%%)", s.Agent, shortID(s.ID), s.Requests, format.Tokens(s.MaxPrompt), format.Tokens(s.ContextWindow), fill*100),
			Recommendation: "Every request resends the whole context. Clear or compact after finishing a subtask, and keep notes in a file instead of the conversation.",
		})
	}
}

// projectConcentration reports projects dominating spend.
func (a *analyzer) projectConcentration() {
	groups := a.e.Group(a.recs, analytics.ByProject)
	total := a.e.Summary(a.recs)
	if len(groups) < 2 || total.Cost <= 0 {
		return
	}
	g := groups[0]
	share := g.Cost / total.Cost
	if share < 0.5 {
		return
	}
	a.add(Finding{
		Severity: Info, Kind: "project-hotspot",
		Title:          "High token consumption project",
		Project:        analytics.ShortProject(g.Key),
		Detail:         fmt.Sprintf("%.0f%% of spend (%s, %s tokens, %d sessions) in the last 30 days", share*100, format.Money(g.Cost), format.Tokens(g.Tokens()), g.Sessions()),
		Recommendation: "Focus optimization here first: check its instruction files, typical session length, and model choice.",
	})
}

func normalize(m string) string {
	m = strings.ToLower(m)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
