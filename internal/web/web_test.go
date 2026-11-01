package web

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

var now = time.Date(2026, 10, 31, 15, 0, 0, 0, time.Local) // Saturday

// isolate points every home-relative lookup (agent discovery, global
// instruction files, the data dir) at a temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "APPDATA"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func engine(t *testing.T, cfg *config.Config, recs ...model.Record) *analytics.Engine {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	e := analytics.New(cfg, db)
	e.Now = func() time.Time { return now }
	return e
}

var seq int

func rec(agent, mdl, session, project string, at time.Time, in, out, cr int64) model.Record {
	seq++
	return model.Record{Key: agent + ":" + string(rune('a'+seq%26)) + time.Duration(seq).String(), Agent: agent, Provider: "anthropic", Model: mdl,
		SessionID: session, Project: project, Timestamp: at, Input: in, Output: out, CacheRead: cr, Accuracy: model.Actual}
}

func build(t *testing.T, e *analytics.Engine) (*Dashboard, string) {
	t.Helper()
	d, err := Build(e, Options{Scanned: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	return d, string(page)
}

// embedded extracts and decodes the JSON block from a rendered page.
func embedded(t *testing.T, page string) Dashboard {
	t.Helper()
	const open = `<script type="application/json" id="tm-data">`
	i := strings.Index(page, open)
	if i < 0 || strings.Count(page, open) != 1 {
		t.Fatal("data block missing or duplicated")
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</script>")
	var d Dashboard
	if err := json.Unmarshal([]byte(rest[:j]), &d); err != nil {
		t.Fatalf("embedded JSON does not parse: %v", err)
	}
	return d
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestBuildMatchesAnalytics(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	recs := []model.Record{
		rec("claude-code", "claude-opus-5-5", "s1", "/w/app", now.Add(-2*time.Hour), 100, 2000, 50_000),
		rec("claude-code", "claude-opus-5-5", "s1", "/w/app", now.Add(-time.Hour), 100, 1000, 90_000),
		rec("claude-code", "claude-sonnet-5-5", "s2", "/w/lib", now.AddDate(0, 0, -3), 500, 400, 10_000),
		rec("codex", "gpt-5", "c1", "/w/app", now.AddDate(0, 0, -40), 9000, 800, 0),
	}
	e := engine(t, cfg, recs...)
	d, page := build(t, e)

	// Facts re-add to exactly what the engine reports for the same records.
	want := e.Summary(recs)
	var cost float64
	var toks, reqs int64
	for _, f := range d.Facts {
		reqs += int64(f[4])
		toks += int64(f[5] + f[6] + f[7] + f[8])
		cost += f[9]
	}
	if reqs != want.Requests || toks != want.Tokens() || !near(cost, want.Cost) {
		t.Fatalf("facts: %d req %d tok %.6f, want %d %d %.6f", reqs, toks, cost, want.Requests, want.Tokens(), want.Cost)
	}
	// Today's hourly facts cover only today's two requests.
	var hourly int64
	for _, f := range d.Hourly {
		hourly += int64(f[4])
		if f[0] != 13 && f[0] != 14 {
			t.Fatalf("hourly bucket %v", f[0])
		}
	}
	if hourly != 2 {
		t.Fatalf("hourly requests = %d, want 2", hourly)
	}
	ov, _, _ := e.Overview(storage.Filter{})
	if d.Overview.Today.Requests != 2 || !near(d.Overview.Month.Cost, ov.Month.Cost) || !near(d.Overview.ProjectedMonth, ov.ProjectedMonth) {
		t.Fatalf("overview %+v", d.Overview)
	}
	if !near(d.Meta.ProjectionFactor*ov.Month.Cost, ov.ProjectedMonth) {
		t.Fatalf("projection factor %v does not reproduce ProjectMonth", d.Meta.ProjectionFactor)
	}
	if d.Meta.Today != "2026-10-31" || d.Meta.WeekStart != "2026-10-26" || d.Meta.MonthStart != "2026-10-01" || d.Meta.Last7Start != "2026-10-25" {
		t.Fatalf("meta dates %+v", d.Meta)
	}
	if len(d.Sessions) != 3 || d.Sessions[0].ID != "s1" || d.Sessions[0].Requests != 2 || d.Sessions[0].End-d.Sessions[0].Start != time.Hour.Milliseconds() {
		t.Fatalf("sessions %+v", d.Sessions)
	}
	if d.Sessions[0].ContextWindow != 1_000_000 || d.Sessions[0].LastPrompt != 90_100 {
		t.Fatalf("context fill inputs %+v", d.Sessions[0])
	}
	// The page carries exactly what Build produced.
	got := embedded(t, page)
	if len(got.Facts) != len(d.Facts) || got.Meta.Records != 4 || got.Meta.Version != "test" {
		t.Fatalf("round trip lost data: %+v", got.Meta)
	}
}

func TestSessionLimit(t *testing.T) {
	isolate(t)
	var recs []model.Record
	for i := 0; i < 5; i++ {
		recs = append(recs, rec("claude-code", "claude-opus-5-5", string(rune('a'+i)), "/w", now.Add(-time.Duration(i)*time.Hour), 1, 1, 0))
	}
	d, err := Build(engine(t, config.Default(), recs...), Options{SessionLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sessions) != 2 || d.Meta.SessionsTotal != 5 || d.Meta.SessionsShown != 2 || d.Sessions[0].ID != "a" {
		t.Fatalf("limit: %d shown, meta %+v", len(d.Sessions), d.Meta)
	}
}

func TestEmptyDatabase(t *testing.T) {
	isolate(t)
	d, page := build(t, engine(t, config.Default()))
	if d.Meta.Records != 0 || len(d.Facts) != 0 || len(d.Findings) != 0 || d.Overview.Today.Cost != 0 {
		t.Fatalf("empty db: %+v", d.Meta)
	}
	// Collections serialize as [] (never null) so the page can iterate them.
	for _, k := range []string{`"facts":[]`, `"hourly":[]`, `"sessions":[]`, `"budgets":[]`, `"limits":[]`, `"findings":[]`, `"days":[]`, `"models":[]`} {
		if !strings.Contains(page, k) {
			t.Errorf("empty export lacks %s", k)
		}
	}
	if len(d.Agents) != 6 {
		t.Fatalf("agents: %d, want every adapter listed", len(d.Agents))
	}
}

func TestMissingPricing(t *testing.T) {
	isolate(t)
	d, _ := build(t, engine(t, config.Default(),
		rec("codex", "gpt-mystery-9", "c1", "/w", now.Add(-time.Hour), 1000, 500, 0),
		rec("claude-code", "claude-opus-5-5", "s1", "/w", now.Add(-time.Hour), 1000, 500, 0)))
	var unpriced, priced *ModelInfo
	for i := range d.Models {
		switch d.Models[i].Name {
		case "gpt-mystery-9":
			unpriced = &d.Models[i]
		case "claude-opus-5-5":
			priced = &d.Models[i]
		}
	}
	if unpriced == nil || unpriced.Priced || unpriced.Input != 0 || priced == nil || !priced.Priced || priced.Input != 4 {
		t.Fatalf("model prices: %+v %+v", unpriced, priced)
	}
	for _, f := range d.Facts {
		if d.Dims.Models[int(f[3])] == "gpt-mystery-9" && (f[9] != 0 || f[10] != 1500) {
			t.Fatalf("unpriced fact got cost %v / unpriced %v", f[9], f[10])
		}
	}
	if d.Overview.Today.Unpriced != 1500 || d.Overview.Today.Cost <= 0 {
		t.Fatalf("overview unpriced %+v", d.Overview.Today)
	}
}

func TestUnsupportedAgentsHaveNoData(t *testing.T) {
	isolate(t)
	d, _ := build(t, engine(t, config.Default(), rec("claude-code", "claude-opus-5-5", "s1", "/w", now.Add(-time.Hour), 1, 1, 0)))
	var cursor *AgentInfo
	for i := range d.Agents {
		if d.Agents[i].Name == "cursor" {
			cursor = &d.Agents[i]
		}
	}
	if cursor == nil || cursor.Availability != "unavailable" || cursor.Files != 0 {
		t.Fatalf("cursor status %+v", cursor)
	}
	// Usage exists only for agents that recorded it: nothing is filled in.
	if len(d.Dims.Agents) != 1 || d.Dims.Agents[0] != "claude-code" {
		t.Fatalf("agents with usage: %v", d.Dims.Agents)
	}
}

func TestBudgets(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	cfg.Privacy.HideProjectPaths = true
	cfg.Budgets.Global = config.Budget{Daily: 0.01, Monthly: 1000}
	cfg.Budgets.PerProject = map[string]config.Budget{"/secret/work/app": {Weekly: 5}}
	cfg.Budgets.PerAgent = map[string]config.Budget{"claude-code": {Monthly: 10}}
	e := engine(t, cfg, rec("claude-code", "claude-opus-5-5", "s1", "/secret/work/app", now.Add(-time.Hour), 10_000, 10_000, 0))
	d, page := build(t, e)
	if len(d.Budgets) != 4 {
		t.Fatalf("budgets: %+v", d.Budgets)
	}
	byName := map[string]BudgetStatus{}
	for _, b := range d.Budgets {
		byName[b.Scope+"/"+b.Period] = b
	}
	daily := byName["global/daily"]
	spent := 10_000*4/1e6 + 10_000*20/1e6
	if !near(daily.Spent, spent) || daily.Fraction < 1 || daily.Remaining >= 0 || daily.Level != 1 {
		t.Fatalf("exceeded daily budget %+v", daily)
	}
	if m := byName["global/monthly"]; m.Fraction >= 0.5 || m.Projected <= m.Spent || m.Level != 0 {
		t.Fatalf("monthly budget %+v", m)
	}
	if p := byName["project/weekly"]; p.Target != "app" || p.Name != "Weekly project: app" {
		t.Fatalf("project budget target not hidden: %+v", p)
	}
	if strings.Contains(page, "/secret/work") {
		t.Fatal("hidden project path leaked into the page")
	}
}

func TestOptimizationFindings(t *testing.T) {
	isolate(t)
	proj := t.TempDir()
	big := strings.Repeat("Always run the full integration suite before committing any change.\n", 200)
	if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	var recs []model.Record
	for i := 0; i < 40; i++ {
		recs = append(recs, rec("claude-code", "claude-opus-5-5", "s1", proj, now.Add(-time.Duration(i)*time.Hour), 100, 300, 200_000))
	}
	for _, hide := range []bool{false, true} {
		cfg := config.Default()
		cfg.Privacy.HideProjectPaths = hide
		d, page := build(t, engine(t, cfg, recs...))
		var f *struct{ Project, File, Kind, Detail string }
		for _, x := range d.Findings {
			if x.Kind == "instruction-file" {
				f = &struct{ Project, File, Kind, Detail string }{x.Project, x.File, x.Kind, x.Detail}
			}
		}
		if f == nil {
			t.Fatalf("hide=%v: no instruction-file finding in %+v", hide, d.Findings)
		}
		if f.Detail == "" || !strings.Contains(f.Detail, "repeated lines") {
			t.Fatalf("finding lacks the optimizer's explanation: %+v", f)
		}
		if hide && (f.File != "CLAUDE.md" || f.Project != filepath.Base(proj) || strings.Contains(page, proj)) {
			t.Fatalf("hidden paths leaked: %+v", f)
		}
		if !hide && !strings.HasSuffix(f.File, "CLAUDE.md") {
			t.Fatalf("file %q", f.File)
		}
	}
}

func TestProjectPathPrivacy(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	isolate(t)
	under := filepath.Join(realHome, "code", "app")
	recs := []model.Record{
		rec("claude-code", "claude-opus-5-5", "s1", under, now.Add(-time.Hour), 1, 1, 0),
		rec("claude-code", "claude-opus-5-5", "s2", "", now.Add(-time.Hour), 1, 1, 0),
	}
	d, page := build(t, engine(t, config.Default(), recs...))
	if d.Dims.Projects[0] != "~"+string(filepath.Separator)+filepath.Join("code", "app") || d.Dims.Projects[1] != "(unknown)" {
		t.Fatalf("shortened projects %v", d.Dims.Projects)
	}
	if strings.Contains(page, realHome) {
		t.Fatal("absolute home path in page")
	}
	cfg := config.Default()
	cfg.Privacy.HideProjectPaths = true
	d, _ = build(t, engine(t, cfg, recs...))
	if d.Dims.Projects[0] != "app" {
		t.Fatalf("hidden project %v", d.Dims.Projects)
	}
}

func TestEscaping(t *testing.T) {
	isolate(t)
	evil := "/w/</script><script>alert(1)</script>& <!--"
	d, page := build(t, engine(t, config.Default(), rec("claude-code", "claude-opus-5-5", "</script>", evil, now.Add(-time.Hour), 1, 1, 0)))
	for _, bad := range []string{"</script><script>alert", "<!--", " "} {
		if strings.Contains(page, bad) {
			t.Fatalf("page contains raw %q", bad)
		}
	}
	if strings.Count(page, "</script>") != 2 {
		t.Fatalf("expected only the two real closing script tags, got %d", strings.Count(page, "</script>"))
	}
	got := embedded(t, page)
	if got.Dims.Projects[0] != d.Dims.Projects[0] || got.Sessions[0].ID != "</script>" {
		t.Fatalf("escaped values do not round-trip: %q", got.Dims.Projects)
	}
	// Every placeholder was filled and the assets cannot end their own tag.
	for _, slot := range []string{cssSlot, jsSlot, dataSlot} {
		if strings.Contains(page, slot) {
			t.Fatalf("placeholder %s left in page", slot)
		}
	}
	for _, name := range []string{"dashboard.css", "dashboard.js"} {
		b, _ := assets.ReadFile("assets/" + name)
		if strings.Contains(strings.ToLower(string(b)), "</script") || strings.Contains(strings.ToLower(string(b)), "</style") {
			t.Fatalf("%s contains a closing tag", name)
		}
	}
	// The script renders text with textContent, never innerHTML.
	js, _ := assets.ReadFile("assets/dashboard.js")
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(string(js), sink) {
			t.Fatalf("dashboard.js uses %s", sink)
		}
	}
}

func TestOffline(t *testing.T) {
	isolate(t)
	_, page := build(t, engine(t, config.Default(), rec("claude-code", "claude-opus-5-5", "s1", "/w", now.Add(-time.Hour), 1, 1, 0)))
	if !strings.Contains(page, `content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:`) {
		t.Fatal("missing restrictive Content-Security-Policy")
	}
	lower := strings.ToLower(page)
	for _, ext := range []string{"src=\"http", "src='http", "href=\"http", "url(http", "url(//", "@import", "<link", "fetch(", "xmlhttprequest", "websocket", "sendbeacon", "eventsource", "<iframe", "//cdn", "googleapis"} {
		if strings.Contains(lower, ext) {
			t.Fatalf("page references an external resource or network API: %q", ext)
		}
	}
	// The only URL is the SVG namespace identifier, which is never fetched.
	if n := strings.Count(page, "http") - strings.Count(page, "http-equiv"); n != strings.Count(page, "http://www.w3.org/2000/svg") {
		t.Fatalf("unexpected URLs in page (%d occurrences of http)", n)
	}
}

func TestResolveAndWrite(t *testing.T) {
	home := isolate(t)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	if p, custom, _ := ResolveOutput(""); custom || p != filepath.Join(data, "tokenmanager", "dashboard.html") {
		t.Fatalf("default path %q custom=%v", p, custom)
	}
	dir := t.TempDir()
	if p, custom, _ := ResolveOutput(dir); !custom || p != filepath.Join(dir, "dashboard.html") {
		t.Fatalf("directory output %q", p)
	}
	if p, _, _ := ResolveOutput("~/x/dash.html"); p != filepath.Join(home, "x", "dash.html") {
		t.Fatalf("tilde output %q", p)
	}
	rel, _, _ := ResolveOutput("rel.html")
	if !filepath.IsAbs(rel) {
		t.Fatalf("relative output not absolute: %q", rel)
	}

	target := filepath.Join(dir, "nested", "out.html")
	if err := Write(target, []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	if err := Write(target, []byte("two"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite without permission: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "one" {
		t.Fatalf("existing file changed: %q", b)
	}
	if err := Write(target, []byte("three"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "three" {
		t.Fatalf("overwrite: %q", b)
	}
	if info, _ := os.Stat(target); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	if err := Write(dir, []byte("x"), true); err == nil {
		t.Fatal("writing over a directory should fail")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "nested", ".tokenmanager-dashboard-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestOpen(t *testing.T) {
	var got []string
	startCommand = func(name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	t.Cleanup(func() { startCommand = func(name string, args ...string) error { return nil } })
	if err := Open("/tmp/d.html"); err != nil || len(got) == 0 || got[len(got)-1] != "/tmp/d.html" {
		t.Fatalf("open: %v %v", got, err)
	}
	startCommand = func(string, ...string) error { return errors.New("no browser") }
	if err := Open("/tmp/d.html"); err == nil || !strings.Contains(err.Error(), "could not open a browser") {
		t.Fatalf("failure not reported: %v", err)
	}
}

func TestLimitsExport(t *testing.T) {
	isolate(t)
	e := engine(t, config.Default(),
		rec("claude-code", "claude-opus-5-5", "s1", "/p", now.Add(-26*time.Hour), 10, 900, 0),
		rec("claude-code", "claude-opus-5-5", "s2", "/p", now.Add(-5*time.Minute), 10, 300, 0))
	d, page := build(t, e)
	if len(d.Limits) != 1 || d.Limits[0].Family != "anthropic" {
		t.Fatalf("limits: %+v", d.Limits)
	}
	l := d.Limits[0]
	if l.Session == nil || l.Session.Counted != 310 || l.Session.Source != "personal-max" || l.Session.SourceLabel != "personal max · estimated" {
		t.Fatalf("session: %+v", l.Session)
	}
	if l.Session.RemainingMs <= 0 || len(l.History) != 2 || !l.History[1].Active {
		t.Fatalf("history oldest first with the active block last: %+v", l.History)
	}
	if !strings.Contains(page, `"limits":[{"family":"anthropic"`) {
		t.Fatal("page does not embed limits")
	}
}
