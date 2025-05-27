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
	cmdWfh         = "wfh"
	argToday       = "today"
	argTomorrow    = "tomorrow"
	verbAddDisplay = " • Added"
	verbSubDisplay = " • Subtracted"

	// helpMessage updated to ensure it reflects all commands and their general syntax.
	// The syntax examples are kept general; specific error messages in handlers
	// will include @BotName for non-DM contexts.
	helpMessage = "Available commands:\n" +
		"• `(add|detract) <count> [today|tomorrow|YYYY-MM-DD] <participants>` (e.g., `add 2 today @user1 @user2`)\n" +
		"• `status [today|tomorrow|YYYY-MM-DD]` (e.g., `status tomorrow`)\n" +
		"• `vacation <@user> YYYY-MM-DD YYYY-MM-DD` (e.g., `vacation @user1 2024-12-20 2024-12-25`)\n" +
		"• `wfh [today|tomorrow|YYYY-MM-DD]` (e.g., `wfh tomorrow`) \n" +
		"Source available at https://github.com/perbu/lunchbot"
)

// Pre-compiled regexes for command parsing
var (
	lunchCommandRegex    = regexp.MustCompile(`^(add|detract)\s+(\d+)(?:\s+(today|tomorrow|\d{4}-\d{2}-\d{2}))?\s+(.+)`)
	vacationCommandRegex = regexp.MustCompile(`^vacation\s+<@([^>]+)>\s+(\d{4}-\d{2}-\d{2})\s+(\d{4}-\d{2}-\d{2})`)
	wfhCommandRegex      = regexp.MustCompile(`^wfh(?:\s+(today|tomorrow|\d{4}-\d{2}-\d{2}))?$`)
	// statusCommandRegex is added for parsing the status command.
	// It captures the optional date argument (today, tomorrow, or YYYY-MM-DD).
	statusCommandRegex = regexp.MustCompile(`^status(?:\s+(today|tomorrow|\d{4}-\d{2}-\d{2}))?$`)
)

// CommandLogicInput holds the necessary details for processing a command's core logic.
type CommandLogicInput struct {
	UserID         string
	ChannelID      string
	Text           string // The command text itself (e.g., "add 2 today @user1")
	IsDM           bool
	RespondingUser string // User who initiated the command
	BotUserID      string // Added to allow consistent error messages
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
	store, err := storage.NewStorage(cfg.DBPath, logger)
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
	b.appMentionCommandHandlers[cmdStatus] = (*Bot).handleLunchStatusCommand
	b.appMentionCommandHandlers[cmdAdd] = (*Bot).handleLunchCommand
	b.appMentionCommandHandlers[cmdDetract] = (*Bot).handleLunchCommand
	b.appMentionCommandHandlers[cmdVacation] = (*Bot).handleVacationCommand
	b.appMentionCommandHandlers[cmdWfh] = (*Bot).handleWfhCommand

	// Direct Message Commands
	b.dmCommandHandlers[cmdStatus] = (*Bot).handleLunchStatusCommandDM
	b.dmCommandHandlers[cmdAdd] = (*Bot).handleLunchCommandDM
	b.dmCommandHandlers[cmdDetract] = (*Bot).handleLunchCommandDM
	b.dmCommandHandlers[cmdVacation] = (*Bot).handleVacationCommandDM
	b.dmCommandHandlers[cmdWfh] = (*Bot).handleWfhCommandDM
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
		return fmt.Errorf("socketClient.RunContext failed: %w", err)
	}
	return nil
}

func (b *Bot) StartScheduler(ctx context.Context) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		b.logger.Error("Failed to load timezone 'Europe/Oslo', using UTC", "error", err)
		location = time.UTC
	}
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	b.logger.Info("Scheduler started", "timezone", location.String())

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("Scheduler stopping due to context cancellation.")
			return
		case <-ticker.C:
			now := time.Now().In(location)
			if now.Hour() == 10 && now.Minute() == 0 {
				b.logger.Info("Scheduler: Time for daily report", "time", now)
				b.sendDailyReport()
			}
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
			if ev.ChannelType == "im" || ev.ChannelType == "mpim" {
				b.logger.Debug("Processing message event (DM/MPIM)", "user", ev.User, "channel", ev.Channel)
				b.handleDirectMessage(ev)
			} else {
				b.logger.Debug("Ignoring non-DM/MPIM message event", "channel_type", ev.ChannelType, "user", ev.User)
			}
		case *slackevents.AppHomeOpenedEvent:
			b.logger.Debug("Processing App Home opened event", "user", ev.User, "tab", ev.Tab, "view_id", ev.View) // Added more event details to log
			// Call the new handler function
			b.handleAppHomeOpened(ev)
		default:
			b.logger.Debug("Unhandled inner event type", "type", fmt.Sprintf("%T", ev))
		}
	default:
		b.logger.Debug("Unhandled outer event type", "type", event.Type)
	}
}

