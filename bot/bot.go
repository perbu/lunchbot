package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/perbu/lunchbot/config"
	"github.com/perbu/lunchbot/storage"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Constants for command keywords and date formats
const (
	dateFormat     = "2006-01-02"
	cmdAdd         = "add"
	cmdDetract     = "detract"
	cmdStatus      = "status"
	cmdVacation    = "vacation"
	argToday       = "today"
	argTomorrow    = "tomorrow"
	verbAddDisplay = " • Added"
	verbSubDisplay = " • Subtracted"

	helpMessage = "Available commands:\n" +
		"• `(add|detract) <count> (today|tomorrow) <participants>` (e.g., `add 2 today @user1 @user2`)\n" +
		"• `status [today|tomorrow|YYYY-MM-DD]` (e.g., `status tomorrow`)\n" +
		"• `vacation <@user> YYYY-MM-DD YYYY-MM-DD` (e.g., `vacation @user1 2024-12-20 2024-12-25`) \n" +
		"Source available at https://github.com/perbu/lunchbot"
)

// Pre-compiled regexes for command parsing
var (
	lunchCommandRegex    = regexp.MustCompile(`^(add|detract)\s+(\d+)\s+(today|tomorrow)\s+(.+)`)
	vacationCommandRegex = regexp.MustCompile(`^vacation\s+<@([^>]+)>\s+(\d{4}-\d{2}-\d{2})\s+(\d{4}-\d{2}-\d{2})`)
)

// CommandLogicInput holds the necessary details for processing a command's core logic.
type CommandLogicInput struct {
	UserID         string
	ChannelID      string
	Text           string // The command text itself (e.g., "add 2 today @user1")
	IsDM           bool
	RespondingUser string // User who initiated the command
}

// Bot structure remains largely the same
type Bot struct {
	config       config.Config
	client       *slack.Client
	socketClient *socketmode.Client
	storage      *storage.Storage
	botUserID    string
	logger       *slog.Logger

	// Command registries
	appMentionCommandHandlers map[string]AppMentionCommandHandler
	dmCommandHandlers         map[string]DMCommandHandler
}

// Handler function types for registered commands
type AppMentionCommandHandler func(b *Bot, event *slackevents.AppMentionEvent, processedText string)
type DMCommandHandler func(b *Bot, event *slackevents.MessageEvent, text string)

func New(cfg config.Config, logger *slog.Logger) (*Bot, error) {
	client := slack.New(
		cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken),
	)

	socketClient := socketmode.New(
		client,
		socketmode.OptionDebug(false), // Set to true for more verbose socket mode logging
	)
	store, err := storage.New(cfg.DBPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	authTest, err := client.AuthTest()
	if err != nil {
		return nil, fmt.Errorf("failed to get bot user ID: %w", err)
	}
	logger.Info("Connected to Slack", "bot_user_id", authTest.UserID, "bot_id", authTest.BotID)

	b := &Bot{
		config:                    cfg,
		client:                    client,
		socketClient:              socketClient,
		storage:                   store,
		botUserID:                 authTest.UserID, // Use UserID for @mentions
		logger:                    logger,
		appMentionCommandHandlers: make(map[string]AppMentionCommandHandler),
		dmCommandHandlers:         make(map[string]DMCommandHandler),
	}
	b.registerCommands()
	return b, nil
}

