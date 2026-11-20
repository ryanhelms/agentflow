#!/usr/bin/env bash
# Post a message to Slack through an incoming webhook.
#
#   slack.sh "<text>"          (mrkdwn supported)
#
# Env: SLACK_WEBHOOK_URL
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

text="${1:?usage: slack.sh <text>}"
if [ -z "${SLACK_WEBHOOK_URL:-}" ]; then
  warn "SLACK_WEBHOOK_URL not set; skipping Slack message"
  exit 0
fi
curl -sSf -X POST -H 'Content-Type: application/json' \
  -d "$(jq -cn --arg t "$text" '{text:$t}')" "$SLACK_WEBHOOK_URL" >/dev/null
