package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
	"github.com/spf13/cobra"
)

func newFleetPingCmd() *cobra.Command {
	c := &cobra.Command{Use: "ping", Short: "Check fleet connectivity and privilege", Args: cobra.NoArgs,
		Example: "  rootfiles fleet ping --all\n  rootfiles fleet ping --host gpu01 --json",
		RunE: func(c *cobra.Command, _ []string) error {
			inv, hosts, err := loadFleetSelection(c, false)
			if err != nil {
				return err
			}
			parallel, _ := c.Flags().GetInt("parallel")
			if parallel < 0 || parallel > 128 || (c.Flags().Changed("parallel") && parallel == 0) {
				return fmt.Errorf("--parallel must be between 1 and 128")
			}
			if !c.Flags().Changed("parallel") {
				parallel = inv.Defaults.Parallel
			}
			if parallel > len(hosts) {
				parallel = len(hosts)
			}
			results := pingFleetFn(c.Context(), fleet.SSHRunner{Binary: "ssh"}, hosts, parallel, buildVersion)
			format, err := outputFormat(c)
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			if format == "text" {
				ui.WriteHeader(out, c.CommandPath())
			}
			if err := writeFleetResults(out, results, format); err != nil {
				return err
			}
			if format == "text" {
				ui.WriteSummary(out, fleetStateCounts(results))
			}
			if !allOK(results) {
				return &ExitError{Code: exitDrift}
			}
			return nil
		}}
	fleetCommonFlags(c)
	c.Flags().Int("parallel", 0, "Maximum concurrent SSH processes")
	addOutputFlag(c)
	return c
}

// pingFleetFn renders the connectivity report. A test replaces it with fixture
// results, so the layout is pinned without opening a connection.
var pingFleetFn = pingFleet

func pingFleet(ctx context.Context, runner fleet.SSHRunner, hosts []fleet.NamedHost, workers int, version string) []fleet.Result {
	results := make([]fleet.Result, len(hosts))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				results[n] = runner.Ping(ctx, hosts[n], version)
			}
		}()
	}
	for i := range hosts {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}
