# Lunchbot

A Slack bot written in Go that helps track lunch orders and participant counts for your team.

## Features

- **Lunch tracking**: Add or subtract participants for today or tomorrow
- **Vacation management**: Track team members on vacation (automatically reduces daily count)
- **Daily reports**: Automatic lunch count reports sent at 10:00 AM (Europe/Oslo time)
- **Order reminders**: Warning messages sent 10 minutes before order deadline
- **Baseline configuration**: Set a default daily lunch count
- **SQLite storage**: Persistent data storage with automatic database setup

## Installation

1. Clone the repository:
   ```bash
   git clone https://github.com/perbu/lunchbot.git
   cd lunchbot
   ```

2. Build the application:
   ```bash
   go build
   ```

3. Create your configuration file:
   ```bash
   cp config.yaml.example config.yaml
   ```

4. Configure your Slack app tokens and settings in `config.yaml`

## Configuration

Create a `config.yaml` file in the project root with the following structure:

```yaml
slack_app_token: "xapp-your-app-token"
slack_bot_token: "xoxb-your-bot-token"
channel: "C1234567890"          # Slack channel ID where the bot listens
report_user: "U0987654321"      # Slack user ID who receives daily reports
baseline: 10                    # Default number of lunches per day
db_path: "lunchbot.db"         # Path to SQLite database file
```

### Slack App Setup

1. Create a new Slack app at https://api.slack.com/apps
2. Enable Socket Mode and generate an App-Level Token
3. Add the following bot token scopes:
   - `app_mentions:read`
   - `chat:write`
   - `channels:read`
4. Install the app to your workspace
5. Copy the Bot User OAuth Token and App-Level Token to your config

## Usage

### Commands

Mention the bot in your configured channel with these commands:

#### Lunch Management
```
@lunchbot lunch add 3 today @alice @bob @charlie
@lunchbot lunch detract 2 tomorrow @dave @eve
```

- **Verb**: `add` or `detract`
- **Count**: Number of participants (must match actual participant count)
- **When**: `today` or `tomorrow`
- **Participants**: Space-separated list of participants

#### Vacation Management
```
@lunchbot vacation @alice 2024-12-25 2024-12-31
```

- **User**: Slack user mention
- **Dates**: Start and end dates in YYYY-MM-DD format (inclusive)

### Automated Features

- **Daily reports**: Sent at 10:00 AM Europe/Oslo time to the configured report user
- **Order warnings**: Sent at 9:50 AM Europe/Oslo time to the main channel
- **Vacation deductions**: Automatically subtracts 1 from daily count for each person on vacation

## Running

```bash
./lunchbot
```

The bot will:
1. Load configuration from `config.yaml`
2. Initialize the SQLite database
3. Connect to Slack via Socket Mode
4. Start listening for mentions and running scheduled tasks

Press `Ctrl+C` to gracefully shutdown.

## Architecture

The project is organized into separate packages:

- `config/` - Configuration loading and types
- `storage/` - Database operations and data models
- `bot/` - Slack bot logic and command handling
- `main.go` - Application entry point

## Requirements

- Go 1.24.2+
- Slack workspace with admin permissions to create apps
- Network access to Slack's API

## License

MIT License