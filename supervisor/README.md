# Factorio Supervisor

A single static Go binary that runs as the parent process of the Factorio
dedicated server inside the container. It replaces the previous mix of
`entrypoint.sh` and Python scripts.

## Responsibilities

- **Process supervision** — launches the base image's `/docker-entrypoint.sh`
  (which execs the Factorio binary), tracks it, and shuts it down gracefully on
  `SIGTERM`/`SIGINT` (triggering a Factorio autosave, then uploading saves).
- **Stdio capture** — every line of the server's stdout/stderr is mirrored to
  the container's own stdout (so `fly logs` keeps working) **and** pushed into an
  in-memory log bus that the web interface streams live.
- **RCON** — a dependency-free Source RCON client used for the player list and
  the web console.
- **Discord** — webhook embeds for startup, shutdown, and player join/leave/
  status.
- **Saves** — upload/download to S3-compatible storage (canonical +
  timestamped backup + `_autosave1`), plus periodic uploads while running.
- **Mods** — resolves the latest release of each public mod from the portal,
  caches zips in S3, copies vendored private mods, and writes `mod-list.json`.

## Subcommands

```
factorio-supervisor run          # launch and supervise the server (default)
factorio-supervisor sync-mods    # assemble /factorio/mods and exit
factorio-supervisor upload       # upload saves to S3 and exit
factorio-supervisor download     # download saves from S3 and exit
```

## Web interface

Enabled by default (`WEB_ENABLED=true`) on `WEB_ADDR` (`:8080`):

| Endpoint       | Description                                            |
| -------------- | ------------------------------------------------------ |
| `GET /healthz` | Liveness + server name.                                |
| `GET /logs`    | JSON snapshot of the retained log ring buffer.         |
| `GET /logs/stream` | Server-Sent Events: backlog then live log lines.   |
| `GET /players` | Current online players (via RCON).                     |
| `POST /rcon`   | Run an RCON command: `{"command": "/players online"}`. |

## Configuration

All configuration is via environment variables (see `internal/config`). Key
ones: `SERVER_NAME`, `RCON_HOST`/`RCON_PORT`/`RCON_PASSWORD`, `DISCORD_WEBHOOK`,
`DISCORD_NOTIFICATIONS`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_ENDPOINT_URL_S3`/`AWS_BUCKET_NAME`, `SAVE_NAME`, `FACTORIO_USERNAME`/
`FACTORIO_TOKEN` (falls back to `server-settings.json`), `MONITOR_INTERVAL`,
`WEB_ENABLED`/`WEB_ADDR`.

## Development

```
go test ./...
go build ./cmd/supervisor
```