func (b *Bot) handleDirectMessage(event *slackevents.MessageEvent) {
	if event.User == b.botUserID || event.User == "" {
		b.logger.Debug("Skipping DM from self or empty user", "user", event.User)
		return
	}
	if event.SubType != "" && event.SubType != "file_share" {
		b.logger.Debug("Skipping DM with subtype", "subtype", event.SubType)
		return
	}

	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}
	b.logger.Info("Received DM", "user", event.User, "text", text)

	for prefix, handler := range b.dmCommandHandlers {
		if strings.HasPrefix(text, prefix) {
			handler(b, event, text)
			return
		}
	}

	b.logger.Info("Unknown DM command", "user", event.User, "text", text)
	b.sendHelpMessage(event.Channel)
}

func (b *Bot) handleMention(event *slackevents.AppMentionEvent) {
	text := strings.TrimSpace(event.Text)
	botMention := fmt.Sprintf("<@%s>", b.botUserID)
	processedText := strings.TrimSpace(strings.TrimPrefix(text, botMention))

	if processedText == "" {
		b.sendHelpMessage(event.Channel)
		return
	}

	b.logger.Info("Received mention", "user", event.User, "channel", event.Channel, "processed_text", processedText)

	for prefix, handler := range b.appMentionCommandHandlers {
		if strings.HasPrefix(processedText, prefix) {
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
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           false,
		RespondingUser: event.User,
		BotUserID:      b.botUserID, // Pass BotUserID for consistent error messages
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
		BotUserID:      b.botUserID, // Pass BotUserID
	}
	b.logger.Info("Processing lunch command (DM)", "user", input.UserID, "text", input.Text)
	b.processLunchCommandLogic(input)
}

func (b *Bot) processLunchCommandLogic(input CommandLogicInput) {
	matches := lunchCommandRegex.FindStringSubmatch(input.Text)
	// Expected: matches[0]=full, matches[1]=verb, matches[2]=count, matches[3]=when (optional), matches[4]=participantsStr

	// Standardized check for regex match failure
	if matches == nil {
		var errorMsg string
		if input.IsDM {
			errorMsg = "❌ Invalid format. Use: `(add|detract) <count> [today|tomorrow|YYYY-MM-DD] <participants>`"
		} else {
			errorMsg = fmt.Sprintf("❌ Invalid format. Use: `@%s (add|detract) <count> [today|tomorrow|YYYY-MM-DD] <participants>`", input.BotUserID)
		}
		b.sendMessage(input.ChannelID, errorMsg)
		return
	}

	verb := matches[1]
	countStr := matches[2]
	when := matches[3] // This will be an empty string if the optional date part is not provided
	participantsStr := strings.TrimSpace(matches[4])

	if when == "" {
		when = argToday
	}

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
	if len(participants) != count {
		b.sendMessage(input.ChannelID, fmt.Sprintf("❌ Number of participants mentioned (%d) doesn't match count (%d).", len(participants), count))
		return
	}

	var targetDate time.Time
	var displayWhen string

	if when == argToday {
		targetDate = time.Now()
		displayWhen = argToday
	} else if when == argTomorrow {
		targetDate = time.Now().AddDate(0, 0, 1)
		displayWhen = argTomorrow
	} else {
		parsedTime, err := time.Parse(dateFormat, when)
		if err != nil {
			b.sendMessage(input.ChannelID, fmt.Sprintf("❌ Invalid date format '%s'. Use 'today', 'tomorrow', or YYYY-MM-DD.", when))
			return
		}
		targetDate = parsedTime
		displayWhen = when
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
	}

	if total < 0 && err == nil {
		b.logger.Warn("Total calculated is negative, this might indicate an issue.", "total", total, "date", dateStr)
	}

	confirmationMessage := fmt.Sprintf("Recorded %s %d for %s. Total for %s is now %d.", verb, count, displayWhen, dateStr, total)
	if input.IsDM {
		confirmationMessage = "✅ " + confirmationMessage
		channelNotification := fmt.Sprintf("📝 <@%s> used DM to %s %d lunches for %s (%s). New total for %s: %d",
			input.UserID, verb, count, displayWhen, strings.Join(participants, ", "), dateStr, total)
		b.sendMessage(b.config.Channel, channelNotification)
	}
	b.sendMessage(input.ChannelID, confirmationMessage)
}

// --- Vacation Command Logic ---

func (b *Bot) handleVacationCommand(event *slackevents.AppMentionEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           false,
		RespondingUser: event.User,
		BotUserID:      b.botUserID, // Pass BotUserID
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
		BotUserID:      b.botUserID, // Pass BotUserID
	}
	b.logger.Info("Processing vacation command (DM)", "user", input.UserID, "text", input.Text)
	b.processVacationCommandLogic(input)
}

