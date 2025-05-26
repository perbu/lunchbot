package main

import (
	"context"
	"github.com/perbu/lunchbot/bot"
	"github.com/perbu/lunchbot/config"
	"log"
	"os"
	"os/signal"
)

func main() {

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	lunchBot, err := bot.New(cfg)
	if err != nil {
		log.Fatal("Failed to create bot:", err)
	}

	// Start the scheduler in a separate goroutine
	go lunchBot.StartScheduler(ctx)

	go func() {
		<-ctx.Done()
		log.Println("Shutting down...")
		if err := lunchBot.Close(); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		os.Exit(0)
	}()

	// Start the bot (this will block)
	log.Println("Starting lunchbot...")
	if err := lunchBot.Start(ctx); err != nil {
		log.Fatal("Failed to start bot:", err)
	}
}
