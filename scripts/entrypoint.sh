#!/bin/bash
set -euo pipefail

# Factorio server entrypoint with Discord integration
# This script wraps the Factorio server startup and manages notifications

POETRY_BIN="/root/.local/bin/poetry"
PYTHON_BIN="python3"

# Inject RCON password if RCON_PASSWORD is set
if [ -n "${RCON_PASSWORD:-}" ]; then
    echo "Configuring RCON password..."
    # The base image reads RCON password from /factorio/config/rconpw
    echo -n "$RCON_PASSWORD" > /factorio/config/rconpw
    # Also update server-settings.json for consistency
    jq --arg pwd "$RCON_PASSWORD" '.rcon.password = $pwd' /factorio/config/server-settings.json > /tmp/server-settings.json.tmp
    mv /tmp/server-settings.json.tmp /factorio/config/server-settings.json
fi

# Function to send startup notification
send_startup_notification() {
    echo "Sending startup notification to Discord..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/startup_notify.py || echo "Failed to send startup notification"
}

# Function to upload save files to S3
upload_save_files() {
    echo "Uploading save files to S3..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/save_file_manager.py --upload || echo "Failed to upload save files"
}

# Function to send shutdown notification
send_shutdown_notification() {
    echo "Sending shutdown notification to Discord..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/shutdown_notify.py || echo "Failed to send shutdown notification"
}

# Function to start player monitor in background
start_player_monitor() {
    echo "Starting player monitor..."
    $POETRY_BIN run $PYTHON_BIN /factorio/scripts/player_monitor.py --interval 600 --no-status &
    PLAYER_MONITOR_PID=$!
    echo "Player monitor started with PID $PLAYER_MONITOR_PID"
}

# Trap shutdown signals
SHUTDOWN_INITIATED=false
shutdown() {
    # Prevent duplicate shutdown notifications
    if [ "$SHUTDOWN_INITIATED" = true ]; then
        return
    fi
    SHUTDOWN_INITIATED=true

    echo "Received shutdown signal"

    # Kill player monitor immediately
    if [ ! -z "${PLAYER_MONITOR_PID:-}" ]; then
        echo "Stopping player monitor (PID $PLAYER_MONITOR_PID)..."
        kill -TERM "$PLAYER_MONITOR_PID" 2>/dev/null || true
    fi

    # Signal Factorio server to shutdown (triggers autosave)
    if [ ! -z "${FACTORIO_PID:-}" ]; then
        echo "Stopping Factorio server (PID $FACTORIO_PID)..."
        kill -TERM "$FACTORIO_PID" 2>/dev/null || true
        # Give Factorio a moment to start saving, but don't wait for full shutdown
        sleep 2
    fi

    # Upload save files to S3 before shutting down
    upload_save_files

    # Send shutdown notification
    send_shutdown_notification

    # Now wait for processes to finish if they're still running
    if [ ! -z "${PLAYER_MONITOR_PID:-}" ]; then
        wait "$PLAYER_MONITOR_PID" 2>/dev/null || true
    fi
    if [ ! -z "${FACTORIO_PID:-}" ]; then
        wait "$FACTORIO_PID" 2>/dev/null || true
    fi

    exit 0
}

trap shutdown SIGTERM SIGINT

# Send startup notification
send_startup_notification

# Start player monitor in background (wait a bit for server to be ready)
(sleep 30 && start_player_monitor) &

# Start Factorio server
echo "Starting Factorio server..."
/docker-entrypoint.sh &
FACTORIO_PID=$!

# Wait for Factorio process
wait "$FACTORIO_PID"
