package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ht2803-max/tokenmanager/internal/web"
)

// stdinIsTerminal reports whether we can ask the user a question; replaced in tests.
var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func webCmd() *cobra.Command {
	var output string
	var open, force bool
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Generate a standalone HTML dashboard to open in a browser",
		Long: `Generate a single self-contained HTML file with charts and tables of your
usage, costs, agents, sessions, projects, models, budgets and optimization
findings. The file works offline and makes no network requests; no server is
started. It is a snapshot: run the command again to see newer usage.

The file contains aggregated usage data (no prompts or responses). Treat it
as private. --agent, --project and --model narrow what is exported.

Examples:
  tokenmanager web
  tokenmanager web --open
  tokenmanager web --output ./dashboard.html --force
  tokenmanager web --no-scan`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, custom, err := web.ResolveOutput(output)
			if err != nil {
				return err
			}
			// The managed default file is always replaced; a file the user
			// pointed at needs --force or a confirmation. Ask before scanning.
			overwrite := force || !custom
			if !overwrite {
				if _, err := os.Stat(path); err == nil {
					if !stdinIsTerminal() {
						return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
					}
					if !confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("%s already exists. Overwrite? [y/N] ", path)) {
						return errors.New("aborted; nothing was written")
					}
					overwrite = true
				}
			}
			e, err := setup(cmd)
			if err != nil {
				return err
			}
			defer e.close()
			d, err := web.Build(e.eng, web.Options{Filter: e.filter(), Scanned: !fl.noScan, Version: Version})
			if err != nil {
				return err
			}
			page, err := web.Render(d)
			if err != nil {
				return err
			}
			if err := web.Write(path, page, overwrite); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s Dashboard written to %s\n", sGood.Render("✓"), sBold.Render(path))
			fmt.Fprintln(e.out, sDim.Render(fmt.Sprintf("  %d requests · %d sessions · %d KB · static snapshot, run again to refresh",
				d.Meta.Records, d.Meta.SessionsTotal, (len(page)+1023)/1024)))
			if open {
				if err := web.Open(path); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), sWarn.Render("warning: ")+err.Error()+"; open the file manually.")
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&output, "output", "o", "", "write the dashboard to this file (default "+web.DefaultPath()+")")
	f.BoolVar(&open, "open", false, "open the dashboard in the default browser")
	f.BoolVarP(&force, "force", "f", false, "overwrite an existing --output file without asking")
	return cmd
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
