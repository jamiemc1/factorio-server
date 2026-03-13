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
from pathlib import Path
from typing import Set

sys.path.insert(0, os.path.dirname(__file__))
from discord_notifier import DiscordNotifier
from rcon_client import FactorioRCON
from save_file_manager import upload_file, log

from s3fs import S3FileSystem


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
        self.last_uploaded_mtime: float = 0.0
        self.s3_filesystem = self._create_s3_filesystem()
        self.save_name = os.environ.get("SAVE_NAME", "_autosave1")
        self.bucket_name = os.environ.get("AWS_BUCKET_NAME", "")
        self.save_path = Path(f"/factorio/saves/{self.save_name}.zip")

    def _create_s3_filesystem(self) -> S3FileSystem | None:
        key = os.environ.get("AWS_ACCESS_KEY_ID")
        secret = os.environ.get("AWS_SECRET_ACCESS_KEY")
        endpoint_url = os.environ.get("AWS_ENDPOINT_URL_S3")
        if not all([key, secret, endpoint_url]):
            log("Save upload disabled: missing AWS credentials")
            return None
        return S3FileSystem(key=key, secret=secret, endpoint_url=endpoint_url)

    def upload_save_if_changed(self) -> None:
        if not self.s3_filesystem or not self.bucket_name:
            return
        if not self.save_path.exists():
            return
        current_mtime = self.save_path.stat().st_mtime
        if current_mtime == self.last_uploaded_mtime:
            return
        destination = f"{self.bucket_name}/{self.save_name}.zip"
        try:
            upload_file(self.s3_filesystem, str(self.save_path), destination)
            self.last_uploaded_mtime = current_mtime
            log(f"Periodic save upload: {destination}")
        except OSError as error:
            log(f"Periodic save upload failed: {error}")

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
            self.upload_save_if_changed()
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
