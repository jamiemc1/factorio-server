# Discord Integration Setup Guide

This guide explains how to configure Discord notifications for your Factorio server.

## Features

The Discord integration provides:

1. **Server Startup Notification** - Alerts when the server comes online
2. **Server Shutdown Notification** - Alerts when the server shuts down
3. **Player Status Updates** - Periodic updates showing who is online
4. **Player Join/Leave Events** - Individual notifications when players join or leave

## Prerequisites

1. A Discord server where you have admin permissions
2. A Discord webhook URL

## Setup Steps

### 1. Create a Discord Webhook

1. Open your Discord server
2. Go to Server Settings → Integrations → Webhooks
3. Click "New Webhook"
4. Give it a name (e.g., "Factorio Server")
5. Select the channel where you want notifications
6. Copy the webhook URL

### 2. Configure Environment Variables

You need to set the following environment variables in your `.fly/secrets.dev` file or Fly.io secrets:

```bash
# Required: Discord webhook URL
DISCORD_WEBHOOK=https://discord.com/api/webhooks/...

# Required: RCON password for player monitoring
RCON_PASSWORD=your_secure_password

# Optional: Custom server name (defaults to "Factorio Server")
SERVER_NAME=Going Big
```

### 3. Update RCON Password

Update the RCON password in `config/server-settings.json`:

```json
{
  "rcon": {
    "port": 27015,
    "password": "your_secure_password"
  }
}
```

**Important:** The password in `server-settings.json` must match the `RCON_PASSWORD` environment variable.

### 4. Deploy

After configuration, deploy your server:

```bash
./scripts/launch_factorio.sh
```

Or if already deployed, update secrets and redeploy:

```bash
cat .fly/secrets.dev | fly secrets import
fly deploy .
```

## Configuration Options

### Player Monitor Settings

The player monitor runs as a background service and can be configured in `scripts/entrypoint.sh`:

- `--interval`: Seconds between player checks (default: 300 = 5 minutes)
- `--no-join-leave`: Disable individual join/leave notifications
- `--no-status`: Disable periodic status updates

Example to check every 10 minutes:
```bash
$POETRY_BIN run $PYTHON_BIN /factorio/scripts/player_monitor.py --interval 600 &
```

### Notification Types

You can customize which notifications are sent by modifying the player monitor startup in `scripts/entrypoint.sh`:

```bash
# Only periodic status updates (no join/leave)
player_monitor.py --interval 300 --no-join-leave

# Only join/leave notifications (no periodic status)
player_monitor.py --interval 300 --no-status
```

## Architecture

The Discord integration consists of:

1. **discord_notifier.py** - Core notification module with reusable methods
2. **factorio_rcon.py** - RCON client for communicating with Factorio server
3. **player_monitor.py** - Background service that monitors player activity
4. **startup_notify.py** - Sends notification when server starts
5. **shutdown_notify.py** - Sends notification when server shuts down
6. **entrypoint.sh** - Orchestrates all components during server lifecycle

This modular design makes it easy to extend with additional features like Discord bot commands for server control.

## Troubleshooting

### No notifications are sent

1. Check that `DISCORD_WEBHOOK` environment variable is set correctly
2. Verify the webhook URL is valid in Discord
3. Check server logs for error messages

### Player monitoring not working

1. Ensure RCON is enabled in `server-settings.json`
2. Verify `RCON_PASSWORD` matches in both server settings and environment
3. Check that port 27015 is not blocked
4. The player monitor waits 30 seconds after server start before beginning checks

### Connection refused errors

The player monitor may log connection errors during the first 30 seconds as it waits for the Factorio server to fully start. This is normal and will resolve once the server is ready.

## Future Enhancements

The current implementation provides a foundation for:

- Discord bot commands to start/stop the server
- Custom notification triggers based on game events
- Admin commands via Discord
- Player statistics and analytics

The modular architecture makes these features straightforward to add when needed.
