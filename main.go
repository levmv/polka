package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/levmv/polka/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Restore default handling after the first signal so a second forces exit
	// if graceful shutdown stalls.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := cli.RunContext(ctx, os.Args[1:]); err != nil {
		if !cli.IsReportedFailure(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}