// registerCommands populates the command handlers for DMs and mentions.
func (b *Bot) registerCommands() {
	// App Mention Commands
	b.appMentionCommandHandlers[cmdStatus] = (*Bot).handleLunchStatusCommand // Wrapper
	b.appMentionCommandHandlers[cmdAdd] = (*Bot).handleLunchCommand          // Wrapper
	b.appMentionCommandHandlers[cmdDetract] = (*Bot).handleLunchCommand      // Wrapper, uses the same logic
	b.appMentionCommandHandlers[cmdVacation] = (*Bot).handleVacationCommand  // Wrapper

	// Direct Message Commands
	b.dmCommandHandlers[cmdStatus] = (*Bot).handleLunchStatusCommandDM // Wrapper
	b.dmCommandHandlers[cmdAdd] = (*Bot).handleLunchCommandDM          // Wrapper
	b.dmCommandHandlers[cmdDetract] = (*Bot).handleLunchCommandDM      // Wrapper
	b.dmCommandHandlers[cmdVacation] = (*Bot).handleVacationCommandDM  // Wrapper
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
					b.logger.Debug("Ignored an event that wasn't EventsAPIEvent", "event_type", evt.Type)
					continue
				}
				b.socketClient.Ack(*evt.Request) // Ack immediately
				b.handleEvent(eventsAPIEvent)
			case socketmode.EventTypeConnecting:
				b.logger.Info("Socketmode client connecting...")
			case socketmode.EventTypeConnectionError:
				b.logger.Error("Socketmode connection error", "error", evt.Data.(error))
			case socketmode.EventTypeConnected:
				b.logger.Info("Socketmode client connected.")
			default:
				b.logger.Debug("Received unhandled socketmode event", "type", evt.Type)
			}
		}
	}()

	err := b.socketClient.RunContext(ctx)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// context.Canceled or DeadlineExceeded are expected on shutdown
		return fmt.Errorf("socketClient.RunContext failed: %w", err)
	}
	return nil
}

func (b *Bot) StartScheduler(ctx context.Context) {
	location, err := time.LoadLocation("Europe/Oslo") // Make timezone configurable if needed
	if err != nil {
		b.logger.Error("Failed to load timezone 'Europe/Oslo', using UTC", "error", err)
		location = time.UTC
	}

	// Use a ticker that aligns better or calculate duration till next event
	// For simplicity, 1-minute ticker is kept, but be aware of potential drift.
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop() // Ensure ticker is stopped when function exits

	b.logger.Info("Scheduler started", "timezone", location.String())

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("Scheduler stopping due to context cancellation.")
			return
		case <-ticker.C:
			now := time.Now().In(location)
			// Check for daily report at 10:00
			if now.Hour() == 10 && now.Minute() == 0 {
				b.logger.Info("Scheduler: Time for daily report", "time", now)
				b.sendDailyReport()
			}
			// Check for warning at 9:50
			if now.Hour() == 9 && now.Minute() == 50 {
				b.logger.Info("Scheduler: Time for warning", "time", now)
				b.sendWarning()
			}
		}
	}
}

func (b *Bot) handleEvent(event slackevents.EventsAPIEvent) {
	b.logger.Debug("Received Slack API event", "type", event.Type)
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent
		b.logger.Debug("Callback event received", "inner_type", innerEvent.Type)
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			b.logger.Debug("Processing app mention event", "user", ev.User, "channel", ev.Channel)
			b.handleMention(ev)
		case *slackevents.MessageEvent:
			// Handle DMs (im) and Group DMs (mpim)
			if ev.ChannelType == "im" || ev.ChannelType == "mpim" {
				b.logger.Debug("Processing message event (DM/MPIM)", "user", ev.User, "channel", ev.Channel)
				b.handleDirectMessage(ev)
			} else {
				b.logger.Debug("Ignoring non-DM/MPIM message event", "channel_type", ev.ChannelType, "user", ev.User)
			}
		default:
			b.logger.Debug("Unhandled inner event type", "type", fmt.Sprintf("%T", ev))
		}
	default:
		b.logger.Debug("Unhandled outer event type", "type", event.Type)

	}
}

// handleDirectMessage dispatches DM commands using the command registry.
func (b *Bot) handleDirectMessage(event *slackevents.MessageEvent) {
	if event.User == b.botUserID || event.User == "" { // Also check for empty User, sometimes happens for bot messages
		b.logger.Debug("Skipping DM from self or empty user", "user", event.User)
		return
	}
	if event.SubType != "" && event.SubType != "file_share" { // Allow file_share if text might accompany it
		b.logger.Debug("Skipping DM with subtype", "subtype", event.SubType)
		return
	}

	text := strings.TrimSpace(event.Text)
	if text == "" { // Ignore empty messages
		return
	}
	b.logger.Info("Received DM", "user", event.User, "text", text)

	for prefix, handler := range b.dmCommandHandlers {
		if strings.HasPrefix(text, prefix) {
			handler(b, event, text) // Pass the full text; handler will parse its own arguments
			return
		}
	}

	b.logger.Info("Unknown DM command", "user", event.User, "text", text)
	b.sendHelpMessage(event.Channel)
}

