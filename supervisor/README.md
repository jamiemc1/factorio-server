# Factorio Supervisor

A single static Go binary that is the container's only entrypoint. It installs
Factorio, prepares the data volume, and runs the dedicated server as its child.
It replaces both the previous `entrypoint.sh` + Python scripts and the
`factoriotools/factorio` base image's `/docker-entrypoint.sh`.

## Responsibilities

- **Game install** — downloads the Factorio headless build onto the data volume
  (`/factorio/game/<version>`, with a `current` symlink) and verifies its
  sha256. `FACTORIO_VERSION` takes an exact `x.y.z` or the channel keywords
  `experimental` / `stable`. On a warm volume this is a single stat call.
- **Volume seeding** — creates the directory tree and lays the image's baked
  `/opt/defaults/{config,mods}` over it on every boot, so `server-settings.json`
  and `mods/public.json` stay image-authoritative.
- **Server launch** — writes `config.ini`, clears partial saves, generates a map
  when there is nothing to load, and assembles the server flags.
- **Process supervision** — launches the Factorio binary, tracks it, and shuts
  it down gracefully on `SIGTERM`/`SIGINT` (triggering a Factorio autosave, then
  uploading saves).
- **Stdio capture** — every line of the server's stdout/stderr is mirrored to
  the container's own stdout (so `fly logs` keeps working) **and** pushed into an
  in-memory log bus that the web interface streams live.
- **RCON** — a dependency-free Source RCON client used for the player list and
  the web console.
- **Discord** — webhook embeds for startup, shutdown, and player join/leave/
  status.
- **Saves** — upload/download to S3-compatible storage (canonical +
  timestamped backup + `_autosave1`), plus periodic uploads while running. On
  boot the S3 copy is pulled only when it is newer than the volume's.
- **Mods** — resolves the latest release of each public mod from the portal,
  fetches only the zips the volume is missing, caches them in S3, copies
  vendored private mods, and writes `mod-list.json` (including the
  `DLC_SPACE_AGE` toggles that `docker-dlc.sh` used to apply).

## Storage layout

Everything lives under `/factorio`, which on Fly is a persistent volume. S3 is
therefore a cold-start cache: a machine that keeps its volume re-downloads
nothing.

```
/factorio/game/<version>/   extracted headless build (bin/x64/factorio, data/)
/factorio/game/current      symlink -> <version>
/factorio/config/           server-settings.json, map-*.json, rconpw, config.ini
/factorio/saves/            _autosave1.zip, ...
/factorio/mods/             *.zip + mod-list.json (the durable mod cache)
/factorio/mods/private/     vendored private zips
```

## Subcommands

```
factorio-supervisor run           # launch and supervise the server (default)
factorio-supervisor install-game  # install the Factorio headless build and exit
factorio-supervisor sync-mods     # assemble /factorio/mods and exit
factorio-supervisor upload        # upload saves to S3 and exit
factorio-supervisor download      # download saves from S3 and exit
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
ones: `SERVER_NAME`, `RCON_HOST`/`RCON_PORT`/`RCON_PASSWORD` (generated if
unset), `DISCORD_WEBHOOK`, `DISCORD_NOTIFICATIONS`, `AWS_ACCESS_KEY_ID`/
`AWS_SECRET_ACCESS_KEY`/`AWS_ENDPOINT_URL_S3`/`AWS_BUCKET_NAME`, `SAVE_NAME`,
`FACTORIO_USERNAME`/`FACTORIO_TOKEN` (falls back to `server-settings.json`),
`MONITOR_INTERVAL`, `WEB_ENABLED`/`WEB_ADDR`.

Install and launch settings, most carried over from the old base image:
`FACTORIO_VERSION` (`experimental`), `GAME_DIR` (`/factorio/game`),
`DEFAULTS_DIR` (`/opt/defaults`), `PORT` (`34197`), `DLC_SPACE_AGE` (`true`,
`false`, or a space-separated mod list), `LOAD_LATEST_SAVE` (`true`),
`GENERATE_NEW_SAVE` (`false`), `PRESET`, `BIND`, `CONSOLE_LOG_LOCATION`, and the
path overrides `CONFIG_DIR`/`SAVES_DIR`/`MODS_DIR`/`PRIVATE_MODS_DIR`/
`PUBLIC_MODS_JSON`/`SCENARIOS_DIR`/`SCRIPT_OUTPUT_DIR`.

`PUID`/`PGID` and `UPDATE_MODS_ON_START` are gone: the supervisor runs as root
on a single-tenant VM, and mod synchronisation is unconditional.

## Development

```
go test ./...
go build ./cmd/supervisor
```
