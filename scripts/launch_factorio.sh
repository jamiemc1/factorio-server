#!/bin/bash
# Deploy script - Run this to deploy/redeploy to existing Fly.io app

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

# Deploy to existing app
fly deploy . \
  --no-cache \
  --ha=false \

mv ${settingFileBackup} ${settingFile}

