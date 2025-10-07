"""
Player monitoring service for Factorio server.

Periodically checks who is online and sends notifications to Discord
about player status changes.
"""

import argparse
import os
import time
import signal
import sys
from typing import Set

sys.path.insert(0, os.path.dirname(__file__))
from discord_notifier import DiscordNotifier
from factorio_rcon import FactorioRCON


class PlayerMonitor:
    """Monitors player activity and sends Discord notifications."""

    def __init__(
        self,
        check_interval: int = 300,
        notify_join_leave: bool = True,
        periodic_status: bool = True
    ):
        """
        Initialize the player monitor.

        Args:
            check_interval: Seconds between player checks (default: 300 = 5 minutes)
            notify_join_leave: Send notifications for individual join/leave events
            periodic_status: Send periodic status updates
        """
        self.check_interval = check_interval
        self.notify_join_leave = notify_join_leave
        self.periodic_status = periodic_status
        self.notifier = DiscordNotifier()
        self.rcon = FactorioRCON()
        self.previous_players: Set[str] = set()
        self.running = True

    def handle_shutdown(self, signum, frame):
        """Handle shutdown signals gracefully."""
        print("Player monitor shutting down...")
        self.running = False
        sys.exit(0)

    def check_players(self) -> None:
        """Check current players and send notifications for changes."""
        try:
            with self.rcon:
                current_players = set(self.rcon.get_online_players())

            # Detect joins and leaves
            joined = current_players - self.previous_players
            left = self.previous_players - current_players

            # Send individual join/leave notifications
            if self.notify_join_leave:
                for player in joined:
                    self.notifier.notify_player_joined(player)

                for player in left:
                    self.notifier.notify_player_left(player)

            # Send periodic status update
            if self.periodic_status:
                self.notifier.notify_player_status(list(current_players))

            self.previous_players = current_players

        except Exception as e:
            print(f"Error checking players: {e}")

    def run(self) -> None:
        """Run the monitoring loop."""
        # Set up signal handlers for graceful shutdown
        signal.signal(signal.SIGTERM, self.handle_shutdown)
        signal.signal(signal.SIGINT, self.handle_shutdown)

        print(f"Player monitor started (checking every {self.check_interval} seconds)")

        while self.running:
            self.check_players()
            time.sleep(self.check_interval)


def parse_arguments() -> argparse.Namespace:
    """Parse command line arguments."""
    parser = argparse.ArgumentParser(
        description="Monitor Factorio server player activity"
    )
    parser.add_argument(
        "--interval",
        type=int,
        default=300,
        help="Check interval in seconds (default: 300)",
    )
    parser.add_argument(
        "--no-join-leave",
        action="store_true",
        help="Disable individual join/leave notifications",
    )
    parser.add_argument(
        "--no-status",
        action="store_true",
        help="Disable periodic status updates",
    )
    return parser.parse_args()


def main() -> None:
    """Main entry point."""
    args = parse_arguments()

    monitor = PlayerMonitor(
        check_interval=args.interval,
        notify_join_leave=not args.no_join_leave,
        periodic_status=not args.no_status,
    )

    monitor.run()


if __name__ == "__main__":
    main()
