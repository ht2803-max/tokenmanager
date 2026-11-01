package cli

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tokenmanager/tokenmanager/internal/agents"
	"github.com/tokenmanager/tokenmanager/internal/analytics"
	"github.com/tokenmanager/tokenmanager/internal/budget"
	"github.com/tokenmanager/tokenmanager/internal/config"
	"github.com/tokenmanager/tokenmanager/internal/daemon"
	"github.com/tokenmanager/tokenmanager/internal/format"
	"github.com/tokenmanager/tokenmanager/internal/model"
	"github.com/tokenmanager/tokenmanager/internal/optimizer"
)

// ---------------------------------------------------------------- optimize

func optimizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "optimize",
		Short: "Analyze the last 30 days and suggest ways to reduce token spend",
		Long: `Analyze usage from the last 30 days and print recommendations.
Savings are estimates based on recent usage and list prices, not guarantees.
TokenManager reads instruction files to size them but never modifies them.`,
		Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			fs, err := optimizer.Analyze(e.eng, e.filter())
			if err != nil {
				return err
			}
			if fl.json {
				return writeJSON(e.out, map[string]any{"window_days": 30, "currency": format.Currency, "savings_are_estimates": true, "findings": fs})
			}
			if fl.csv {
				t := newTable("severity", "kind", "title", "project", "file", "detail", "recommendation", "tokens_per_session", "estimated_monthly_savings")
				for _, f := range fs {
					t.add(f.SeverityName, f.Kind, f.Title, f.Project, f.File, f.Detail, f.Recommendation, fmt.Sprint(f.TokensPerSession), fmt.Sprintf("%.2f", f.MonthlySavings))
				}
				return t.csv(e.out)
			}
			printFindings(e.out, fs)
			return nil
		}),
	}
}

func printFindings(w io.Writer, fs []optimizer.Finding) {
	heading(w, "token optimization report", "last 30 days")
	if len(fs) == 0 {
		fmt.Fprintln(w, sGood.Render("  No issues found. Usage looks efficient."))
		return
	}
	var total float64
	for _, f := range fs {
		total += f.MonthlySavings
	}
	for i, f := range fs {
		sev := map[optimizer.Severity]string{
			optimizer.High: sBad.Render("● HIGH  "), optimizer.Medium: sWarn.Render("● MEDIUM"),
			optimizer.Low: sAccent.Render("● LOW   "), optimizer.Info: sDim.Render("● INFO  "),
		}[f.Severity]
		fmt.Fprintf(w, "\n  %s  %s\n", sev, sBold.Render(f.Title))
		kv := func(k, v string) {
			if v != "" {
				fmt.Fprintf(w, "    %s %s\n", sDim.Render(fmt.Sprintf("%-18s", k)), v)
			}
		}
		kv("Project", f.Project)
		kv("File", f.File)
		kv("Finding", f.Detail)
		if f.TokensPerSession > 0 {
			kv("Potential savings", fmt.Sprintf("~%s tokens per session (estimate)", format.Tokens(f.TokensPerSession)))
		}
		kv("Recommendation", f.Recommendation)
		if f.MonthlySavings >= 0.01 {
			kv("Est. monthly", sGood.Render(format.Money(f.MonthlySavings))+sDim.Render(" (estimate, not guaranteed)"))
		}
		_ = i
	}
	fmt.Fprintf(w, "\n  %s %s per month across %d findings. %s\n", sBold.Render("Estimated total:"), sGood.Render(format.Money(total)), len(fs),
		sDim.Render("Findings may overlap; nothing was changed on disk."))
}

// ---------------------------------------------------------------- budget

func budgetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "View and manage spending budgets",
		Long: `Show budget status. Budgets are monitoring only: TokenManager alerts you
but cannot enforce provider-side spend limits.

Examples:
  tokenmanager budget
  tokenmanager budget set monthly 100
  tokenmanager budget set daily 10 --for agent:claude-code
  tokenmanager budget set monthly 40 --for project:my-app
  tokenmanager budget rm monthly --for model:opus`,
		Args: cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			st, err := budget.Evaluate(e.eng)
			if err != nil {
				return err
			}
			return printBudgets(e, st)
		}),
	}
	var scope string
	set := &cobra.Command{
		Use: "set <daily|weekly|monthly> <amount>", Short: "Set a budget", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			amt, err := strconv.ParseFloat(strings.TrimPrefix(args[1], "$"), 64)
			if err != nil || amt < 0 {
				return fmt.Errorf("invalid amount %q", args[1])
			}
			return updateBudget(cmd, scope, args[0], amt)
		},
	}
	rm := &cobra.Command{
		Use: "rm <daily|weekly|monthly>", Short: "Remove a budget", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return updateBudget(cmd, scope, args[0], 0) },
	}
	for _, c := range []*cobra.Command{set, rm} {
		c.Flags().StringVar(&scope, "for", "", "scope as agent:<name>, project:<substring> or model:<substring> (default global)")
	}
	cmd.AddCommand(set, rm)
	return cmd
}

func updateBudget(cmd *cobra.Command, scope, period string, amt float64) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	kind, target := "global", ""
	if scope != "" {
		k, t, ok := strings.Cut(scope, ":")
		if !ok || t == "" {
			return fmt.Errorf("--for must look like agent:claude-code, project:name or model:opus")
		}
		kind, target = k, t
	}
	if err := budget.Set(cfg, kind, target, period, amt); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	format.Currency = cfg.Currency
	if amt == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s %s budget. Saved to %s\n", period, kind, config.Path())
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Set %s %s budget%s to %s. Saved to %s\n", period, kind, suffix(target), format.Money(amt), config.Path())
	}
	return nil
}

func suffix(t string) string {
	if t == "" {
		return ""
	}
	return " for " + t
}

func printBudgets(e *env, st []budget.Status) error {
	if fl.json {
		type bj struct {
			Name      string  `json:"name"`
			Scope     string  `json:"scope"`
			Target    string  `json:"target,omitempty"`
			Period    string  `json:"period"`
			Limit     float64 `json:"limit"`
			Spent     float64 `json:"spent"`
			Remaining float64 `json:"remaining"`
			Usage     float64 `json:"usage"`
			Projected float64 `json:"projected,omitempty"`
		}
		var out []bj
		for _, s := range st {
			out = append(out, bj{s.Name(), s.Scope, s.Target, s.Period, s.Limit, round2(s.Spent), round2(s.Remaining()), round2(s.Fraction()), round2(s.Projected)})
		}
		return writeJSON(e.out, map[string]any{"currency": format.Currency, "enforced": false, "budgets": out})
	}
	if len(st) == 0 {
		heading(e.out, "budgets", "")
		fmt.Fprintln(e.out, "  No budgets configured. Try:  tokenmanager budget set monthly 100")
		return nil
	}
	for _, s := range st {
		heading(e.out, s.Name()+" budget", s.PeriodStart.Format("since Jan 2"))
		style := usageStyle(s.Fraction())
		fmt.Fprintf(e.out, "  Spent: %s   Budget: %s   Remaining: %s\n", sBold.Render(format.Money(s.Spent)), format.Money(s.Limit), style.Render(format.Money(s.Remaining())))
		fmt.Fprintf(e.out, "  Usage: %s %s\n", style.Render(format.BlockBar(s.Fraction(), 24)), style.Render(format.Percent(s.Fraction())))
		if s.Projected > 0 {
			p := s.Projected / s.Limit
			fmt.Fprintf(e.out, "  Projected: %s %s\n", usageStyle(p).Render(format.Money(s.Projected)), sDim.Render("by end of month at the current pace"))
		}
	}
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, sDim.Render("  Monitoring only — provider-side limits are not changed. Alerts at "+thresholds(e.cfg)+"."))
	return nil
}

func thresholds(c *config.Config) string {
	var s []string
	for _, t := range c.Budgets.Thresholds {
		s = append(s, fmt.Sprintf("%.0f%%", t*100))
	}
	return strings.Join(s, ", ")
}

// ---------------------------------------------------------------- watch

func watchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Continuously monitor token consumption in real time",
		Long:  "Stream new requests as agents write them, using filesystem events. Ctrl-C to stop.",
		Args:  cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			today, _ := e.eng.Records(analytics.PeriodFor("today", e.eng.Now()), e.filter())
			sum := e.eng.Summary(today)
			w := e.out
			if !fl.json {
				heading(w, "live usage", "watching for new requests · Ctrl-C to stop")
				fmt.Fprintf(w, "  Today so far: %s tokens · %s\n\n", format.Tokens(sum.Tokens()), costCell(sum.Cost, sum.Unpriced))
				t := newTable("Time", "Agent", "Model", "Input", "Output", "Cache R", "Cache W", "Cost", "Today")
				t.render(w)
			}
			ch := make(chan []model.Record, 16)
			errc := make(chan error, 1)
			go func() { errc <- e.coll.Watch(ctx, ch) }()
			f := e.filter()
			for {
				select {
				case <-ctx.Done():
					return nil
				case err := <-errc:
					return err
				case recs := <-ch:
					for _, r := range recs {
						if (f.Agent != "" && r.Agent != f.Agent) || (f.Model != "" && !strings.Contains(r.Model, f.Model)) || (f.Project != "" && !strings.Contains(r.Project, f.Project)) {
							continue
						}
						c, ok := e.eng.Prices.Cost(r)
						e.eng.Add(&sum, r)
						if fl.json {
							_ = writeJSON(w, map[string]any{"time": r.Timestamp, "agent": r.Agent, "provider": r.Provider, "model": r.Model, "session": r.SessionID, "project": r.Project,
								"input": r.Input, "output": r.Output, "cache_read": r.CacheRead, "cache_write": r.CacheWrite(), "cost": round2(c), "priced": ok, "today_cost": round2(sum.Cost)})
							continue
						}
						cost := "n/a"
						if ok {
							cost = format.Money(c)
						}
						fmt.Fprintf(w, "  %s  %-11s  %-20s  %7s  %7s  %7s  %7s  %8s  %s\n", r.Timestamp.Local().Format("15:04:05"), format.Truncate(r.Agent, 11),
							format.Truncate(r.Model, 20), format.Tokens(r.Input), format.Tokens(r.Output), format.Tokens(r.CacheRead), format.Tokens(r.CacheWrite()),
							cost, sBold.Render(format.Money(sum.Cost)))
					}
				}
			}
		}),
	}
}

// ---------------------------------------------------------------- export

func exportCmd() *cobra.Command {
	var out, fmtName string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export usage records as JSON or CSV",
		Long:  "Export per-request records (token counts and metadata only, no conversation content).",
		Args:  cobra.NoArgs,
		RunE: run(func(e *env, _ []string) error {
			p := e.period("all")
			recs, err := e.eng.Records(p, e.filter())
			if err != nil {
				return err
			}
			if fl.csv {
				fmtName = "csv"
			}
			w := e.out
			if out != "" && out != "-" {
				f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}
			switch fmtName {
			case "json":
				type rj struct {
					Time         time.Time `json:"time"`
					Agent        string    `json:"agent"`
					Provider     string    `json:"provider"`
					Model        string    `json:"model"`
					Session      string    `json:"session"`
					Project      string    `json:"project"`
					Input        int64     `json:"input_tokens"`
					Output       int64     `json:"output_tokens"`
					CacheRead    int64     `json:"cache_read_tokens"`
					CacheWrite5m int64     `json:"cache_write_5m_tokens"`
					CacheWrite1h int64     `json:"cache_write_1h_tokens"`
					Reasoning    int64     `json:"reasoning_tokens"`
					Accuracy     string    `json:"token_accuracy"`
					Cost         *float64  `json:"estimated_cost"`
				}
				rows := make([]rj, 0, len(recs))
				for _, r := range recs {
					x := rj{r.Timestamp, r.Agent, r.Provider, r.Model, r.SessionID, r.Project, r.Input, r.Output, r.CacheRead, r.CacheWrite5m, r.CacheWrite1h, r.Reasoning, string(r.Accuracy), nil}
					if c, ok := e.eng.Prices.Cost(r); ok {
						c = float64(int64(c*1e6+0.5)) / 1e6
						x.Cost = &c
					}
					rows = append(rows, x)
				}
				err = writeJSON(w, map[string]any{"period": p.Name, "currency": format.Currency, "records": rows})
			case "csv":
				cw := csv.NewWriter(w)
				_ = cw.Write([]string{"time", "agent", "provider", "model", "session", "project", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens", "reasoning_tokens", "token_accuracy", "estimated_cost"})
				for _, r := range recs {
					cost := ""
					if c, ok := e.eng.Prices.Cost(r); ok {
						cost = strconv.FormatFloat(c, 'f', 6, 64)
					}
					_ = cw.Write([]string{r.Timestamp.Format(time.RFC3339), r.Agent, r.Provider, r.Model, r.SessionID, r.Project,
						i64(r.Input), i64(r.Output), i64(r.CacheRead), i64(r.CacheWrite5m), i64(r.CacheWrite1h), i64(r.Reasoning), string(r.Accuracy), cost})
				}
				cw.Flush()
				err = cw.Error()
			default:
				return fmt.Errorf("unknown format %q (want json or csv)", fmtName)
			}
			if err == nil && w != e.out {
				fmt.Fprintf(os.Stderr, "Exported %d records to %s\n", len(recs), out)
			}
			return err
		}),
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write to file instead of stdout")
	cmd.Flags().StringVarP(&fmtName, "format", "f", "json", "json or csv")
	return cmd
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// ---------------------------------------------------------------- daemon

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Run a lightweight background collector (and budget alerts)"}
	cmd.AddCommand(
		&cobra.Command{Use: "start", Short: "Start the background daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			pid, err := daemon.Start()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Daemon running (pid %d). Log: %s\n", pid, daemon.LogPath())
			return nil
		}},
		&cobra.Command{Use: "stop", Short: "Stop the background daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.Stop(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Daemon stopped.")
			return nil
		}},
		&cobra.Command{Use: "status", Short: "Show daemon status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if pid, ok := daemon.Running(); ok {
				fmt.Fprintf(cmd.OutOrStdout(), "Daemon running (pid %d). Log: %s\n", pid, daemon.LogPath())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon not running. Start it with: tokenmanager daemon start")
			}
			return nil
		}},
		&cobra.Command{Use: "run", Short: "Run the collector in the foreground (used by start, launchd, systemd)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			format.Currency = cfg.Currency
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, cfg)
		}},
	)
	return cmd
}