func (b *Bot) processVacationCommandLogic(input CommandLogicInput) {
	matches := vacationCommandRegex.FindStringSubmatch(input.Text)
	// Expected: matches[0]=full, matches[1]=targetUserID, matches[2]=fromDate, matches[3]=toDate

	// Standardized check for regex match failure.
	// For this regex, a match always yields 4 elements (full + 3 groups).
	// So, `matches == nil` is the primary check.
	if matches == nil {
		var errorMsg string
		if input.IsDM {
			errorMsg = "Invalid format. Use: `vacation <@user> YYYY-MM-DD YYYY-MM-DD`"
		} else {
			errorMsg = fmt.Sprintf("Invalid format. Use: `@%s vacation <@user> YYYY-MM-DD YYYY-MM-DD`", input.BotUserID)
		}
		b.sendMessage(input.ChannelID, errorMsg)
		return
	}

	targetUserID := matches[1]
	fromDateStr := matches[2]
	toDateStr := matches[3]

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
		channelNotification := fmt.Sprintf("🏖️ <@%s> used DM to record vacation for <@%s> from %s to %s.",
			input.RespondingUser, targetUserID, fromDateStr, toDateStr)
		b.sendMessage(b.config.Channel, channelNotification)
	}
	b.sendMessage(input.ChannelID, confirmationMessage)
}

// --- WFH Command Logic ---

func (b *Bot) handleWfhCommand(event *slackevents.AppMentionEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           false,
		RespondingUser: event.User,
		BotUserID:      b.botUserID, // Pass BotUserID
	}
	b.logger.Info("Processing WFH command (mention)", "user", input.UserID, "text", input.Text)
	b.processWfhCommandLogic(input)
}

func (b *Bot) handleWfhCommandDM(event *slackevents.MessageEvent, text string) {
	input := CommandLogicInput{
		UserID:         event.User,
		ChannelID:      event.Channel,
		Text:           text,
		IsDM:           true,
		RespondingUser: event.User,
		BotUserID:      b.botUserID, // Pass BotUserID
	}
	b.logger.Info("Processing WFH command (DM)", "user", input.UserID, "text", input.Text)
	b.processWfhCommandLogic(input)
}

func (b *Bot) processWfhCommandLogic(input CommandLogicInput) {
	matches := wfhCommandRegex.FindStringSubmatch(input.Text)
	// Expected: matches[0]=full, matches[1]=date (optional: today, tomorrow, or YYYY-MM-DD)

	// Standardized check for regex match failure
	if matches == nil {
		var errorMsg string
		if input.IsDM {
			errorMsg = "❌ Invalid format. Use: `wfh [today|tomorrow|YYYY-MM-DD]`"
		} else {
			errorMsg = fmt.Sprintf("❌ Invalid format. Use: `@%s wfh [today|tomorrow|YYYY-MM-DD]`", input.BotUserID)
		}
		b.sendMessage(input.ChannelID, errorMsg)
		return
	}

	when := matches[1] // This will be empty if date part is not provided
	if when == "" {
		when = argToday
	}

	var targetDate time.Time
	var displayWhen string

	if when == argToday {
		targetDate = time.Now()
		displayWhen = argToday
	} else if when == argTomorrow {
		targetDate = time.Now().AddDate(0, 0, 1)
		displayWhen = argTomorrow
	} else {
		parsedTime, err := time.Parse(dateFormat, when)
		if err != nil {
			b.sendMessage(input.ChannelID, fmt.Sprintf("❌ Invalid date format '%s'. Use 'today', 'tomorrow', or YYYY-MM-DD.", when))
			return
		}
		targetDate = parsedTime
		displayWhen = when
	}

	dateStr := targetDate.Format(dateFormat)

	err := b.storage.AddWfhRecord(input.UserID, dateStr)
	if err != nil {
		b.logger.Error("Failed to insert WFH record", "error", err, "user", input.UserID, "is_dm", input.IsDM)
		b.sendMessage(input.ChannelID, "❌ Failed to record WFH status. Please try again.")
		return
	}

	logAction := "recorded WFH"
	if input.IsDM {
		logAction = "recorded WFH via DM"
	}
	b.logger.Info(fmt.Sprintf("Successfully %s", logAction), "date", dateStr, "user", input.UserID)

	confirmationMessage := fmt.Sprintf("✅ <@%s> set as working from home for %s (%s).", input.UserID, displayWhen, dateStr)
	if input.IsDM {
		channelNotification := fmt.Sprintf("🏠 <@%s> used DM to set WFH status for %s (%s).",
			input.UserID, displayWhen, dateStr)
		b.sendMessage(b.config.Channel, channelNotification)
	}
	b.sendMessage(input.ChannelID, confirmationMessage)
}

