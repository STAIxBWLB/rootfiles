package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/STAIxBWLB/rootfiles/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, version, commit); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.Msg != "" {
				fmt.Fprintln(os.Stderr, "Error:", exitErr.Msg)
			}
			stop()
			os.Exit(exitErr.Code)
		}
		// Surface the error before exiting so upgrade/apply failures are
		// not silent. Historically `rootfiles upgrade` would exit 1 with no
		// output when the binary replace or a network call failed, making
		// the root cause invisible in CI logs.
		fmt.Fprintln(os.Stderr, "Error:", err)
		stop()
		os.Exit(1)
	}
}
