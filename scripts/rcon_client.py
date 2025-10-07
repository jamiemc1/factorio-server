"""
Factorio RCON client for communicating with the game server.

This module provides methods to interact with a running Factorio server
via RCON (Remote Console) to retrieve player information and execute commands.
"""

import os
from typing import Optional
from factorio_rcon import RCONClient


class FactorioRCON:
    """Client for interacting with Factorio server via RCON."""

    def __init__(
        self,
        host: str = "localhost",
        port: int = 27015,
        password: Optional[str] = None
    ):
        """
        Initialize the RCON client.

        Args:
            host: Factorio server host
            port: RCON port (default: 27015)
            password: RCON password. If not provided, reads from RCON_PASSWORD env var.
        """
        self.host = host
        self.port = port
        self.password = password or os.getenv("RCON_PASSWORD", "")
        self.client: Optional[RCONClient] = None

    def connect(self) -> None:
        """Establish connection to the Factorio RCON server."""
        self.client = RCONClient(self.host, self.port, self.password)

    def disconnect(self) -> None:
        """Close the RCON connection."""
        if self.client:
            self.client.close()
            self.client = None

    def __enter__(self):
        """Context manager entry."""
        self.connect()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        """Context manager exit."""
        self.disconnect()

    def get_online_players(self) -> list[str]:
        """
        Get list of currently online players.

        Returns:
            List of player names currently online
        """
        if not self.client:
            raise RuntimeError("RCON client not connected")

        response = self.client.send_command("/players online")

        # Parse the response
        # Format: "Online players (X): player1, player2, player3"
        if "Online players" not in response:
            return []

        # Extract player list from response
        if ":" not in response:
            return []

        player_section = response.split(":", 1)[1].strip()

        if not player_section or player_section == "":
            return []

        # Split by comma and clean up names
        players = [name.strip() for name in player_section.split(",")]
        return [p for p in players if p]

    def execute_command(self, command: str) -> str:
        """
        Execute a command on the Factorio server.

        Args:
            command: Command to execute

        Returns:
            Server response
        """
        if not self.client:
            raise RuntimeError("RCON client not connected")

        return self.client.send_command(command)
