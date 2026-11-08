package analytics

import (
	"sort"

	"github.com/ht2803-max/tokenmanager/internal/model"
	"github.com/ht2803-max/tokenmanager/internal/storage"
)

// Tool dimensions for GroupTools.
const (
	ByTool      Dimension = "tool"
	ByMCPServer Dimension = "mcp-server"
)

// CompactionDrop is how far a prompt must shrink, relative to the previous
// request in the same session file, to count as a compaction or summary:
// carried tool results stop being counted there.
const CompactionDrop = 0.5

// compactionFloor ignores drops in small prompts, which are noise.
const compactionFloor = 20_000

// ToolUse is one tool call with its estimated token attribution.
//
// Provider usage is not split by content block, so everything except the
// call itself is an estimate:
//   - OutputTokens: the emitting request's output split across its calls,
//     weighted by input size (evenly when sizes are unknown).
//   - ResultTokens (from the parser, ~4 bytes per token): charged to the next
//     request in the same session file, where the result enters the prompt.
//   - CarriedTokens: the result re-read from cache by every later request
//     until the session file ends or the prompt shrinks by CompactionDrop.
type ToolUse struct {
	model.ToolCall
	Model   string // model of the emitting request ("" if unknown)
	Project string

	OutputTokens  int64
	Charged       bool  // a later request was found to carry the result
	CarriedReqs   int64 // later requests that re-read the result
	CarriedTokens int64

	OutputCost, ResultCost, CarriedCost float64
}

// Label is the display name: "server/tool" for MCP tools.
func (u *ToolUse) Label() string { return model.ToolLabel(u.Tool, u.MCPServer) }

// Cost is the estimated total: output share + first-time input + carried.
func (u *ToolUse) Cost() float64 { return u.OutputCost + u.ResultCost + u.CarriedCost }

// FirstTokens is the result's first-time input tokens (0 if never charged).
func (u *ToolUse) FirstTokens() int64 {
	if u.Charged {
		return u.ResultTokens
	}
	return 0
}

// ToolCalls loads tool calls for a period and filter.
func (e *Engine) ToolCalls(p Period, f storage.Filter) ([]model.ToolCall, error) {
	f.From, f.To = p.From, p.To
	return e.DB.QueryTools(f)
}

// rates are per-token prices in the configured currency.
type rates struct{ in, out, read, write5m, write1h float64 }

func (e *Engine) ratesFor(m, speed string, cache map[string]*rates) *rates {
	k := m + "\x00" + speed
	if r, ok := cache[k]; ok {
		return r
	}
	unit := func(r model.Record) float64 {
		r.Model, r.Speed = m, speed
		c, ok := e.Prices.Cost(r)
		if !ok {
			return 0
		}
		return c
	}
	var r *rates
	if _, ok := e.Prices.Lookup(m); ok {
		r = &rates{unit(model.Record{Input: 1}), unit(model.Record{Output: 1}), unit(model.Record{CacheRead: 1}),
			unit(model.Record{CacheWrite5m: 1}), unit(model.Record{CacheWrite1h: 1})}
	}
	cache[k] = r
	return r
}

// inputRate is what new prompt tokens cost on r: cache writes when the
// request wrote to the cache, plain input otherwise.
func (rt *rates) inputRate(r model.Record) float64 {
	switch {
	case r.CacheWrite1h > r.CacheWrite5m:
		return rt.write1h
	case r.CacheWrite5m > 0:
		return rt.write5m
	}
	return rt.in
}

// carryRate is what re-sent prompt tokens cost on r: cache reads when the
// request hit the cache, plain input otherwise.
func (rt *rates) carryRate(r model.Record) float64 {
	if r.CacheRead > 0 {
		return rt.read
	}
	return rt.in
}

// AttributeTools estimates tokens and cost for each call. recs should cover
// the same period as calls; a request outside recs is treated as unknown.
func (e *Engine) AttributeTools(recs []model.Record, calls []model.ToolCall) []ToolUse {
	type pos struct {
		stream []int // indices into recs, time ordered
		i      int
	}
	streams := map[string][]int{}
	for i, r := range recs {
		k := r.Agent + "\x00" + r.SessionID + "\x00" + r.SourceFile
		streams[k] = append(streams[k], i)
	}
	where := make(map[string]pos, len(recs))
	segEnd := make([]int, len(recs))        // last stream index of the record's segment
	carryCost := make([]float64, len(recs)) // prefix sums of carry rates along a stream
	cache := map[string]*rates{}
	for _, st := range streams {
		sort.SliceStable(st, func(a, b int) bool { return recs[st[a]].Timestamp.Before(recs[st[b]].Timestamp) })
		end := len(st) - 1
		for j := len(st) - 1; j >= 0; j-- {
			segEnd[st[j]] = end
			if j > 0 {
				prev, cur := recs[st[j-1]].PromptTokens(), recs[st[j]].PromptTokens()
				if prev >= compactionFloor && float64(cur) < CompactionDrop*float64(prev) {
					end = j - 1
				}
			}
		}
		var sum float64
		for j, ri := range st {
			r := recs[ri]
			if rt := e.ratesFor(r.Model, r.Speed, cache); rt != nil {
				sum += rt.carryRate(r)
			}
			carryCost[ri] = sum
			where[r.Key] = pos{st, j}
		}
	}

	// Output split: weights per emitting request.
	weight := map[string]int64{}
	count := map[string]int64{}
	for _, c := range calls {
		if c.RecordKey != "" {
			weight[c.RecordKey] += c.InputBytes
			count[c.RecordKey]++
		}
	}

	out := make([]ToolUse, 0, len(calls))
	for _, c := range calls {
		u := ToolUse{ToolCall: c}
		p, ok := where[c.RecordKey]
		if ok {
			r := recs[p.stream[p.i]]
			u.Model, u.Project = r.Model, r.Project
			rt := e.ratesFor(r.Model, r.Speed, cache)
			share := 1 / float64(count[c.RecordKey])
			if w := weight[c.RecordKey]; w > 0 {
				share = float64(c.InputBytes) / float64(w)
			}
			u.OutputTokens = int64(float64(r.Output)*share + 0.5)
			if rt != nil {
				u.OutputCost = float64(u.OutputTokens) * rt.out
			}
			if c.HasResult && p.i+1 < len(p.stream) {
				ni := p.stream[p.i+1]
				next := recs[ni]
				u.Charged = true
				if nrt := e.ratesFor(next.Model, next.Speed, cache); nrt != nil {
					u.ResultCost = float64(c.ResultTokens) * nrt.inputRate(next)
				}
				last := p.stream[segEnd[ni]]
				if segEnd[ni] > p.i+1 {
					u.CarriedReqs = int64(segEnd[ni] - (p.i + 1))
					u.CarriedTokens = c.ResultTokens * u.CarriedReqs
					u.CarriedCost = float64(c.ResultTokens) * (carryCost[last] - carryCost[ni])
				}
			}
		}
		out = append(out, u)
	}
	return out
}

