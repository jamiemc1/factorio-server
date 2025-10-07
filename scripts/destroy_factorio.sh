#!/bin/bash
# Stop the Factorio server (scales to 0 machines, stops billing)
# All data and configuration are preserved

APP_NAME=$(grep "^app = " fly.toml | cut -d "'" -f 2)

echo "Stopping Factorio server: $APP_NAME"
fly scale count 0 --app "$APP_NAME" --yes

echo "Server stopped. No machines running, billing paused."
echo "To restart, run: ./scripts/launch_factorio.sh"
