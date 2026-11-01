// Package cli implements the tokenmanager command line.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/ht2803-max/tokenmanager/internal/analytics"
	"github.com/ht2803-max/tokenmanager/internal/collector"
	"github.com/ht2803-max/tokenmanager/internal/config"
	"github.com/ht2803-max/tokenmanager/internal/format"
	"github.com/ht2803-max/tokenmanager/internal/storage"
	"github.com/ht2803-max/tokenmanager/internal/tui"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

type flags struct {
	json, csv               bool
	today, week, month, all bool
	agent, project, model   string
	noScan                  bool
}

var fl flags

// env is everything a command needs.
type env struct {
	cfg  *config.Config
	db   *storage.DB
	eng  *analytics.Engine
	coll *collector.Collector
	out  io.Writer
}

func (e *env) close() { e.db.Close() }

// filter builds a storage filter from --agent/--project/--model.
func (e *env) filter() storage.Filter {
	return storage.Filter{Agent: fl.agent, Project: fl.project, Model: fl.model}
}

// period resolves --today/--week/--month/--all with a per-command default.
func (e *env) period(def string) analytics.Period {
	name := def
	switch {
	case fl.today:
		name = "today"
	case fl.week:
		name = "week"
	case fl.month:
		name = "month"
	case fl.all:
		name = "all"
	}
	return analytics.PeriodFor(name, e.eng.Now())
}

func setup(cmd *cobra.Command) (*env, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	format.Currency = cfg.Currency
	db, err := storage.Open(cfg.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	e := &env{cfg: cfg, db: db, eng: analytics.New(cfg, db), out: cmd.OutOrStdout()}
	e.coll = collector.New(cfg, db, logger())
	if !fl.noScan {
		res := e.coll.Scan()
		for _, err := range res.Errors {
			fmt.Fprintln(os.Stderr, sWarn.Render("warning: ")+err.Error())
		}
	}
	return e, nil
}

func logger() *slog.Logger {
	if os.Getenv("TOKENMANAGER_DEBUG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return nil
}

// run wraps a command body with setup/teardown.
func run(fn func(e *env, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		e, err := setup(cmd)
		if err != nil {
			return err
		}
		defer e.close()
		return fn(e, args)
	}
}

// NewRoot builds the command tree.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tokenmanager",
		Short: "Monitor, analyze and optimize AI coding agent token usage",
		Long: `TokenManager tracks token consumption and estimated spend across AI coding
agents (Claude Code, OpenAI Codex, Gemini CLI, Aider, Continue) from their
local session files. Nothing leaves your machine.

Run without arguments to open the interactive dashboard.`,
		SilenceUsage: true,
		Version:      Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := setup(cmd)
			if err != nil {
				return err
			}
			defer e.close()
			return tui.Run(e.cfg, e.db, e.eng, e.coll, e.filter())
		},
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&fl.json, "json", false, "output JSON")
	pf.BoolVar(&fl.csv, "csv", false, "output CSV")
	pf.BoolVar(&fl.today, "today", false, "limit to today")
	pf.BoolVar(&fl.week, "week", false, "limit to this week (Monday-based)")
	pf.BoolVar(&fl.month, "month", false, "limit to this calendar month")
	pf.BoolVar(&fl.all, "all", false, "all recorded history")
	pf.StringVar(&fl.agent, "agent", "", "filter by agent (claude-code, codex, gemini-cli, aider, continue)")
	pf.StringVar(&fl.project, "project", "", "filter by project path (substring)")
	pf.StringVar(&fl.model, "model", "", "filter by model (substring)")
	pf.BoolVar(&fl.noScan, "no-scan", false, "skip ingesting new agent data before running")

	root.AddCommand(
		periodCmd("today", "Show today's token consumption and spending"),
		periodCmd("week", "Show this week's usage"),
		periodCmd("month", "Show this month's spending and token consumption"),
		agentsCmd(), sessionsCmd(), projectsCmd(), modelsCmd(), costsCmd(),
		optimizeCmd(), budgetCmd(), watchCmd(), exportCmd(), daemonCmd(),
		scanCmd(), configCmd(),
	)
	return root
}

// Execute runs the CLI.
func Execute() {
	if err := NewRoot().Execute(); err != nil {
		os.Exit(1)
	}
}