// handleMention dispatches mention commands using the command registry.
func (b *Bot) handleMention(event *slackevents.AppMentionEvent) {
	// Optional: Allow mentions only in the configured channel, or make it configurable
	// if event.Channel != b.config.Channel {
	// 	b.logger.Debug("Mention in wrong channel, ignoring", "event_channel", event.Channel, "config_channel", b.config.Channel)
	// 	return
	// }

	text := strings.TrimSpace(event.Text)
	botMention := fmt.Sprintf("<@%s>", b.botUserID)
	processedText := strings.TrimSpace(strings.TrimPrefix(text, botMention))

	if processedText == "" { // Bot was mentioned with no command
		b.sendHelpMessage(event.Channel)
		return
	}

	b.logger.Info("Received mention", "user", event.User, "channel", event.Channel, "processed_text", processedText)

	for prefix, handler := range b.appMentionCommandHandlers {
		if strings.HasPrefix(processedText, prefix) {
			// Pass the text *after* removing the bot mention.
			// The handlers expect text like "add 2 today @user1"
			handler(b, event, processedText)
			return
		}
	}

	b.logger.Info("Unknown mention command", "user", event.User, "processed_text", processedText)
	b.sendHelpMessage(event.Channel)
}

// --- Lunch Command Logic (add/detract) ---

func (b *Bot) handleLunchCommand(event *slackevents.AppMentionEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User, // The user who typed the command
		ChannelID:      event.Channel,
		Text:           text, // e.g., "add 2 today @user1 @user2"
		IsDM:           false,
		RespondingUser: event.User,
	}
	b.logger.Info("Processing lunch command (mention)", "user", input.UserID, "text", input.Text)
	b.processLunchCommandLogic(input)
}

func (b *Bot) handleLunchCommandDM(event *slackevents.MessageEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           true,
		RespondingUser: event.User,
	}
	b.logger.Info("Processing lunch command (DM)", "user", input.UserID, "text", input.Text)
	b.processLunchCommandLogic(input)
}

func (b *Bot) processLunchCommandLogic(input CommandLogicInput) {
	matches := lunchCommandRegex.FindStringSubmatch(input.Text)
	// Expected: matches[0]=full, matches[1]=verb, matches[2]=count, matches[3]=when, matches[4]=participantsStr

	if len(matches) != 5 {
		errorMsg := fmt.Sprintf("❌ Invalid format. Use: `@%s (add|detract) <count> (today|tomorrow) <participants>`", b.botUserID)
		if input.IsDM {
			errorMsg = "❌ Invalid format. Use: `(add|detract) <count> (today|tomorrow) <participants>`"
		}
		b.sendMessage(input.ChannelID, errorMsg)
		return
	}

	verb := matches[1] // "add" or "detract"
	countStr := matches[2]
	when := matches[3] // "today" or "tomorrow"
	participantsStr := strings.TrimSpace(matches[4])

	count, err := strconv.Atoi(countStr)
	if err != nil {
		b.sendMessage(input.ChannelID, fmt.Sprintf("❌ Invalid count '%s'. Must be a number.", countStr))
		b.logger.Warn("Invalid count in lunch command", "count_str", countStr, "user", input.UserID)
		return
	}
	if count <= 0 {
		b.sendMessage(input.ChannelID, "❌ Count must be a positive number.")
		return
	}

	participants := strings.Fields(participantsStr)
	// Validate participants (e.g., ensure they are valid user mentions if necessary)
	// For now, just check count.
	if len(participants) != count {
		b.sendMessage(input.ChannelID, fmt.Sprintf("❌ Number of participants mentioned (%d) doesn't match count (%d).", len(participants), count))
		return
	}

	targetDate := time.Now()
	if when == argTomorrow {
		targetDate = targetDate.AddDate(0, 0, 1)
	}
	dateStr := targetDate.Format(dateFormat)

	err = b.storage.AddLunchRecord(dateStr, input.UserID, verb, count, participants)
	if err != nil {
		b.logger.Error("Failed to insert lunch record", "error", err, "user", input.UserID, "is_dm", input.IsDM)
		b.sendMessage(input.ChannelID, "❌ Failed to record lunch data. Please try again.")
		return
	}

	logAction := "recorded lunch"
	if input.IsDM {
		logAction = "recorded lunch via DM"
	}
	b.logger.Info(fmt.Sprintf("Successfully %s", logAction), "verb", verb, "count", count, "date", dateStr, "user", input.UserID, "participants", participants)

	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total after lunch record", "error", err, "date", dateStr)
		// Non-critical for the user's confirmation, but should be logged.
		// Decide if user should be notified. For now, let's not send another error message if record was saved.
	}

	// The check for total < 0 should ideally be part of the transaction in storage or handled more robustly.
	// If AddLunchRecord could make it negative, it should perhaps error or be handled before this point.
	// For now, we'll proceed with the message based on the potentially calculated total.
	if total < 0 && err == nil { // Only warn if CalculateTotal didn't error but result is negative
		b.logger.Warn("Total calculated is negative, this might indicate an issue.", "total", total, "date", dateStr)
		// b.sendMessage(input.ChannelID, "⚠️ Warning: The new total is negative. Please check the records.")
	}

	confirmationMessage := fmt.Sprintf("Recorded %s %d for %s. Total for %s is now %d.", verb, count, when, dateStr, total)
	if input.IsDM {
		confirmationMessage = "✅ " + confirmationMessage
		// Notify main channel if DM
		channelNotification := fmt.Sprintf("📝 <@%s> used DM to %s %d lunches for %s (%s). New total for %s: %d",
			input.UserID, verb, count, when, strings.Join(participants, ", "), dateStr, total)
		b.sendMessage(b.config.Channel, channelNotification) // Ensure b.config.Channel is the correct main channel ID
	}
	b.sendMessage(input.ChannelID, confirmationMessage)
}

