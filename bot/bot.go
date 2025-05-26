package bot

import (
	"context"
	"errors"
	"fmt"
	"github.com/perbu/lunchbot/config"
	"github.com/perbu/lunchbot/storage"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type Bot struct {
	config       config.Config
	client       *slack.Client
	socketClient *socketmode.Client
	storage      *storage.Storage
	botUserID    string
}

func New(cfg config.Config) (*Bot, error) {
	client := slack.New(
		cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken),
	)

	socketClient := socketmode.New(
		client,
		socketmode.OptionDebug(false),
	)

	store, err := storage.New(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Get bot user ID
	authTest, err := client.AuthTest()
	if err != nil {
		return nil, fmt.Errorf("failed to get bot user ID: %w", err)
	}

	return &Bot{
		config:       cfg,
		client:       client,
		socketClient: socketClient,
		storage:      store,
		botUserID:    authTest.UserID,
	}, nil
}

func (b *Bot) Close() error {
	if b.storage != nil {
		return b.storage.Close()
	}
	return nil
}

func (b *Bot) Start(ctx context.Context) error {
	go func() {
		for evt := range b.socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				b.socketClient.Ack(*evt.Request)
				b.handleEvent(eventsAPIEvent)
			}
		}
	}()

	if err := b.socketClient.RunContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("b.socketClient.Run(): %w", err)
	}
	return nil
}

func (b *Bot) StartScheduler(ctx context.Context) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		log.Printf("Failed to load timezone: %v", err)
		location = time.UTC
	}

	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		<-ctx.Done()
		ticker.Stop()
	}()

	for range ticker.C {
		now := time.Now().In(location)

		// Check for daily report at 10:00
		if now.Hour() == 10 && now.Minute() == 0 {
			b.sendDailyReport()
		}

		// Check for warning at 9:50
		if now.Hour() == 9 && now.Minute() == 50 {
			b.sendWarning()
		}
	}
}

func (b *Bot) handleEvent(event slackevents.EventsAPIEvent) {
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			b.handleMention(ev)
		}
	}
}

func (b *Bot) handleMention(event *slackevents.AppMentionEvent) {
	if event.Channel != b.config.Channel {
		return
	}

	text := strings.TrimSpace(event.Text)

	// Remove the bot mention from the beginning
	botMention := fmt.Sprintf("<@%s>", b.botUserID)
	text = strings.TrimPrefix(text, botMention)
	text = strings.TrimSpace(text)

	if strings.HasPrefix(text, "lunch") {
		b.handleLunchCommand(event, text)
	} else if strings.HasPrefix(text, "vacation") {
		b.handleVacationCommand(event, text)
	} else {
		b.sendMessage(event.Channel, "Available commands:\n• `lunch (add|detract) <count> (today|tomorrow) <participants>`\n• `vacation <@user> YYYY-MM-DD YYYY-MM-DD`")
	}
}

func (b *Bot) handleLunchCommand(event *slackevents.AppMentionEvent, text string) {
	// Parse: lunch (add|detract) <int> (today|tomorrow) <participant> [<participant> …]
	re := regexp.MustCompile(`lunch\s+(add|detract)\s+(\d+)\s+(today|tomorrow)\s+(.+)`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 5 {
		b.sendMessage(event.Channel, "Invalid format. Use: `@lunchbot lunch (add|detract) <count> (today|tomorrow) <participants>`")
		return
	}

	verb := matches[1]
	count, _ := strconv.Atoi(matches[2])
	when := matches[3]
	participantsStr := strings.TrimSpace(matches[4])

	participants := strings.Fields(participantsStr)
	if len(participants) != count {
		b.sendMessage(event.Channel, fmt.Sprintf("Number of participants (%d) doesn't match count (%d)", len(participants), count))
		return
	}

	// Calculate target date
	targetDate := time.Now()
	if when == "tomorrow" {
		targetDate = targetDate.AddDate(0, 0, 1)
	}
	dateStr := targetDate.Format("2006-01-02")

	// Store in database
	err := b.storage.AddLunchRecord(dateStr, event.User, verb, count, participants)
	if err != nil {
		log.Printf("Failed to insert lunch record: %v", err)
		b.sendMessage(event.Channel, "Failed to record lunch data")
		return
	}

	// Calculate new total
	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		log.Printf("Failed to calculate total: %v", err)
		b.sendMessage(event.Channel, "Failed to calculate total")
		return
	}

	if total < 0 {
		b.sendMessage(event.Channel, "Invalid operation: total cannot be negative")
		return
	}

	b.sendMessage(event.Channel, fmt.Sprintf("Recorded %s %d for %s. Total for %s: %d", verb, count, when, dateStr, total))
}

func (b *Bot) handleVacationCommand(event *slackevents.AppMentionEvent, text string) {
	// Parse: vacation <user> <from> <to>
	re := regexp.MustCompile(`vacation\s+<@([^>]+)>\s+(\d{4}-\d{2}-\d{2})\s+(\d{4}-\d{2}-\d{2})`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 4 {
		b.sendMessage(event.Channel, "Invalid format. Use: `@lunchbot vacation <@user> YYYY-MM-DD YYYY-MM-DD`")
		return
	}

	userID := matches[1]
	fromDate := matches[2]
	toDate := matches[3]

	// Validate dates
	if err := b.storage.ValidateDate(fromDate); err != nil {
		b.sendMessage(event.Channel, "Invalid from date format. Use YYYY-MM-DD")
		return
	}
	if err := b.storage.ValidateDate(toDate); err != nil {
		b.sendMessage(event.Channel, "Invalid to date format. Use YYYY-MM-DD")
		return
	}

	err := b.storage.AddVacationRecord(userID, fromDate, toDate)
	if err != nil {
		log.Printf("Failed to insert vacation record: %v", err)
		b.sendMessage(event.Channel, "Failed to record vacation")
		return
	}

	b.sendMessage(event.Channel, fmt.Sprintf("Recorded vacation for <@%s> from %s to %s", userID, fromDate, toDate))
}

func (b *Bot) sendMessage(channel, text string) {
	_, _, err := b.client.PostMessage(channel, slack.MsgOptionText(text, false))
	if err != nil {
		log.Printf("Failed to send message: %v", err)
	}
}

func (b *Bot) sendDailyReport() {
	today := time.Now().Format("2006-01-02")
	total, err := b.storage.CalculateTotal(today, b.config.Baseline)
	if err != nil {
		log.Printf("Failed to calculate total for daily report: %v", err)
		return
	}

	message := fmt.Sprintf("Daily lunch report for %s: %d people", today, total)
	b.sendMessage(b.config.ReportUser, message)
}

func (b *Bot) sendWarning() {
	b.sendMessage(b.config.Channel, "⚠️ Lunch order closes in 10 minutes!")
}