// --- Lunch Status Command Logic ---

func (b *Bot) handleLunchStatusCommand(event *slackevents.AppMentionEvent, text string) {
	b.logger.Info("Processing status command (mention)", "user", event.User, "text", text)
	// Pass b.botUserID for consistent error message formatting
	b.processLunchStatusCommandLogic(event.Channel, text, false, event.User, b.botUserID)
}

func (b *Bot) handleLunchStatusCommandDM(event *slackevents.MessageEvent, text string) {
	b.logger.Info("Processing status command (DM)", "user", event.User, "text", text)
	// Pass b.botUserID (though not used in DM error message, good for signature consistency)
	b.processLunchStatusCommandLogic(event.Channel, text, true, event.User, b.botUserID)
}

// processLunchStatusCommandLogic now uses regex for parsing and has botUserID for error messages.
// The helper parseDateInputForStatus is removed.
func (b *Bot) processLunchStatusCommandLogic(channelID, text string, isDM bool, requestingUser string, botUserID string) {
	matches := statusCommandRegex.FindStringSubmatch(text)
	// Expected: matches[0]=full, matches[1]=date (optional: today, tomorrow, or YYYY-MM-DD)

	if matches == nil {
		var errorMsg string
		if isDM {
			errorMsg = "❌ Invalid format. Use: `status [today|tomorrow|YYYY-MM-DD]`"
		} else {
			errorMsg = fmt.Sprintf("❌ Invalid format. Use: `@%s status [today|tomorrow|YYYY-MM-DD]`", botUserID)
		}
		b.sendMessage(channelID, errorMsg)
		return
	}

	whenArg := argToday                       // Default to today
	if len(matches) > 1 && matches[1] != "" { // matches[1] contains the date argument if provided
		whenArg = matches[1]
	}

	var targetTime time.Time
	var displayWhen string

	if whenArg == argToday {
		targetTime = time.Now()
		displayWhen = argToday
	} else if whenArg == argTomorrow {
		targetTime = time.Now().AddDate(0, 0, 1)
		displayWhen = argTomorrow
	} else { // Must be YYYY-MM-DD due to regex
		parsedT, err := time.Parse(dateFormat, whenArg)
		if err != nil {
			// This should ideally not happen if regex is correct and captures a valid format string.
			// However, time.Parse can still fail for invalid dates like "2023-02-30".
			b.logger.Error("Date matched regex but failed to parse", "date_arg", whenArg, "error", err, "user", requestingUser)
			b.sendMessage(channelID, fmt.Sprintf("❌ Invalid date value '%s'. Please use a correct YYYY-MM-DD date.", whenArg))
			return
		}
		targetTime = parsedT
		displayWhen = whenArg
	}
	dateStr := targetTime.Format(dateFormat)

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

	wfhUserIDs, err := b.storage.GetWfhForDate(dateStr)
	if err != nil {
		b.logger.Error("Failed to get WFH users for status", "error", err, "date", dateStr, "user", requestingUser)
		b.sendMessage(channelID, "Failed to retrieve WFH users.")
		return
	}

	total, err := b.storage.CalculateTotal(dateStr, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total for status", "error", err, "date", dateStr, "user", requestingUser)
		b.sendMessage(channelID, "❌ Failed to calculate total for status.")
		return
	}

	statusReport := b.buildStatusMessage(displayWhen, dateStr, records, vacationUserIDs, wfhUserIDs, total)

	logContext := []interface{}{"date", dateStr, "total", total, "baseline", b.config.Baseline, "changes", len(records), "vacations", len(vacationUserIDs), "wfh", len(wfhUserIDs), "user", requestingUser}
	if isDM {
		b.logger.Info("Provided status via DM", logContext...)
	} else {
		b.logger.Info("Provided status", logContext...)
	}
	b.sendMessage(channelID, statusReport)
}