// --- Vacation Command Logic ---

func (b *Bot) handleVacationCommand(event *slackevents.AppMentionEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User, // User who is performing the action
		ChannelID:      event.Channel,
		Text:           text, // e.g., "vacation @targetUser 2024-12-20 2024-12-25"
		IsDM:           false,
		RespondingUser: event.User,
	}
	b.logger.Info("Processing vacation command (mention)", "user", input.UserID, "text", input.Text)
	b.processVacationCommandLogic(input)
}

func (b *Bot) handleVacationCommandDM(event *slackevents.MessageEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           true,
		RespondingUser: event.User,
	}
	b.logger.Info("Processing vacation command (DM)", "user", input.UserID, "text", input.Text)
	b.processVacationCommandLogic(input)
}

func (b *Bot) processVacationCommandLogic(input CommandLogicInput) {
	matches := vacationCommandRegex.FindStringSubmatch(input.Text)
	// Expected: matches[0]=full, matches[1]=targetUserID, matches[2]=fromDate, matches[3]=toDate

	if len(matches) != 4 {
		errorMsg := fmt.Sprintf("Invalid format. Use: `@%s vacation <@user> YYYY-MM-DD YYYY-MM-DD`", b.botUserID)
		if input.IsDM {
			errorMsg = "Invalid format. Use: `vacation <@user> YYYY-MM-DD YYYY-MM-DD`"
		}
		b.sendMessage(input.ChannelID, errorMsg)
		return
	}

	targetUserID := matches[1] // The user going on vacation
	fromDateStr := matches[2]
	toDateStr := matches[3]

	// Validate dates (storage.ValidateDate might not be needed if time.Parse is strict enough)
	fromTime, errFrom := time.Parse(dateFormat, fromDateStr)
	if errFrom != nil {
		b.sendMessage(input.ChannelID, fmt.Sprintf("Invalid 'from' date format: %s. Use YYYY-MM-DD.", fromDateStr))
		return
	}
	toTime, errTo := time.Parse(dateFormat, toDateStr)
	if errTo != nil {
		b.sendMessage(input.ChannelID, fmt.Sprintf("Invalid 'to' date format: %s. Use YYYY-MM-DD.", toDateStr))
		return
	}
	if toTime.Before(fromTime) {
		b.sendMessage(input.ChannelID, "'To' date cannot be before 'from' date.")
		return
	}

	err := b.storage.AddVacationRecord(targetUserID, fromDateStr, toDateStr)
	if err != nil {
		b.logger.Error("Failed to insert vacation record", "error", err, "target_user", targetUserID, "requesting_user", input.UserID)
		b.sendMessage(input.ChannelID, "❌ Failed to record vacation. Please try again.")
		return
	}

	logAction := "recorded vacation"
	if input.IsDM {
		logAction = "recorded vacation via DM"
	}
	b.logger.Info(fmt.Sprintf("Successfully %s", logAction), "target_user", targetUserID, "from", fromDateStr, "to", toDateStr, "requesting_user", input.UserID)

	confirmationMessage := fmt.Sprintf("Recorded vacation for <@%s> from %s to %s.", targetUserID, fromDateStr, toDateStr)
	if input.IsDM {
		confirmationMessage = "✅ " + confirmationMessage
		// Notify main channel if DM
		channelNotification := fmt.Sprintf("🏖️ <@%s> used DM to record vacation for <@%s> from %s to %s.",
			input.RespondingUser, targetUserID, fromDateStr, toDateStr)
		b.sendMessage(b.config.Channel, channelNotification)
	}
	b.sendMessage(input.ChannelID, confirmationMessage)
}

