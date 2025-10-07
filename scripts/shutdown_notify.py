"""
Shutdown notification script for Factorio server.

Sends a Discord notification when the server is shutting down.
"""

import os
from discord_notifier import DiscordNotifier


def main() -> None:
    """Send server shutdown notification."""
    try:
        notifier = DiscordNotifier()
        server_name = os.getenv("SERVER_NAME", "Factorio Server")
        notifier.notify_server_shutdown(server_name)
        print(f"Shutdown notification sent for {server_name}")
    except Exception as e:
        print(f"Failed to send shutdown notification: {e}")


if __name__ == "__main__":
    main()
