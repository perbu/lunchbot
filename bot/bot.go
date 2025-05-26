package bot

import (
	"context"
	"errors"
	"fmt"
	"github.com/perbu/lunchbot/config"
	"github.com/perbu/lunchbot/storage"
	"log/slog"
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
	logger       *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) (*Bot, error) {
	client := slack.New(
		cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken),
	)

	socketClient := socketmode.New(
		client,
		socketmode.OptionDebug(false),
	)
	store, err := storage.New(cfg.DBPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Get bot user ID
	authTest, err := client.AuthTest()
	if err != nil {
		return nil, fmt.Errorf("failed to get bot user ID: %w", err)
	}
	logger.Info("connect to slack socket client", "bot ID", authTest.BotID)

	return &Bot{
		config:       cfg,
		client:       client,
		socketClient: socketClient,
		storage:      store,
		botUserID:    authTest.UserID,
		logger:       logger,
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
		b.logger.Error("Failed to load timezone", "error", err)
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
	b.logger.Debug("Received event", "type", event.Type)
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent
		b.logger.Debug("Callback event", "inner_type", innerEvent.Type)
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			b.logger.Debug("Processing app mention")
			b.handleMention(ev)
		case *slackevents.MessageEvent:
			b.logger.Debug("Message event", "channel_type", ev.ChannelType, "user", ev.User, "text", ev.Text)
			if ev.ChannelType == "im" {
				b.logger.Info("Processing DM")
				b.handleDirectMessage(ev)
			} else {
				b.logger.Debug("Ignoring non-DM message", "channel_type", ev.ChannelType)
			}
		default:
			b.logger.Debug("Unknown event type", "type", fmt.Sprintf("%T", ev))
		}
	}
}