// --- Lunch Status Command Logic ---

func (b *Bot) handleLunchStatusCommand(event *slackevents.AppMentionEvent, text string) {
	b.logger.Info("Processing status command (mention)", "user", event.User, "text", text)
	b.processLunchStatusCommandLogic(event.Channel, text, false, event.User)
}

func (b *Bot) handleLunchStatusCommandDM(event *slackevents.MessageEvent, text string) {
	b.logger.Info("Processing status command (DM)", "user", event.User, "text", text)
	b.processLunchStatusCommandLogic(event.Channel, text, true, event.User)
}

// Helper to parse date input for status commands
func (b *Bot) parseDateInputForStatus(textParts []string, channelIDForErrorMsg string) (targetTime time.Time, displayWhen string, parsedDateStr string, ok bool) {
	displayWhen = argToday  // Default
	targetTime = time.Now() // Default

	if len(textParts) >= 2 { // e.g. "status tomorrow" or "status 2024-01-15"
		dateInput := textParts[1]
		if dateInput == argToday {
			// Defaults are fine
		} else if dateInput == argTomorrow {
			displayWhen = argTomorrow
			targetTime = time.Now().AddDate(0, 0, 1)
		} else {
			// Try to parse as YYYY-MM-DD
			parsedT, err := time.Parse(dateFormat, dateInput)
			if err != nil {
				b.sendMessage(channelIDForErrorMsg, fmt.Sprintf("Invalid date format '%s'. Use 'today', 'tomorrow', or YYYY-MM-DD.", dateInput))
				return time.Time{}, "", "", false // Not OK
			}
			targetTime = parsedT
			displayWhen = dateInput // Use the actual date string for "when"
		}
	}
	parsedDateStr = targetTime.Format(dateFormat)
	return targetTime, displayWhen, parsedDateStr, true // OK
}

func (b *Bot) processLunchStatusCommandLogic(channelID, text string, isDM bool, requestingUser string) {
	parts := strings.Fields(text) // parts[0] is "status"

	_, displayWhen, dateStr, ok := b.parseDateInputForStatus(parts, channelID)
	if !ok {
		return // Error message already sent by parser
	}

	records, err := b.storage.GetLunchRecordsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get lunch records for status", "error", err, "date", dateStr, "user", requestingUser)
		b.sendMessage(channelID, "Failed to retrieve lunch status records.")
		return
	}

	vacationUserIDs, err := b.storage.GetVacationsForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get vacation users for status", "error", err, "date", dateStr, "user", requestingUser)
		b.sendMessage(channelID, "Failed to retrieve vacation users.")
		return
	}

	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total for status", "error", err, "date", dateStr, "user", requestingUser)
		b.sendMessage(channelID, "❌ Failed to calculate total for status.")
		return
	}

	statusReport := b.buildStatusMessage(displayWhen, dateStr, records, vacationUserIDs, total)

	logContext := []interface{}{"date", dateStr, "total", total, "baseline", b.config.Baseline, "changes", len(records), "vacations", len(vacationUserIDs), "user", requestingUser}
	if isDM {
		b.logger.Info("Provided status via DM", logContext...)
	} else {
		b.logger.Info("Provided status", logContext...)
	}
	b.sendMessage(channelID, statusReport)
}

