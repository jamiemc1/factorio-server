"""
Startup notification script for Factorio server.

Sends a Discord notification when the server starts up.
"""

import os
from discord_notifier import DiscordNotifier


def main() -> None:
    """Send server startup notification."""
    try:
        notifier = DiscordNotifier()
        server_name = os.getenv("SERVER_NAME", "Factorio Server")
        notifier.notify_server_startup(server_name)
        print(f"Startup notification sent for {server_name}")
    except Exception as e:
        print(f"Failed to send startup notification: {e}")


if __name__ == "__main__":
    main()