// buildStatusMessage formats the detailed status report.
func (b *Bot) buildStatusMessage(when, dateStr string, records []storage.LunchRecord, vacationUserIDs []string, wfhUserIDs []string, total int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Lunch status for %s (%s):\n", when, dateStr))
	sb.WriteString(fmt.Sprintf("• Baseline: %d\n", b.config.Baseline))

	if len(records) > 0 {
		sb.WriteString("• Changes:\n")
		for _, record := range records {
			participantsList := strings.Join(record.Participants, ", ")
			verbDisplay := verbSubDisplay
			if record.Verb == cmdAdd {
				verbDisplay = verbAddDisplay
			}
			sb.WriteString(fmt.Sprintf("  -%s %d by <@%s> (participants: %s)\n", verbDisplay, record.Count, record.UserID, participantsList))
		}
	} else {
		sb.WriteString("• Changes: None\n")
	}

	if len(vacationUserIDs) > 0 {
		vacationNames := make([]string, len(vacationUserIDs))
		for i, userID := range vacationUserIDs {
			slackUser, err := b.client.GetUserInfo(userID)
			if err != nil {
				b.logger.Warn("Failed to get user info for status message", "user_id", userID, "error", err)
				vacationNames[i] = fmt.Sprintf("<@%s>", userID)
			} else {
				vacationNames[i] = slackUser.RealName
				if vacationNames[i] == "" {
					vacationNames[i] = slackUser.Name
				}
			}
		}
		sb.WriteString(fmt.Sprintf("• On vacation (%d): %s\n", len(vacationUserIDs), strings.Join(vacationNames, ", ")))
	} else {
		sb.WriteString("• On vacation: None\n")
	}

	if len(wfhUserIDs) > 0 {
		wfhNames := make([]string, len(wfhUserIDs))
		for i, userID := range wfhUserIDs {
			slackUser, err := b.client.GetUserInfo(userID)
			if err != nil {
				b.logger.Warn("Failed to get user info for WFH status message", "user_id", userID, "error", err)
				wfhNames[i] = fmt.Sprintf("<@%s>", userID)
			} else {
				wfhNames[i] = slackUser.RealName
				if wfhNames[i] == "" {
					wfhNames[i] = slackUser.Name
				}
			}
		}
		sb.WriteString(fmt.Sprintf("• Working from home (%d): %s\n", len(wfhUserIDs), strings.Join(wfhNames, ", ")))
	} else {
		sb.WriteString("• Working from home: None\n")
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
	_, _, err := b.client.PostMessage(channel, slack.MsgOptionText(text, false), slack.MsgOptionAsUser(false))
	if err != nil {
		b.logger.Error("Failed to send message", "channel", channel, "error", err, "text", text)
	}
}

func (b *Bot) sendHelpMessage(channelID string) {
	b.sendMessage(channelID, helpMessage)
}

func (b *Bot) sendDailyReport() {
	location, _ := time.LoadLocation("Europe/Oslo")
	if location == nil {
		location = time.UTC
	}
	todayInLocation := time.Now().In(location).Format(dateFormat)

	total, err := b.storage.CalculateTotal(todayInLocation, b.config.Baseline)
	if err != nil {
		b.logger.Error("Failed to calculate total for daily report", "error", err, "date", todayInLocation)
		b.sendMessage(b.config.ReportUser, fmt.Sprintf("Error generating daily lunch report for %s: %v", todayInLocation, err))
		return
	}

	b.logger.Info("Sending daily report", "date", todayInLocation, "total", total, "report_user", b.config.ReportUser)
	message := fmt.Sprintf("🗓️ Daily lunch report for %s: %d people expected.", todayInLocation, total)
	b.sendMessage(b.config.ReportUser, message)
}

func (b *Bot) sendWarning() {
	if b.config.Channel == "" {
		b.logger.Error("Cannot send warning, main channel (b.config.Channel) not configured.")
		return
	}
	b.logger.Info("Sending lunch order warning", "channel", b.config.Channel)
	b.sendMessage(b.config.Channel, "⚠️ Reminder: Lunch order closes in 10 minutes!")
}

// handleAppHomeOpened constructs and publishes the App Home view.
func (b *Bot) handleAppHomeOpened(event *slackevents.AppHomeOpenedEvent) {
	b.logger.Info("App Home opened by user - this is not supported", "user", event.User)

}
