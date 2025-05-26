package main

import (
	"context"
	"github.com/perbu/lunchbot/bot"
	"github.com/perbu/lunchbot/config"
	"log"
	"log/slog"
	"os"
	"os/signal"
)

func main() {
	// Initialize structured logger with DEBUG level
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	lunchBot, err := bot.New(cfg, logger)
	if err != nil {
		log.Fatal("Failed to create bot:", err)
	}

	// Start the scheduler in a separate goroutine
	go lunchBot.StartScheduler(ctx)

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down...")
		if err := lunchBot.Close(); err != nil {
			logger.Error("Error during shutdown", "error", err)
		}
		os.Exit(0)
	}()

	// Start the bot (this will block)
	logger.Info("Starting lunchbot...")
	if err := lunchBot.Start(ctx); err != nil {
		logger.Error("Failed to start bot", "error", err)
		os.Exit(1)
	}
}
