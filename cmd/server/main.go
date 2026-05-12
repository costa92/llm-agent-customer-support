package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/costa92/llm-agent-customer-support/internal/app"
	"github.com/costa92/llm-agent-customer-support/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build app: %v", err)
	}

	if err := application.Run(ctx); err != nil {
		log.Printf("server stopped with error: %v", err)
		os.Exit(1)
	}
}
