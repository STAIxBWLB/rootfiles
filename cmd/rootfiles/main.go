package main

import (
	"context"
	"errors"
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
			stop()
			os.Exit(exitErr.Code)
		}
		stop()
		os.Exit(1)
	}
}