// ---------------------------------------------------------------- scan

func scanCmd() *cobra.Command {
	var rebuild bool
	cmd := &cobra.Command{
		Use: "scan", Short: "Ingest new usage from agent files now", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl.noScan = true
			e, err := setup(cmd)
			if err != nil {
				return err
			}
			defer e.close()
			if rebuild {
				if err := e.db.Reset(); err != nil {
					return err
				}
			}
			start := time.Now()
			res := e.coll.Scan()
			for _, err := range res.Errors {
				fmt.Fprintln(os.Stderr, sWarn.Render("warning: ")+err.Error())
			}
			st, _ := e.db.Stats()
			fmt.Fprintf(e.out, "Scanned %d files (%d changed) in %s: %d new requests. Database holds %s requests in %s.\n",
				res.Files, res.Changed, time.Since(start).Round(time.Millisecond), len(res.Added), format.Thousands(st.Records), e.cfg.DBPath())
			return nil
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "drop the database contents and re-read every file")
	return cmd
}

// ---------------------------------------------------------------- config

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or initialize configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Config:   %s\nDatabase: %s\nCurrency: %s · refresh %s · retention %s\n\nAgents:\n",
				config.Path(), cfg.DBPath(), cfg.Currency, cfg.RefreshInterval.Duration, retention(cfg.RetentionDays))
			for _, a := range agents.All() {
				st := agents.Discover(cfg, a)
				state := "not found"
				if !st.Enabled {
					state = "disabled"
				} else if st.Installed {
					state = fmt.Sprintf("detected, %d files", len(st.Files))
				}
				fmt.Fprintf(w, "  %-12s %-22s %s\n", a.Name, state, strings.Join(st.Roots, ", "))
			}
			return nil
		},
	}
	cmd.AddCommand(
		&cobra.Command{Use: "path", Short: "Print the config file path", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), config.Path())
		}},
		&cobra.Command{Use: "init", Short: "Write a config file with defaults and detected agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := os.Stat(config.Path()); err == nil {
				return fmt.Errorf("%s already exists", config.Path())
			}
			cfg := config.Default()
			t := true
			for _, a := range agents.All() {
				cfg.Agents[a.Name] = config.AgentConfig{Enabled: &t}
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", config.Path())
			return nil
		}},
	)
	return cmd
}

func retention(d int) string {
	if d <= 0 {
		return "forever"
	}
	return fmt.Sprintf("%d days", d)
}
