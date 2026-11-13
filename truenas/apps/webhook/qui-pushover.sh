#!/usr/bin/env bash
set -euo pipefail

function urienc() { jq --raw-output --null-input --arg u "${1}" '$u|@uri'; }

function htmlesc() {
    local s="${1}"
    s="${s//&/&amp;}"
    s="${s//</&lt;}"
    s="${s//>/&gt;}"
    printf '%s' "${s}"
}

PUSHOVER_URL_BASE="${1:?}"
PAYLOAD="${2:?}"

QUI_APPLICATION_URL="${QUI_APPLICATION_URL:?QUI_APPLICATION_URL env var is required}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local raw_title raw_message title message
    if jq -e 'type == "object"' <<<"${PAYLOAD}" >/dev/null 2>&1; then
        raw_title="$(_jq '.title // .subject // "Qui Notification"')"
        raw_message="$(_jq '.message // .body // "No details available"')"
    else
        raw_title="Qui Notification"
        raw_message="${PAYLOAD}"
    fi

    title="$(htmlesc "${raw_title}")"
    message="$(htmlesc "${raw_message}")"

    PUSHOVER_TITLE="${raw_title}"
    printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${title}" "${message}"
    PUSHOVER_URL="${QUI_APPLICATION_URL}"
    PUSHOVER_URL_TITLE="Open Qui"

    if [[ "${raw_title,,} ${raw_message,,}" =~ (error|fail|unhealthy|disconnected) ]]; then
        PUSHOVER_PRIORITY="high"
    elif [[ "${raw_title,,} ${raw_message,,}" == *"test"* ]]; then
        PUSHOVER_PRIORITY="low"
    else
        PUSHOVER_PRIORITY="normal"
    fi

    apprise --title "${PUSHOVER_TITLE}" --body "${PUSHOVER_MESSAGE}" --input-format html \
        "${PUSHOVER_URL_BASE}?url=$(urienc "${PUSHOVER_URL}")&url_title=$(urienc "${PUSHOVER_URL_TITLE}")&priority=$(urienc "${PUSHOVER_PRIORITY}")&format=html"
}

function main() {
    notify
}

main "$@"
