package optimizer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

// Tool findings. Providers do not split usage by content block, so every
// figure here comes from analytics.AttributeTools and is an estimate; each
// finding says so. Savings are priced with the actual model and cache rates
// of the requests that carried the results.
const (
	// largeShare and largeP95 gate "Large tool results".
	largeShare = 0.15
	largeP95   = 4_000
	// trimTo is the result size assumed achievable with narrower calls.
	trimTo = 2_000
	// rereadMin large results from one tool in one session suggest re-reading.
	rereadMin  = 10
	rereadSize = 2_000
	// floodSize is a shell output large enough to count as a flood.
	floodSize  = 5_000
	floodMin   = 3
	floodTrim  = 1_000
	mcpShare   = 0.10
	mcpRare    = 2   // calls in the window at or below which a server is "rarely used"
	mcpActive  = 200 // total calls needed before calling any server rarely used
	carryShare = 0.25
	carryReqs  = 20
)

// shellTools are the shell tools of each agent.
var shellTools = map[string]bool{"Bash": true, "BashOutput": true, "exec": true, "shell": true, "local_shell": true, "run_shell_command": true}

// trimmedCost is the estimated cost of the part of each result above cap,
// for uses passing keep.
func trimmedCost(uses []analytics.ToolUse, cap int64, keep func(*analytics.ToolUse) bool) (cost float64, n int) {
	for i := range uses {
		u := &uses[i]
		if !keep(u) || u.ResultTokens <= cap {
			continue
		}
		n++
		cost += (u.ResultCost + u.CarriedCost) * float64(u.ResultTokens-cap) / float64(u.ResultTokens)
	}
	return cost, n
}

// largeToolResults flags built-in tools whose results take a large share of
// input and whose typical large result could be much smaller.
func (a *analyzer) largeToolResults() {
	for _, g := range a.tools.Group(analytics.ByTool) {
		if g.Server != "" || shellTools[g.Tool] || g.Share < largeShare || g.P95Result < largeP95 || g.Results < 5 {
			continue
		}
		key := g.Key
		cost, _ := trimmedCost(a.tools.Uses, trimTo, func(u *analytics.ToolUse) bool { return u.Label() == key })
		gain := cost * a.monthly
		rec := fmt.Sprintf("Make %s calls narrower so results stay small: ask for the part you need instead of everything.", g.Key)
		switch g.Tool {
		case "Read", "read_file", "view":
			rec = "Read files with offset/limit (a range of lines) instead of whole files, and use Grep or Glob first to find the part you need."
		case "WebFetch", "web_fetch":
			rec = "Fetch only the page you need and ask for a summary in the prompt; avoid fetching large pages repeatedly."
		}
		a.add(Finding{
			Severity: max(severityFor(gain), Low), Kind: "tool-large-results",
			Title:          "Large tool results (estimate)",
			Detail:         fmt.Sprintf("Estimated: %s results are ~%.0f%% of your input tokens (first-time + carried); p95 %s result is ~%s tokens over %d calls", g.Key, g.Share*100, g.Key, format.Tokens(g.P95Result), g.Calls),
			Recommendation: rec,
			MonthlySavings: gain,
		})
	}
}

// rereadResults flags tools that return many large results within single
// sessions: usually the same content read again instead of remembered.
// Only counts and sizes are known, never what was read.
func (a *analyzer) rereadResults() {
	type key struct{ session, tool string }
	large := map[key][]int{}
	for i := range a.tools.Uses {
		u := &a.tools.Uses[i]
		if u.MCPServer != "" || shellTools[u.Tool] || u.ResultTokens < rereadSize {
			continue
		}
		k := key{u.Agent + "/" + u.SessionID, u.Label()}
		large[k] = append(large[k], i)
	}
	type agg struct {
		sessions, results int
		tokens            int64
		cost              float64
	}
	byTool := map[string]*agg{}
	for k, idx := range large {
		if len(idx) < rereadMin {
			continue
		}
		g := byTool[k.tool]
		if g == nil {
			g = &agg{}
			byTool[k.tool] = g
		}
		g.sessions++
		g.results += len(idx)
		// Half of the large results beyond the first few are assumed avoidable.
		sort.Slice(idx, func(i, j int) bool { return a.tools.Uses[idx[i]].Timestamp.Before(a.tools.Uses[idx[j]].Timestamp) })
		for n, i := range idx {
			u := &a.tools.Uses[i]
			g.tokens += u.ResultTokens
			if n >= rereadMin/2 {
				g.cost += 0.5 * (u.ResultCost + u.CarriedCost)
			}
		}
	}
	for tool, g := range byTool {
		gain := g.cost * a.monthly
		a.add(Finding{
			Severity: max(severityFor(gain), Low), Kind: "tool-reread",
			Title:          "Large tool results repeated within sessions (estimate)",
			Detail:         fmt.Sprintf("Estimated: %s returned %d results over %s tokens (avg ~%s) in %s, at least %d per session", tool, g.results, format.Tokens(rereadSize), format.Tokens(g.tokens/int64(g.results)), plural(g.sessions, "session"), rereadMin),
			Recommendation: "Many large results from one tool in a session usually mean the same content is read again. Ask the agent to keep notes in a file or refer back to what it already read, and read targeted ranges.",
			MonthlySavings: gain,
		})
	}
}

