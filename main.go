package main

import (
	"github.com/perbu/lunchbot/bot"
	"github.com/perbu/lunchbot/config"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	lunchBot, err := bot.New(cfg)
	if err != nil {
		log.Fatal("Failed to create bot:", err)
	}
	defer func() {
		if err := lunchBot.Close(); err != nil {
			log.Printf("Error closing bot: %v", err)
		}
	}()

	// Start the scheduler in a separate goroutine
	go lunchBot.StartScheduler()

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("Shutting down...")
		if err := lunchBot.Close(); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		os.Exit(0)
	}()

	// Start the bot (this will block)
	log.Println("Starting lunchbot...")
	lunchBot.Start()
}
