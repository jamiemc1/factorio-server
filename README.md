# factorio-server

Hosting a Factorio server with Discord integration.

## Features

- Automated server deployment on Fly.io
- Self-installing Factorio headless build — no base image to keep in step
- A persistent volume holding the game, mods and saves, with S3 as a cold-start
  cache
- S3-based save file backup and restore
- Discord notifications for server events and player activity
- RCON support for server management

## Architecture

A single Go binary, the [supervisor](supervisor/), is the container's entrypoint.
On boot it installs Factorio onto the `/factorio` volume, seeds config and mod
defaults baked into the image, syncs mods, launches the server, and uploads
saves on shutdown. See [supervisor/README.md](supervisor/README.md) for the
storage layout and the full environment variable list.

## Discord Integration

The server includes Discord webhook integration for real-time notifications:

- Server startup and shutdown alerts
- Periodic player status updates
- Player join/leave notifications

See [DISCORD_SETUP.md](DISCORD_SETUP.md) for configuration instructions.

## Quick Start

1. Set the secrets Fly injects at runtime:
   ```
   fly secrets set SERVER_NAME=... RCON_PASSWORD=... DISCORD_WEBHOOK=... \
     AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
     AWS_ENDPOINT_URL_S3=... AWS_BUCKET_NAME=...
   ```
2. Edit `config/server-settings.json` and `mods/public.json` — both are baked
   into the image, so changing them means redeploying.
3. Deploy: `fly deploy` (pushing to `main` does this via GitHub Actions). The
   10GB volume declared in `fly.toml` is created on the first deploy.

## Running locally

```
docker build -t factorio-local .
docker run --rm --env-file .env -v "$PWD/tmp/volume:/factorio" \
  -p 34197:34197/udp -p 8080:8080 factorio-local
```

The bind mount plays the role of the Fly volume: the first run downloads the
game and mods, later runs reuse them.
