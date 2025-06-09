#!/bin/bash

fly launch \
  --no-github-workflow \
  --max-concurrent 1 \
  --config fly.toml \
  --copy-config \


cat .fly/secrets.dev | fly secrets import