// buildStatusMessage formats the detailed status report.
func (b *Bot) buildStatusMessage(when, dateStr string, records []storage.LunchRecord, vacationUserIDs []string, total int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Lunch status for %s (%s):\n", when, dateStr))
	sb.WriteString(fmt.Sprintf("• Baseline: %d\n", b.config.Baseline))

	if len(records) > 0 {
		sb.WriteString("• Changes:\n")
		for _, record := range records {
			participantsList := strings.Join(record.Participants, ", ")
			verbDisplay := verbSubDisplay // Default to subtracted
			if record.Verb == cmdAdd {
				verbDisplay = verbAddDisplay
			}
			// Clarify who made the change
			sb.WriteString(fmt.Sprintf("  -%s %d by <@%s> (participants: %s)\n", verbDisplay, record.Count, record.UserID, participantsList))
		}
	} else {
		sb.WriteString("• Changes: None\n")
	}

	if len(vacationUserIDs) > 0 {
		vacationNames := make([]string, len(vacationUserIDs))
		for i, userID := range vacationUserIDs {
			// Fetching user info can be slow; consider caching or if just userID is acceptable.
			slackUser, err := b.client.GetUserInfo(userID)
			if err != nil {
				b.logger.Warn("Failed to get user info for status message", "user_id", userID, "error", err)
				vacationNames[i] = fmt.Sprintf("<@%s>", userID) // Fallback to mention
			} else {
				vacationNames[i] = slackUser.RealName // Or slackUser.Name
				if vacationNames[i] == "" {           // Fallback if RealName is empty
					vacationNames[i] = slackUser.Name
				}
			}
		}
		sb.WriteString(fmt.Sprintf("• On vacation (%d): %s\n", len(vacationUserIDs), strings.Join(vacationNames, ", ")))
	} else {
		sb.WriteString("• On vacation: None\n")
	}

	sb.WriteString(fmt.Sprintf("• Total expected for lunch: %d", total))
	return sb.String()
}

// --- Utility and Scheduled Functions ---

func (b *Bot) sendMessage(channel, text string) {
	if channel == "" {
		b.logger.Error("sendMessage called with empty channel ID", "text", text)
		return
	}
	_, _, err := b.client.PostMessage(channel, slack.MsgOptionText(text, false), slack.MsgOptionAsUser(false)) // AsUser(false) is typical for bots
	if err != nil {
		b.logger.Error("Failed to send message", "channel", channel, "error", err, "text", text)
	}
}

func (b *Bot) sendHelpMessage(channelID string) {
	// Add bot user ID to example commands if help is for a mention context (optional)
	// For simplicity, using a generic help message here.
	b.sendMessage(channelID, helpMessage)
}

func (b *Bot) sendDailyReport() {
	location, _ := time.LoadLocation("Europe/Oslo") // Or get from config/scheduler
	if location == nil {
		location = time.UTC
	}
	todayInLocation := time.Now().In(location).Format(dateFormat)

	total, err := b.storage.CalculateTotal(todayInLocation, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total for daily report", "error", err, "date", todayInLocation)
		// Optionally send an error report to an admin channel
		b.sendMessage(b.config.ReportUser, fmt.Sprintf("Error generating daily lunch report for %s: %v", todayInLocation, err))
		return
	}

	b.logger.Info("Sending daily report", "date", todayInLocation, "total", total, "report_user", b.config.ReportUser)
	message := fmt.Sprintf("🗓️ Daily lunch report for %s: %d people expected.", todayInLocation, total)
	b.sendMessage(b.config.ReportUser, message) // ReportUser should be a channel ID or user ID
}

func (b *Bot) sendWarning() {
	if b.config.Channel == "" {
		b.logger.Error("Cannot send warning, main channel (b.config.Channel) not configured.")
		return
	}
	b.logger.Info("Sending lunch order warning", "channel", b.config.Channel)
	b.sendMessage(b.config.Channel, "⚠️ Reminder: Lunch order closes in 10 minutes!")
}
