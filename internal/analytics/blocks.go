package analytics

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// Subscription plans (Claude Pro/Max, ChatGPT Plus/Pro) are limited by usage
// windows rather than billed per token: a session block that starts with the
// first request after the previous block ended, and a weekly cap. Window
// boundaries are estimated from request timestamps unless the provider
// reports them (Codex rate_limits), and every limit carries its source.

// LimitSource says where a limit came from, in order of precedence.
type LimitSource string

const (
	SourceReported    LimitSource = "reported"     // provider-reported (Codex rate_limits)
	SourceConfigured  LimitSource = "configured"   // plans.<family>.session_tokens / weekly_tokens
	SourcePersonalMax LimitSource = "personal-max" // largest completed window recently: an estimate
	SourceNone        LimitSource = "none"
)

// Estimated reports whether a limit from this source is a guess.
func (s LimitSource) Estimated() bool { return s == SourcePersonalMax }

// Block is one session window of a provider family.
type Block struct {
	Agg
	Family     string
	Start, End time.Time
	Active     bool  // now falls inside [Start, End)
	Counted    int64 // tokens that count toward limits (plans.count)
	Reported   bool  // boundaries reported by the provider, not estimated
}

// Limit is the limit a window is measured against.
type Limit struct {
	Tokens   int64 // in counted tokens; 0 when unknown
	Source   LimitSource
	Implied  bool      // Tokens derived from a reported percentage
	Fraction float64   // share used (the provider's own figure when reported)
	Observed time.Time // when the reported snapshot was taken
}

// Burn is the current consumption rate.
type Burn struct {
	TokensPerMin float64 // counted tokens
	CostPerHour  float64 // API-equivalent cost
	Basis        string  // "last 15m" or "window average"
}

// Window is a session or weekly window with its rate, projection and limit.
type Window struct {
	Block
	Kind      string // "session" or "weekly"
	Rolling   bool   // weekly window without a fixed reset (End is now)
	Burn      Burn
	Projected int64 // counted tokens at End at the current burn rate (0 when rolling)
	Limit     Limit
	ETA       time.Duration // time until the limit at the current burn rate
	ETAKnown  bool          // false when no limit is known or nothing is burning
	Reached   bool
}

// Remaining is the time until the window resets (0 when rolling).
func (w *Window) Remaining(now time.Time) time.Duration {
	if w.Rolling {
		return 0
	}
	return max(0, w.End.Sub(now))
}

// HitsBeforeReset reports whether the limit is projected within this window.
func (w *Window) HitsBeforeReset(now time.Time) bool {
	return w.Reached || (w.ETAKnown && (w.Rolling || now.Add(w.ETA).Before(w.End)))
}

// PlanReport is the subscription view of one provider family.
type PlanReport struct {
	Family   string
	Plan     string
	Agents   []string
	Session  *Window // nil when no block is active
	Weekly   Window
	History  []Block // most recent first, active block included
	Snapshot *model.RateLimits
}

// BlockOptions are the window settings for one family.
type BlockOptions struct {
	Length time.Duration
	Floor  bool     // floor block starts to the hour
	Count  []string // token fields counted toward limits
}

// countTokens sums the fields that count toward limits.
func countTokens(r model.Record, fields []string) int64 {
	var n int64
	for _, f := range fields {
		switch f {
		case "input":
			n += r.Input
		case "output":
			n += r.Output
		case "cache_read":
			n += r.CacheRead
		case "cache_write":
			n += r.CacheWrite()
		}
	}
	return n
}

// GroupBlocks splits time-ordered records into session blocks. A block
// starts at the first request at or after the previous block's end (floored
// to the hour when opt.Floor is set) and lasts opt.Length.
func (e *Engine) GroupBlocks(recs []model.Record, family string, opt BlockOptions, now time.Time) []Block {
	var out []Block
	for _, r := range recs {
		if len(out) == 0 || !r.Timestamp.Before(out[len(out)-1].End) {
			start := r.Timestamp
			if opt.Floor {
				start = start.Truncate(time.Hour)
			}
			out = append(out, Block{Family: family, Start: start, End: start.Add(opt.Length)})
		}
		b := &out[len(out)-1]
		e.Add(&b.Agg, r)
		b.Counted += countTokens(r, opt.Count)
	}
	for i := range out {
		out[i].Active = !now.Before(out[i].Start) && now.Before(out[i].End)
	}
	return out
}

// windowBlock aggregates the records inside [from, to).
func (e *Engine) windowBlock(recs []model.Record, family string, from, to time.Time, count []string) Block {
	b := Block{Family: family, Start: from, End: to}
	for _, r := range recs {
		if !r.Timestamp.Before(from) && r.Timestamp.Before(to) {
			e.Add(&b.Agg, r)
			b.Counted += countTokens(r, count)
		}
	}
	return b
}

