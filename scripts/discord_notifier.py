"""
Discord notification module for Factorio server events.

This module provides a centralized way to send notifications to Discord
about server events, player activity, and status updates.
"""

import os
from datetime import datetime
from typing import Optional
from discord_webhook import DiscordWebhook, DiscordEmbed


class DiscordNotifier:
    """Handles Discord webhook notifications for Factorio server events."""

    def __init__(self, webhook_url: Optional[str] = None):
        """
        Initialize the Discord notifier.

        Args:
            webhook_url: Discord webhook URL. If not provided, reads from DISCORD_WEBHOOK env var.
        """
        self.webhook_url = webhook_url or os.getenv("DISCORD_WEBHOOK")
        if not self.webhook_url:
            raise ValueError("Discord webhook URL not provided")

    def _send_embed(
        self,
        title: str,
        description: str,
        color: str = "03b2f8",
        fields: Optional[list[dict]] = None
    ) -> None:
        """
        Send an embedded message to Discord.

        Args:
            title: Embed title
            description: Embed description
            color: Hex color code (default: blue)
            fields: List of field dicts with 'name' and 'value' keys
        """
        webhook = DiscordWebhook(url=self.webhook_url)
        embed = DiscordEmbed(title=title, description=description, color=color)
        embed.set_timestamp()

        if fields:
            for field in fields:
                embed.add_embed_field(
                    name=field["name"],
                    value=field["value"],
                    inline=field.get("inline", False)
                )

        webhook.add_embed(embed)
        webhook.execute()

    def notify_server_startup(self, server_name: str = "Factorio Server") -> None:
        """
        Send a notification that the server has started.

        Args:
            server_name: Name of the Factorio server
        """
        self._send_embed(
            title=f"🚀 {server_name} Started",
            description="The Factorio server is now online and ready for players!",
            color="00ff00"
        )

    def notify_server_shutdown(self, server_name: str = "Factorio Server") -> None:
        """
        Send a notification that the server is shutting down.

        Args:
            server_name: Name of the Factorio server
        """
        self._send_embed(
            title=f"🛑 {server_name} Shutting Down",
            description="The Factorio server is shutting down.",
            color="ff0000"
        )

    def notify_player_status(
        self,
        online_players: list[str],
        server_name: str = "Factorio Server"
    ) -> None:
        """
        Send a notification about current player status.

        Args:
            online_players: List of player names currently online
            server_name: Name of the Factorio server
        """
        player_count = len(online_players)

        if player_count == 0:
            description = "No players currently online."
            fields = None
        else:
            description = f"{player_count} player{'s' if player_count != 1 else ''} online"
            fields = [{
                "name": "Players",
                "value": "\n".join(f"• {player}" for player in online_players),
                "inline": False
            }]

        self._send_embed(
            title=f"👥 {server_name} - Player Status",
            description=description,
            color="03b2f8",
            fields=fields
        )

    def notify_player_joined(self, player_name: str) -> None:
        """
        Send a notification when a player joins.

        Args:
            player_name: Name of the player who joined
        """
        self._send_embed(
            title="➕ Player Joined",
            description=f"**{player_name}** has joined the server",
            color="00ff00"
        )

    def notify_player_left(self, player_name: str) -> None:
        """
        Send a notification when a player leaves.

        Args:
            player_name: Name of the player who left
        """
        self._send_embed(
            title="➖ Player Left",
            description=f"**{player_name}** has left the server",
            color="ffa500"
        )
