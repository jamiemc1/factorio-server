#!/bin/bash
set -euo pipefail

# Factorio server entrypoint with Discord integration
# This script wraps the Factorio server startup and manages notifications

POETRY_BIN="/root/.local/bin/poetry"
PYTHON_BIN="python3"

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S UTC')] $*"
}

# Inject RCON password if RCON_PASSWORD is set
if [ -n "${RCON_PASSWORD:-}" ]; then
    log "Configuring RCON password..."
    # The base image reads RCON password from /factorio/config/rconpw
    echo -n "$RCON_PASSWORD" > /factorio/config/rconpw
    # Also update server-settings.json for consistency
    jq --arg pwd "$RCON_PASSWORD" '.rcon.password = $pwd' /factorio/config/server-settings.json > /tmp/server-settings.json.tmp
    mv /tmp/server-settings.json.tmp /factorio/config/server-settings.json
fi

notifications_enabled() {
    [ "${DISCORD_NOTIFICATIONS:-true}" = "true" ]
}

# Function to send startup notification
send_startup_notification() {
    if ! notifications_enabled; then
        log "Discord notifications disabled, skipping startup notification"
        return
    fi
    log "Sending startup notification to Discord..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/startup_notify.py || log "Failed to send startup notification"
}

# Function to upload save files to S3
upload_save_files() {
    log "Uploading save files to S3..."
    local upload_start=$SECONDS
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/save_file_manager.py --upload
    local upload_status=$?
    local upload_duration=$(( SECONDS - upload_start ))
    if [ $upload_status -eq 0 ]; then
        log "Save file upload completed in ${upload_duration}s"
    else
        log "Save file upload FAILED (exit code $upload_status) after ${upload_duration}s"
    fi
    return $upload_status
}

# Function to send shutdown notification
send_shutdown_notification() {
    if ! notifications_enabled; then
        log "Discord notifications disabled, skipping shutdown notification"
        return
    fi
    log "Sending shutdown notification to Discord..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/shutdown_notify.py || log "Failed to send shutdown notification"
}

# Function to start player monitor in background
start_player_monitor() {
    log "Starting player monitor..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/player_monitor.py --interval 600 --no-status &
    PLAYER_MONITOR_PID=$!
    log "Player monitor started with PID $PLAYER_MONITOR_PID"
}

# Trap shutdown signals
SHUTDOWN_INITIATED=false
shutdown() {
    # Prevent duplicate shutdown notifications
    if [ "$SHUTDOWN_INITIATED" = true ]; then
        return
    fi
    SHUTDOWN_INITIATED=true

    local shutdown_start=$SECONDS
    log "Received shutdown signal — beginning shutdown sequence"

    # Kill player monitor immediately
    if [ -n "${PLAYER_MONITOR_PID:-}" ]; then
        log "Stopping player monitor (PID $PLAYER_MONITOR_PID)..."
        kill -TERM "$PLAYER_MONITOR_PID" 2>/dev/null || true
        wait "$PLAYER_MONITOR_PID" 2>/dev/null || true
        log "Player monitor stopped"
    fi

    # Signal Factorio server to shutdown (triggers autosave) and wait for it to finish
    if [ -n "${FACTORIO_PID:-}" ]; then
        log "Sending SIGTERM to Factorio server (PID $FACTORIO_PID)..."
        kill -TERM "$FACTORIO_PID" 2>/dev/null || true

        log "Waiting for Factorio to save and exit (up to 60s)..."
        local wait_count=0
        while kill -0 "$FACTORIO_PID" 2>/dev/null; do
            sleep 1
            wait_count=$(( wait_count + 1 ))
            if [ $wait_count -ge 60 ]; then
                log "WARNING: Factorio did not exit after 60s, proceeding with upload anyway"
                break
            fi
        done
        log "Factorio server exited after ${wait_count}s"
    fi

    # Debug: check what's actually in the saves directory at upload time
    log "DEBUG: Contents of /factorio/saves/:"
    ls -la /factorio/saves/ 2>&1 | while read line; do log "  $line"; done

    # Upload save files to S3
    upload_save_files || log "WARNING: Upload failed, save may not be persisted"

    # Send shutdown notification
    send_shutdown_notification

    local shutdown_duration=$(( SECONDS - shutdown_start ))
    log "Shutdown sequence completed in ${shutdown_duration}s"

    exit 0
}

trap shutdown SIGTERM SIGINT

# Send startup notification
send_startup_notification

# Assemble /factorio/mods from private (baked-in) and public (S3-cached) sources
sync_mods() {
    log "Syncing mods (private + S3-cached public)..."
    if $POETRY_BIN run $PYTHON_BIN /factorio/scripts/mod_manager.py --sync; then
        log "Mod sync completed"
    else
        log "FATAL: Mod sync failed — aborting startup to avoid loading a mismatched mod set"
        exit 1
    fi
}
sync_mods

# Start player monitor in background (wait a bit for server to be ready)
(sleep 30 && start_player_monitor) &

# Start Factorio server
log "Starting Factorio server..."
/docker-entrypoint.sh &
FACTORIO_PID=$!

# Wait for Factorio process
wait "$FACTORIO_PID"