// PlanReport computes the session block, weekly window and block history of
// one family from its records (time-ordered, already narrowed to the family).
func (e *Engine) PlanReport(family string, recs []model.Record, snap *model.RateLimits, now time.Time) PlanReport {
	plans := e.Cfg.Plans
	pl := plans.For(family)
	opt := BlockOptions{Length: time.Duration(pl.SessionHours * float64(time.Hour)), Floor: *pl.FloorToHour, Count: plans.CountFields()}
	rep := PlanReport{Family: family, Plan: pl.Plan, Agents: pl.Agents, Snapshot: snap}
	if rep.Plan == "" && snap != nil {
		rep.Plan = snap.Plan
	}
	var primary, secondary *model.RateLimitWindow
	if snap != nil {
		if snap.Primary.Current(now) {
			primary = snap.Primary
		}
		if snap.Secondary.Current(now) {
			secondary = snap.Secondary
		}
	}

	blocks := e.GroupBlocks(recs, family, opt, now)
	// A reported window replaces the estimated active block.
	if primary != nil {
		rb := e.windowBlock(recs, family, primary.Start(), primary.ResetsAt, opt.Count)
		rb.Active, rb.Reported = true, true
		blocks = slices.DeleteFunc(blocks, func(b Block) bool { return b.Active || b.End.After(rb.Start) })
		blocks = append(blocks, rb)
	}
	var personalMax int64
	for _, b := range blocks {
		if !b.Active && !b.Start.Before(now.AddDate(0, 0, -30)) {
			personalMax = max(personalMax, b.Counted)
		}
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		rep.History = append(rep.History, blocks[i])
	}
	if n := len(blocks); n > 0 && blocks[n-1].Active {
		w := &Window{Block: blocks[n-1], Kind: "session"}
		w.Burn = e.sessionBurn(recs, w.Block, plans.BurnWindow(), opt.Count, now)
		w.Limit = pickLimit(primary, snap, w.Counted, pl.SessionTokens, personalMax)
		w.finish(now)
		rep.Session = w
	}

	// Weekly window: reported, then a fixed reset from config, then rolling.
	wk := Window{Kind: "weekly"}
	var from, to time.Time
	switch {
	case secondary != nil:
		from, to = secondary.Start(), secondary.ResetsAt
		wk.Reported = true
	default:
		if reset, ok := lastWeeklyReset(pl.WeeklyReset, now); ok {
			from, to = reset, reset.AddDate(0, 0, 7)
		} else {
			// Rolling: the window ends now, inclusive of a request stamped now.
			from, to, wk.Rolling = now.AddDate(0, 0, -7), now.Add(time.Nanosecond), true
		}
	}
	reported := wk.Reported
	wk.Block = e.windowBlock(recs, family, from, to, opt.Count)
	wk.Reported, wk.Active = reported, true
	var weeklyMax int64
	for k := 1; k <= 4; k++ {
		pb := e.windowBlock(recs, family, from.AddDate(0, 0, -7*k), from.AddDate(0, 0, -7*(k-1)), opt.Count)
		weeklyMax = max(weeklyMax, pb.Counted)
	}
	elapsed := max(now.Sub(wk.Start), time.Minute)
	wk.Burn = Burn{float64(wk.Counted) / elapsed.Minutes(), wk.Cost / elapsed.Hours(), "window average"}
	wk.Limit = pickLimit(secondary, snap, wk.Counted, pl.WeeklyTokens, weeklyMax)
	wk.finish(now)
	if wk.Rolling {
		wk.End = now
	}
	rep.Weekly = wk
	return rep
}

// sessionBurn is the counted-token rate over the last burn window of the
// block, or the block average when that window has fewer than two requests.
func (e *Engine) sessionBurn(recs []model.Record, b Block, window time.Duration, count []string, now time.Time) Burn {
	from := now.Add(-window)
	if from.Before(b.Start) {
		from = b.Start
	}
	var tokens int64
	var cost float64
	n := 0
	for _, r := range recs {
		if r.Timestamp.Before(from) || r.Timestamp.After(now) || !r.Timestamp.Before(b.End) {
			continue
		}
		n++
		tokens += countTokens(r, count)
		if c, ok := e.Prices.Cost(r); ok {
			cost += c
		}
	}
	if span := now.Sub(from); n >= 2 && span >= time.Minute {
		return Burn{float64(tokens) / span.Minutes(), cost / span.Hours(), "last " + shortDur(window)}
	}
	elapsed := max(now.Sub(b.Start), time.Minute)
	return Burn{float64(b.Counted) / elapsed.Minutes(), b.Cost / elapsed.Hours(), "window average"}
}

