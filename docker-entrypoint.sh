#!/usr/bin/env bash
set -Eeo pipefail

cd /app
./browser-update
./zipreport-server -c config/config.json
