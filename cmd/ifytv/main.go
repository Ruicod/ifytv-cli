package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Ruicod/ifytv-cli/internal/ifytvcli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := ifytvcli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, version)
	stop()
	os.Exit(code)
}