// pickLimit applies the precedence reported > configured > personal max > none.
func pickLimit(reported *model.RateLimitWindow, snap *model.RateLimits, counted, configured, personalMax int64) Limit {
	frac := func(limit int64) float64 {
		if limit <= 0 {
			return 0
		}
		return float64(counted) / float64(limit)
	}
	switch {
	case reported != nil:
		l := Limit{Source: SourceReported, Fraction: reported.UsedPercent / 100, Observed: snap.Observed}
		// The percentage covers the whole account; the token figure is only
		// what this machine's logs imply.
		if l.Fraction >= 0.01 && counted > 0 {
			l.Tokens, l.Implied = int64(float64(counted)/l.Fraction), true
		}
		return l
	case configured > 0:
		return Limit{Tokens: configured, Source: SourceConfigured, Fraction: frac(configured)}
	case personalMax > 0:
		return Limit{Tokens: personalMax, Source: SourcePersonalMax, Fraction: frac(personalMax)}
	}
	return Limit{Source: SourceNone}
}

// finish computes the projection and ETA from the burn rate and limit.
func (w *Window) finish(now time.Time) {
	if !w.Rolling {
		w.Projected = w.Counted + int64(w.Burn.TokensPerMin*w.Remaining(now).Minutes())
	}
	if w.Limit.Source == SourceNone {
		return
	}
	if w.Limit.Fraction >= 1 {
		w.Reached, w.ETAKnown = true, true
		return
	}
	if w.Limit.Tokens <= 0 || w.Burn.TokensPerMin <= 0 {
		return
	}
	left := float64(w.Limit.Tokens - w.Counted)
	w.ETA = time.Duration(left / w.Burn.TokensPerMin * float64(time.Minute))
	w.ETAKnown = true
}

// lastWeeklyReset parses "mon 09:00" and returns the latest such local time
// at or before now.
func lastWeeklyReset(spec string, now time.Time) (time.Time, bool) {
	wd, hh, mm, ok := ParseWeeklyReset(spec)
	if !ok {
		return time.Time{}, false
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	t = t.AddDate(0, 0, -((int(t.Weekday()) - int(wd) + 7) % 7))
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t, true
}

// ParseWeeklyReset parses a weekly reset such as "mon 09:00" or "Sunday 18:30".
func ParseWeeklyReset(spec string) (time.Weekday, int, int, bool) {
	f := strings.Fields(strings.ToLower(spec))
	if len(f) != 2 || len(f[0]) < 3 {
		return 0, 0, 0, false
	}
	wd := -1
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.HasPrefix(strings.ToLower(d.String()), f[0][:3]) {
			wd = int(d)
		}
	}
	var hh, mm int
	if _, err := fmt.Sscanf(f[1], "%d:%d", &hh, &mm); err != nil || wd < 0 || hh > 23 || mm > 59 || hh < 0 || mm < 0 {
		return 0, 0, 0, false
	}
	return time.Weekday(wd), hh, mm, true
}

func shortDur(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// limitsLookback covers 30 days of blocks plus four previous weekly windows.
const limitsLookback = 36 * 24 * time.Hour

// Limits builds a PlanReport for every family with recent usage or a
// provider snapshot. Records are narrowed to the plan's agents unless the
// filter names an agent explicitly.
func (e *Engine) Limits(f storage.Filter) ([]PlanReport, error) {
	now := e.Now()
	f.From, f.To = now.Add(-limitsLookback), time.Time{}
	recs, err := e.DB.Query(f)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Timestamp.Before(recs[j].Timestamp) })
	var out []PlanReport
	for _, fam := range config.PlanFamilies {
		pl := e.Cfg.Plans.For(fam)
		var fr []model.Record
		for _, r := range recs {
			if r.Provider == fam && (f.Agent != "" || slices.Contains(pl.Agents, r.Agent)) {
				fr = append(fr, r)
			}
		}
		var snap *model.RateLimits
		if rl, ok := e.DB.RateLimits(fam); ok && (f.Agent == "" || f.Agent == rl.Agent) {
			snap = &rl
		}
		current := snap != nil && (snap.Primary.Current(now) || snap.Secondary.Current(now))
		if len(fr) == 0 && !current {
			continue
		}
		out = append(out, e.PlanReport(fam, fr, snap, now))
	}
	return out, nil
}

// Label names the limit's source for display, e.g. "personal max · estimated".
func (l Limit) Label() string {
	switch l.Source {
	case SourceReported:
		if l.Implied {
			return "reported % · tokens implied"
		}
		return "reported"
	case SourceConfigured:
		return "configured"
	case SourcePersonalMax:
		return "personal max · estimated"
	}
	return "no limit known"
}

// Outlook is a one-line summary of when the limit is reached.
func (w *Window) Outlook(now time.Time) string {
	switch {
	case w.Limit.Source == SourceNone:
		return "no limit known"
	case w.Reached:
		return "limit reached"
	case !w.ETAKnown:
		return "not burning: limit not in sight"
	case w.HitsBeforeReset(now):
		return "limit in " + fmtDur(w.ETA) + " at current pace"
	}
	return "not reached before reset at current pace"
}

func fmtDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}