// shellFloods flags shell commands whose output floods the context.
func (a *analyzer) shellFloods() {
	cost, n := trimmedCost(a.tools.Uses, floodTrim, func(u *analytics.ToolUse) bool {
		return u.MCPServer == "" && shellTools[u.Tool] && u.ResultTokens >= floodSize
	})
	if n < floodMin {
		return
	}
	var biggest int64
	var tok int64
	for i := range a.tools.Uses {
		u := &a.tools.Uses[i]
		if u.MCPServer == "" && shellTools[u.Tool] && u.ResultTokens >= floodSize {
			biggest = max(biggest, u.ResultTokens)
			tok += u.ResultTokens
		}
	}
	gain := cost * a.monthly
	a.add(Finding{
		Severity: max(severityFor(gain), Low), Kind: "tool-shell-flood",
		Title:          "Shell output floods the context (estimate)",
		Detail:         fmt.Sprintf("Estimated: %d shell commands returned more than %s tokens of output (~%s in total, largest ~%s)", n, format.Tokens(floodSize), format.Tokens(tok), format.Tokens(biggest)),
		Recommendation: "Pipe long output through head, tail or grep, use quieter flags (-q, --silent, --quiet), and redirect build or test logs to a file the agent can search.",
		MonthlySavings: gain,
	})
}

// mcpOverhead flags MCP servers whose results dominate the context, and
// servers that are rarely called (their tool definitions are still sent
// with every request, which local logs cannot size).
func (a *analyzer) mcpOverhead() {
	servers := a.tools.Group(analytics.ByMCPServer)
	var rare []string
	for _, g := range servers {
		if g.Share >= mcpShare {
			srv := g.Key
			cost, _ := trimmedCost(a.tools.Uses, trimTo, func(u *analytics.ToolUse) bool { return u.MCPServer == srv })
			gain := cost * a.monthly
			a.add(Finding{
				Severity: max(severityFor(gain), Low), Kind: "tool-mcp-overhead",
				Title:          "MCP server results dominate context (estimate)",
				Detail:         fmt.Sprintf("Estimated: results from MCP server %s are ~%.0f%% of your input tokens over %d calls (p95 ~%s tokens)", g.Key, g.Share*100, g.Calls, format.Tokens(g.P95Result)),
				Recommendation: "Ask the server's tools for less (filters, limits, summaries), or disable tools from this server that return large payloads.",
				MonthlySavings: gain,
			})
		}
		if g.Calls <= mcpRare {
			rare = append(rare, fmt.Sprintf("%s (%d)", g.Key, g.Calls))
		}
	}
	if len(rare) > 0 && a.tools.Calls >= mcpActive {
		sort.Strings(rare)
		a.add(Finding{
			Severity: Info, Kind: "tool-mcp-rare",
			Title:          "Rarely used MCP servers",
			Detail:         fmt.Sprintf("Called at most %d times in %s tool calls: %s", mcpRare, format.Thousands(a.tools.Calls), strings.Join(rare, ", ")),
			Recommendation: "Every enabled MCP server adds its tool definitions to each request. Disable servers you rarely use in projects that do not need them. (Definition sizes are not in local logs, so no savings estimate.)",
		})
	}
}

// carriedContext flags tool results that stay in context for many requests.
func (a *analyzer) carriedContext() {
	if a.tools.Prompt == 0 {
		return
	}
	var carried, charged, reqs int64
	var cost float64
	for i := range a.tools.Uses {
		u := &a.tools.Uses[i]
		if !u.Charged {
			continue
		}
		charged++
		reqs += u.CarriedReqs
		carried += u.CarriedTokens
		cost += u.CarriedCost
	}
	share := float64(carried) / float64(a.tools.Prompt)
	if charged == 0 || share < carryShare || reqs/charged < carryReqs {
		return
	}
	// Compacting or starting fresh after a subtask is assumed to drop about
	// a third of the carried results.
	gain := cost / 3 * a.monthly
	a.add(Finding{
		Severity: max(severityFor(gain), Low), Kind: "tool-carried-context",
		Title:          "Old tool results carried in context (estimate)",
		Detail:         fmt.Sprintf("Estimated: tool results re-read by later requests are ~%.0f%% of prompt tokens (~%s); each result stayed in context for ~%d more requests on average", share*100, format.Tokens(carried), reqs/charged),
		Recommendation: "Run /compact (or your agent's equivalent) after finishing a subtask, and start a fresh session for unrelated work so old file reads and command output stop being resent.",
		MonthlySavings: gain,
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