func (b *Bot) handleDirectMessage(event *slackevents.MessageEvent) {
	b.logger.Debug("DM handler called", "user", event.User, "bot_user_id", b.botUserID, "subtype", event.SubType)

	// Skip bot messages and messages with subtypes (like file uploads)
	if event.User == b.botUserID {
		b.logger.Debug("Skipping bot's own message")
		return
	}
	if event.SubType != "" {
		b.logger.Debug("Skipping message with subtype", "subtype", event.SubType)
		return
	}

	text := strings.TrimSpace(event.Text)
	b.logger.Info("Received DM from user", "user", event.User, "text", text)

	if strings.HasPrefix(text, "status") {
		b.logger.Info("Processing status command via DM")
		b.handleLunchStatusCommandDM(event, text)
	} else if strings.HasPrefix(text, "add") || strings.HasPrefix(text, "detract") {
		b.logger.Info("Processing add/detract command via DM")
		b.handleLunchCommandDM(event, text)
	} else if strings.HasPrefix(text, "vacation") {
		b.logger.Info("Processing vacation command via DM")
		b.handleVacationCommandDM(event, text)
	} else {
		b.logger.Info("Unknown DM command, showing help")
		b.sendMessage(event.Channel, "Available commands:\n• `(add|detract) <count> (today|tomorrow) <participants>`\n• `status [today|tomorrow|YYYY-MM-DD]`\n• `vacation <@user> YYYY-MM-DD YYYY-MM-DD`")
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

	b.logger.Info("Received command from user", "user", event.User, "text", text)

	if strings.HasPrefix(text, "status") {
		b.logger.Info("Processing status command")
		b.handleLunchStatusCommand(event, text)
	} else if strings.HasPrefix(text, "add") || strings.HasPrefix(text, "detract") {
		b.logger.Info("Processing add/detract command")
		b.handleLunchCommand(event, text)
	} else if strings.HasPrefix(text, "vacation") {
		b.logger.Info("Processing vacation command")
		b.handleVacationCommand(event, text)
	} else {
		b.logger.Info("Unknown command, showing help")
		b.sendMessage(event.Channel, "Available commands:\n• `(add|detract) <count> (today|tomorrow) <participants>`\n• `status [today|tomorrow|YYYY-MM-DD]`\n• `vacation <@user> YYYY-MM-DD YYYY-MM-DD`")
	}
}

func (b *Bot) handleLunchCommand(event *slackevents.AppMentionEvent, text string) {
	// Parse: (add|detract) <int> (today|tomorrow) <participant> [<participant> …]
	re := regexp.MustCompile(`(add|detract)\s+(\d+)\s+(today|tomorrow)\s+(.+)`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 5 {
		b.sendMessage(event.Channel, "❌ Invalid format. Use: `@lunchbot (add|detract) <count> (today|tomorrow) <participants>`")
		return
	}

	verb := matches[1]
	count, _ := strconv.Atoi(matches[2])
	when := matches[3]
	participantsStr := strings.TrimSpace(matches[4])

	participants := strings.Fields(participantsStr)
	if len(participants) != count {
		b.sendMessage(event.Channel, fmt.Sprintf("❌ Number of participants (%d) doesn't match count (%d)", len(participants), count))
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
		b.logger.Error("Failed to insert lunch record", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to record lunch data")
		return
	}

	b.logger.Info("Successfully recorded lunch", "verb", verb, "count", count, "date", dateStr, "user", event.User)

	// Calculate new total
	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("❌ Failed to calculate total", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to calculate total")
		return
	}

	if total < 0 {
		b.logger.Warn("Invalid operation: total would be negative", "total", total)
		b.sendMessage(event.Channel, "❌ Invalid operation: total cannot be negative")
		return
	}

	b.logger.Info("New total calculated", "date", dateStr, "total", total)
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
		b.logger.Error("Failed to insert vacation record", "error", err)
		b.sendMessage(event.Channel, "Failed to record vacation")
		return
	}

	b.logger.Info("Successfully recorded vacation", "user", userID, "from", fromDate, "to", toDate)
	b.sendMessage(event.Channel, fmt.Sprintf("Recorded vacation for <@%s> from %s to %s", userID, fromDate, toDate))
}

func (b *Bot) handleLunchStatusCommand(event *slackevents.AppMentionEvent, text string) {
	// Parse: status [today|tomorrow|YYYY-MM-DD]
	parts := strings.Fields(text)
	when := "today"
	var dateStr string
	var targetDate time.Time

	if len(parts) >= 2 {
		dateInput := parts[1]
		if dateInput == "today" {
			when = "today"
			targetDate = time.Now()
		} else if dateInput == "tomorrow" {
			when = "tomorrow"
			targetDate = time.Now().AddDate(0, 0, 1)
		} else {
			// Try to parse as YYYY-MM-DD
			if err := b.storage.ValidateDate(dateInput); err != nil {
				b.sendMessage(event.Channel, "Invalid date format. Use 'today', 'tomorrow', or YYYY-MM-DD")
				return
			}
			when = dateInput
			var err error
			targetDate, err = time.Parse("2006-01-02", dateInput)
			if err != nil {
				b.sendMessage(event.Channel, "Invalid date format. Use 'today', 'tomorrow', or YYYY-MM-DD")
				return
			}
		}
	} else {
		targetDate = time.Now()
	}

	dateStr = targetDate.Format("2006-01-02")

	// Get lunch records for the date
	records, err := b.storage.GetLunchRecordsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get lunch records", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve lunch status")
		return
	}

	// Get vacation count and names
	vacationCount, err := b.storage.GetVacationCountForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get vacation count", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve vacation status")
		return
	}

	vacationUsers, err := b.storage.GetVacationsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get vacation users", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve vacation users")
		return
	}

	// Calculate total
	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("❌ Failed to calculate total", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to calculate total")
		return
	}

	// Build status message
	message := fmt.Sprintf("📊 Lunch status for %s (%s):\n", when, dateStr)
	message += fmt.Sprintf("• Baseline: %d\n", b.config.Baseline)

	if len(records) > 0 {
		message += "• Changes:\n"
		for _, record := range records {
			participantsList := strings.Join(record.Participants, " ")
			verb := record.Verb
			if verb == "add" {
				verb = " • Added"
			} else {
				verb = " • Subtracted"
			}
			message += fmt.Sprintf("  - %s %d: %s\n", verb, record.Count, participantsList)
		}
	} else {
		message += "• Changes: None\n"
	}

	if vacationCount > 0 {
		vacationNames := make([]string, len(vacationUsers))
		for i, userID := range vacationUsers {
			user, err := b.client.GetUserInfo(userID)
			if err != nil {
				b.logger.Warn("Failed to get user info", "user", userID, "error", err)
				vacationNames[i] = userID // Fallback to user ID
			} else {
				vacationNames[i] = user.Name
			}
		}
		message += fmt.Sprintf("• On vacation: %s\n", strings.Join(vacationNames, " "))
	}

	message += fmt.Sprintf("• Total: %d lunches", total)

	b.logger.Info("Provided status", "date", dateStr, "total", total, "baseline", b.config.Baseline, "changes", len(records), "vacations", vacationCount)
	b.sendMessage(event.Channel, message)
}

