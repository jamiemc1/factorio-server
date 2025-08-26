#!/bin/bash

tokenSecretFile=".factorio_token.dev"
token="$(/bin/cat ${tokenSecretFile})"
settingFile="config/server-settings.json"
settingFileBackup="config/server-settings.json.old"
cp ${settingFile} ${settingFileBackup}
sed "s/replaceThisToken/${token}/" ${settingFileBackup} > ${settingFile}

fly launch \
  --name "factorio-server" \
  --no-github-workflow \
  --config fly.toml \
  --copy-config \
  --no-cache \
  --yes \
  --no-public-ips \
  --no-deploy \

fly ips \
	allocate-v4 \
	--shared


cat .fly/secrets.dev | fly secrets import

fly deploy . \
  --no-cache \
  --ha=false \

mv ${settingFileBackup} ${settingFile}

