
import json
import os
from datetime import datetime
from discord_webhook import DiscordWebhook, DiscordEmbed




def discord():

    webhook = DiscordWebhook(
        url=str(os.getenv("DISCORD_WEBHOOK"))
    )
