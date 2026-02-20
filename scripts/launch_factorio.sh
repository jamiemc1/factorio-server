#!/bin/bash
# Deploy script - Run this to deploy/redeploy to existing Fly.io app
# Usage: ./scripts/launch_factorio.sh [--silent]

SILENT=false
for arg in "$@"; do
    case $arg in
        --silent) SILENT=true ;;
    esac
done

tokenSecretFile=".factorio_token.dev"
rconSecretFile=".factorio_rcon.dev"
token="$(/bin/cat ${tokenSecretFile})"
rconPassword="$(/bin/cat ${rconSecretFile})"
settingFile="config/server-settings.json"
settingFileBackup="config/server-settings.json.old"
cp ${settingFile} ${settingFileBackup}
sed "s/replaceThisToken/${token}/" ${settingFileBackup} > ${settingFile}
sed -i "" "s/replaceThisRCON/${rconPassword}/" ${settingFile}

# Update secrets (in case they changed) - filter out comments and empty lines
grep -v '^\s*#' .fly/secrets.dev | grep -v '^\s*$' | fly secrets import

if [ "$SILENT" = true ]; then
    echo "Deploying in silent mode (Discord notifications disabled)"
    fly secrets set DISCORD_NOTIFICATIONS=false --stage
else
    fly secrets set DISCORD_NOTIFICATIONS=true --stage
fi

# Deploy to existing app
fly deploy . \
  --no-cache \
  --ha=false \

mv ${settingFileBackup} ${settingFile}

