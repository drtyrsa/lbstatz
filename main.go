package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/drtyrsa/lbstatz/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := cli.NewRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lbstatz:", err)
		os.Exit(1)
	}
}
