# factorio-server

Hosting a Factorio server with Discord integration.

## Features

- Automated server deployment on Fly.io
- S3-based save file backup and restore
- Discord notifications for server events and player activity
- RCON support for server management

## Discord Integration

The server includes Discord webhook integration for real-time notifications:

- Server startup and shutdown alerts
- Periodic player status updates
- Player join/leave notifications

See [DISCORD_SETUP.md](DISCORD_SETUP.md) for configuration instructions.

## Quick Start

1. Configure your Discord webhook and RCON password in `.fly/secrets.dev`
2. Update RCON settings in `config/server-settings.json`
3. Deploy: `./scripts/launch_factorio.sh`