// ToolStat aggregates tool uses. Calls is actual; every token and cost
// figure is an estimate (see ToolUse).
type ToolStat struct {
	Key     string // tool label, or MCP server for ByMCPServer
	Tool    string // tool name (empty for server groups)
	Server  string
	Calls   int64
	Results int64

	InputBytes, ResultBytes         int64
	OutputTokens, ResultTokens      int64
	FirstTokens, CarriedTokens      int64
	OutputCost, InputCost, Cost     float64
	AvgResult, P95Result, MaxResult int64 // result tokens per call with a result

	Share    float64 // (first-time + carried) tokens over all prompt tokens
	sessions map[string]bool
	sizes    []int64
}

// Sessions is the distinct session count.
func (s *ToolStat) Sessions() int { return len(s.sessions) }

// InputTokens is first-time plus carried tokens: the tool's footprint in prompts.
func (s *ToolStat) InputTokens() int64 { return s.FirstTokens + s.CarriedTokens }

// GroupTools aggregates uses by ByTool or ByMCPServer (non-MCP calls are
// skipped for the latter). promptTotal is the prompt tokens of the records
// the uses were attributed against, for Share. Sorted by cost then tokens.
func GroupTools(uses []ToolUse, d Dimension, promptTotal int64) []*ToolStat {
	m := map[string]*ToolStat{}
	for i := range uses {
		u := &uses[i]
		k := u.Label()
		if d == ByMCPServer {
			if u.MCPServer == "" {
				continue
			}
			k = u.MCPServer
		}
		s := m[k]
		if s == nil {
			s = &ToolStat{Key: k, Server: u.MCPServer, sessions: map[string]bool{}}
			if d != ByMCPServer {
				s.Tool = u.Tool
			}
			m[k] = s
		}
		s.Calls++
		s.InputBytes += u.InputBytes
		s.OutputTokens += u.OutputTokens
		s.OutputCost += u.OutputCost
		s.InputCost += u.ResultCost + u.CarriedCost
		s.Cost += u.Cost()
		s.FirstTokens += u.FirstTokens()
		s.CarriedTokens += u.CarriedTokens
		s.sessions[u.Agent+"/"+u.SessionID] = true
		if u.HasResult {
			s.Results++
			s.ResultBytes += u.ResultBytes
			s.ResultTokens += u.ResultTokens
			s.sizes = append(s.sizes, u.ResultTokens)
		}
	}
	out := make([]*ToolStat, 0, len(m))
	for _, s := range m {
		if n := len(s.sizes); n > 0 {
			sort.Slice(s.sizes, func(i, j int) bool { return s.sizes[i] < s.sizes[j] })
			s.AvgResult = s.ResultTokens / int64(n)
			s.P95Result = Percentile(s.sizes, 0.95)
			s.MaxResult = s.sizes[n-1]
		}
		if promptTotal > 0 {
			s.Share = float64(s.InputTokens()) / float64(promptTotal)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		if out[i].InputTokens() != out[j].InputTokens() {
			return out[i].InputTokens() > out[j].InputTokens()
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Percentile returns the nearest-rank percentile of sorted values.
func Percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p+0.999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

// ToolReport is everything the tools surfaces show for one record set.
type ToolReport struct {
	Uses   []ToolUse
	Prompt int64 // prompt tokens of the records attributed against
	Calls  int64
}

// Tools loads and attributes tool calls for recs' period and filter.
func (e *Engine) Tools(p Period, f storage.Filter, recs []model.Record) (ToolReport, error) {
	calls, err := e.ToolCalls(p, f)
	if err != nil {
		return ToolReport{}, err
	}
	rep := ToolReport{Uses: e.AttributeTools(recs, calls), Calls: int64(len(calls))}
	for _, r := range recs {
		rep.Prompt += r.PromptTokens()
	}
	return rep, nil
}

// Group is GroupTools over the report.
func (t ToolReport) Group(d Dimension) []*ToolStat { return GroupTools(t.Uses, d, t.Prompt) }
