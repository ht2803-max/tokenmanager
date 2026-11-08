package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/format"
)

// toolJSON is the machine-readable form of a ToolStat. Only call counts are
// actual; every token and cost figure is an estimate.
type toolJSON struct {
	Key             string  `json:"key"`
	Tool            string  `json:"tool,omitempty"`
	MCPServer       string  `json:"mcp_server,omitempty"`
	Calls           int64   `json:"calls"`
	Results         int64   `json:"results"`
	Sessions        int     `json:"sessions"`
	AvgResult       int64   `json:"avg_result_tokens"`
	P95Result       int64   `json:"p95_result_tokens"`
	ResultTokens    int64   `json:"result_tokens"`
	FirstTimeTokens int64   `json:"first_time_input_tokens"`
	CarriedTokens   int64   `json:"carried_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ShareOfInput    float64 `json:"share_of_input"`
	EstimatedCost   float64 `json:"estimated_cost"`
}

func toolsCmd() *cobra.Command {
	var by string
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Break down token usage by tool (Read, Bash, MCP servers, ...)",
		Long: `Show tool calls and their estimated token footprint.

Calls are counted from the agent logs (actual). Providers do not split usage
by content block, so the rest is estimated: result tokens are result bytes / 4,
charged to the next request in the session; carried tokens are that result
re-read from cache by later requests until the session ends or is compacted;
output tokens are each request's output split across its calls by input size.
Only tool names and byte counts are stored, never inputs or results.`,
		Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			var d analytics.Dimension
			switch by {
			case "tool", "":
				d = analytics.ByTool
			case "server", "mcp":
				d = analytics.ByMCPServer
			default:
				return fmt.Errorf("unknown --by %q (want tool or server)", by)
			}
			p := e.period("month")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			rep, err := e.eng.Tools(p, e.filter(), recs)
			if err != nil {
				return err
			}
			gs := rep.Group(d)
			if fl.json {
				out := make([]toolJSON, 0, len(gs))
				for _, g := range gs {
					out = append(out, toolJSON{g.Key, g.Tool, g.Server, g.Calls, g.Results, g.Sessions(), g.AvgResult, g.P95Result,
						g.ResultTokens, g.FirstTokens, g.CarriedTokens, g.OutputTokens, round4(g.Share), round2(g.Cost)})
				}
				return writeJSON(e.out, map[string]any{"period": p.Name, "from": nz(p.From), "to": nz(p.To), "by": string(d),
					"currency": format.Currency, "prompt_tokens": rep.Prompt,
					"accuracy": map[string]string{"calls": "actual", "tokens": "estimated", "cost": "estimated"},
					"tools":    out})
			}
			label := "Tool"
			if d == analytics.ByMCPServer {
				label = "MCP server"
			}
			t := newTable(label, "Calls", "Avg result", "P95 result", "Est. result tok", "Est. carried tok", "Share of input", "Est. cost")
			t.alignRight(1, 2, 3, 4, 5, 6, 7)
			for _, g := range gs {
				name := g.Key
				if !fl.csv {
					name = format.Truncate(name, 40)
				}
				t.add(name, format.Thousands(g.Calls), format.Tokens(g.AvgResult), format.Tokens(g.P95Result),
					format.Tokens(g.FirstTokens), format.Tokens(g.CarriedTokens), format.Percent(g.Share), format.Money(g.Cost))
			}
			if fl.csv {
				return t.csv(e.out)
			}
			heading(e.out, "tools", p.Label())
			if len(gs) == 0 {
				msg := "No tool calls recorded for this period."
				if d == analytics.ByMCPServer {
					msg = "No MCP tool calls recorded for this period."
				}
				fmt.Fprintln(e.out, sDim.Render("  "+msg))
				return nil
			}
			t.render(e.out)
			var first, carried int64
			for _, g := range rep.Group(analytics.ByTool) {
				first += g.FirstTokens
				carried += g.CarriedTokens
			}
			fmt.Fprintf(e.out, "\n  %s calls · tool results ~%s first-time + ~%s carried of %s prompt tokens\n",
				format.Thousands(rep.Calls), format.Tokens(first), format.Tokens(carried), format.Tokens(rep.Prompt))
			for _, l := range []string{
				"  Calls are actual; tokens and costs are estimates (result bytes / 4, charged to the next request;",
				"  carried = re-read by later requests until the session ends or is compacted). Avg/P95 are tokens per result.",
			} {
				fmt.Fprintln(e.out, sDim.Render(l))
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&by, "by", "tool", "group by tool or server (MCP servers only)")
	return cmd
}

func round4(f float64) float64 { return float64(int64(f*1e4+0.5)) / 1e4 }
