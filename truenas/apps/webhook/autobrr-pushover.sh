#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=pushover.sh
source "${WEBHOOK_LIB_DIR:-/config}/pushover.sh"

function htmlesc() {
    local s="${1}"
    s="${s//&/&amp;}"
    s="${s//</&lt;}"
    s="${s//>/&gt;}"
    printf '%s' "${s}"
}

PUSHOVER_URL_BASE="${1:?}"
PAYLOAD="${2:?}"

AUTOBRR_APPLICATION_URL="${AUTOBRR_APPLICATION_URL:?AUTOBRR_APPLICATION_URL env var is required}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local subject message event success infourl
    subject="$(_jq '.subject // "Autobrr Notification"')"
    message="$(htmlesc "$(_jq '.message // "No details available"')")"
    event="$(htmlesc "$(_jq '.event // empty')")"
    success="$(_jq 'if .success == false then "false" else "true" end')"
    infourl="$(_jq '.infourl // empty')"

    PUSHOVER_TITLE="${subject}"
    if [[ -n "${event}" ]]; then
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${event}" "${message}"
    else
        printf -v PUSHOVER_MESSAGE "%s" "${message}"
    fi

    if [[ -n "${infourl}" && "${infourl}" =~ ^https?:// ]]; then
        PUSHOVER_URL="${infourl}"
    else
        PUSHOVER_URL="${AUTOBRR_APPLICATION_URL}"
    fi
    PUSHOVER_URL_TITLE="Open Autobrr"

    if [[ "${success}" == "false" || "${event}" =~ ^(PUSH_ERROR|IRC_DISCONNECTED)$ ]]; then
        PUSHOVER_PRIORITY="high"
    elif [[ "${event}" == "TEST" || "${subject,,}" == *"test"* ]]; then
        PUSHOVER_PRIORITY="low"
    else
        PUSHOVER_PRIORITY="normal"
    fi

    send_pushover "${PUSHOVER_URL_BASE}" "${PUSHOVER_TITLE}" "${PUSHOVER_MESSAGE}" \
        "${PUSHOVER_URL}" "${PUSHOVER_URL_TITLE}" "${PUSHOVER_PRIORITY}"
}

function main() {
    notify
}

main "$@"