func (b *Bot) sendMessage(channel, text string) {
	_, _, err := b.client.PostMessage(channel, slack.MsgOptionText(text, false))
	if err != nil {
		b.logger.Error("Failed to send message", "error", err)
	}
}

func (b *Bot) handleLunchCommandDM(event *slackevents.MessageEvent, text string) {
	// Parse: (add|detract) <int> (today|tomorrow) <participant> [<participant> …]
	re := regexp.MustCompile(`(add|detract)\s+(\d+)\s+(today|tomorrow)\s+(.+)`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 5 {
		b.sendMessage(event.Channel, "❌ Invalid format. Use: `(add|detract) <count> (today|tomorrow) <participants>`")
		return
	}

	verb := matches[1]
	count, _ := strconv.Atoi(matches[2])
	when := matches[3]
	participantsStr := strings.TrimSpace(matches[4])

	participants := strings.Fields(participantsStr)
	if len(participants) != count {
		b.sendMessage(event.Channel, fmt.Sprintf("❌ Number of participants (%d) doesn't match count (%d)", len(participants), count))
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
		b.logger.Error("Failed to insert lunch record", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to record lunch data")
		return
	}

	b.logger.Info("Successfully recorded lunch via DM", "verb", verb, "count", count, "date", dateStr, "user", event.User)

	// Calculate new total
	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("❌ Failed to calculate total", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to calculate total")
		return
	}

	if total < 0 {
		b.logger.Warn("Invalid operation: total would be negative", "total", total)
		b.sendMessage(event.Channel, "❌ Invalid operation: total cannot be negative")
		return
	}

	b.logger.Info("New total calculated", "date", dateStr, "total", total)

	// Send confirmation to user
	b.sendMessage(event.Channel, fmt.Sprintf("✅ Recorded %s %d for %s. Total for %s: %d", verb, count, when, dateStr, total))

	// Send notification to main channel
	channelMessage := fmt.Sprintf("📝 <@%s> %s %d lunches for %s (%s). New total: %d", event.User, verb, count, when, strings.Join(participants, " "), total)
	b.sendMessage(b.config.Channel, channelMessage)
}

func (b *Bot) handleVacationCommandDM(event *slackevents.MessageEvent, text string) {
	// Parse: vacation <user> <from> <to>
	re := regexp.MustCompile(`vacation\s+<@([^>]+)>\s+(\d{4}-\d{2}-\d{2})\s+(\d{4}-\d{2}-\d{2})`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 4 {
		b.sendMessage(event.Channel, "Invalid format. Use: `vacation <@user> YYYY-MM-DD YYYY-MM-DD`")
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
		b.logger.Error("Failed to insert vacation record", "error", err)
		b.sendMessage(event.Channel, "Failed to record vacation")
		return
	}

	b.logger.Info("Successfully recorded vacation via DM", "target_user", userID, "from", fromDate, "to", toDate, "user", event.User)

	// Send confirmation to user
	b.sendMessage(event.Channel, fmt.Sprintf("✅ Recorded vacation for <@%s> from %s to %s", userID, fromDate, toDate))

	// Send notification to main channel
	channelMessage := fmt.Sprintf("🏖️ <@%s> recorded vacation for <@%s> from %s to %s", event.User, userID, fromDate, toDate)
	b.sendMessage(b.config.Channel, channelMessage)
}

func (b *Bot) handleLunchStatusCommandDM(event *slackevents.MessageEvent, text string) {
	// Parse: status [today|tomorrow|YYYY-MM-DD]
	parts := strings.Fields(text)
	when := "today"
	var dateStr string
	var targetDate time.Time

	if len(parts) >= 2 {
		dateInput := parts[1]
		if dateInput == "today" {
			when = "today"
			targetDate = time.Now()
		} else if dateInput == "tomorrow" {
			when = "tomorrow"
			targetDate = time.Now().AddDate(0, 0, 1)
		} else {
			// Try to parse as YYYY-MM-DD
			if err := b.storage.ValidateDate(dateInput); err != nil {
				b.sendMessage(event.Channel, "Invalid date format. Use 'today', 'tomorrow', or YYYY-MM-DD")
				return
			}
			when = dateInput
			var err error
			targetDate, err = time.Parse("2006-01-02", dateInput)
			if err != nil {
				b.sendMessage(event.Channel, "Invalid date format. Use 'today', 'tomorrow', or YYYY-MM-DD")
				return
			}
		}
	} else {
		targetDate = time.Now()
	}

	dateStr = targetDate.Format("2006-01-02")

	// Get lunch records for the date
	records, err := b.storage.GetLunchRecordsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get lunch records", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve lunch status")
		return
	}

	// Get vacation count and names
	vacationCount, err := b.storage.GetVacationCountForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get vacation count", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve vacation status")
		return
	}

	vacationUsers, err := b.storage.GetVacationsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get vacation users", "error", err)
		b.sendMessage(event.Channel, "Failed to retrieve vacation users")
		return
	}

	// Calculate total
	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("❌ Failed to calculate total", "error", err)
		b.sendMessage(event.Channel, "❌ Failed to calculate total")
		return
	}

	// Build status message
	message := fmt.Sprintf("📊 Lunch status for %s (%s):\n", when, dateStr)
	message += fmt.Sprintf("• Baseline: %d\n", b.config.Baseline)

	if len(records) > 0 {
		message += "• Changes:\n"
		for _, record := range records {
			participantsList := strings.Join(record.Participants, " ")
			verb := record.Verb
			if verb == "add" {
				verb = " • Added"
			} else {
				verb = " • Subtracted"
			}
			message += fmt.Sprintf("  - %s %d: %s\n", verb, record.Count, participantsList)
		}
	} else {
		message += "• Changes: None\n"
	}

	if vacationCount > 0 {
		vacationNames := make([]string, len(vacationUsers))
		for i, userID := range vacationUsers {
			user, err := b.client.GetUserInfo(userID)
			if err != nil {
				b.logger.Warn("Failed to get user info", "user", userID, "error", err)
				vacationNames[i] = userID // Fallback to user ID
			} else {
				vacationNames[i] = user.Name
			}
		}
		message += fmt.Sprintf("• On vacation: %s\n", strings.Join(vacationNames, " "))
	}

	message += fmt.Sprintf("• Total: %d lunches", total)

	b.logger.Info("Provided status via DM", "date", dateStr, "total", total, "baseline", b.config.Baseline, "changes", len(records), "vacations", vacationCount)
	b.sendMessage(event.Channel, message)
}

func (b *Bot) sendDailyReport() {
	today := time.Now().Format("2006-01-02")
	total, err := b.storage.CalculateTotal(today, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total for daily report", "error", err)
		return
	}

	b.logger.Info("Sending daily report", "date", today, "total", total)
	message := fmt.Sprintf("Daily lunch report for %s: %d people", today, total)
	b.sendMessage(b.config.ReportUser, message)
}

func (b *Bot) sendWarning() {
	b.logger.Info("Sending lunch order warning")
	b.sendMessage(b.config.Channel, "⚠️ Lunch order closes in 10 minutes!")
}
