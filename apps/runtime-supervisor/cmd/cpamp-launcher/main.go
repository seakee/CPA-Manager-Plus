package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

func main() {
	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "cpamp-launcher is only supported on linux")
		os.Exit(1)
	}
	prepared, err := prepare(PrepareConfigFromEnv(), DefaultSystem())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	supCfg := NewSupervisorConfig(prepared, os.Environ())
	if err := RunSupervisor(ctx, supCfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
