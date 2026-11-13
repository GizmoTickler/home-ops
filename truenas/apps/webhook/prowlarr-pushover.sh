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

PROWLARR_APPLICATION_URL="${PROWLARR_APPLICATION_URL:?PROWLARR_APPLICATION_URL env var is required}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local event_type
    event_type="$(_jq '.eventType // empty')"

    case "${event_type}" in
        Test)
            printf -v PUSHOVER_TITLE "Test Notification"
            printf -v PUSHOVER_MESSAGE "Howdy this is a test notification from Prowlarr"
            printf -v PUSHOVER_URL "%s" "${PROWLARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "Open Prowlarr"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Health|HealthRestored)
            local level health_type health_msg
            level="$(_jq '.level // empty')"
            health_type="$(htmlesc "$(_jq '.type // "Health Check"')")"
            health_msg="$(htmlesc "$(_jq '.message // "No details available"')")"
            if [[ "${event_type}" == "HealthRestored" ]]; then
                printf -v PUSHOVER_TITLE "Prowlarr Health Restored"
                printf -v PUSHOVER_PRIORITY "low"
            elif [[ "${level}" == "error" ]]; then
                printf -v PUSHOVER_TITLE "Prowlarr Health Error"
                printf -v PUSHOVER_PRIORITY "high"
            else
                printf -v PUSHOVER_TITLE "Prowlarr Health Warning"
                printf -v PUSHOVER_PRIORITY "normal"
            fi
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${health_type}" "${health_msg}"
            printf -v PUSHOVER_URL "%s/system/status" "${PROWLARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View System Status"
            ;;
        ApplicationUpdate)
            local msg prev_ver new_ver
            msg="$(htmlesc "$(_jq '.message // "Prowlarr updated"')")"
            prev_ver="$(htmlesc "$(_jq '.previousVersion // empty')")"
            new_ver="$(htmlesc "$(_jq '.newVersion // empty')")"
            printf -v PUSHOVER_TITLE "Prowlarr Updated"
            if [[ -n "${prev_ver}" && -n "${new_ver}" ]]; then
                printf -v PUSHOVER_MESSAGE "<b>%s -> %s</b><small>\n%s</small>" "${prev_ver}" "${new_ver}" "${msg}"
            else
                printf -v PUSHOVER_MESSAGE "<small>%s</small>" "${msg}"
            fi
            printf -v PUSHOVER_URL "%s/system/status" "${PROWLARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View System Status"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Grab)
            local rel_title indexer
            rel_title="$(htmlesc "$(_jq '.release.releaseTitle // "Unknown Release"')")"
            indexer="$(htmlesc "$(_jq '.release.indexer // "Unknown Indexer"')")"
            printf -v PUSHOVER_TITLE "Prowlarr Grab"
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n<b>Indexer:</b> %s</small>" "${rel_title}" "${indexer}"
            printf -v PUSHOVER_URL "%s/history" "${PROWLARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View History"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        *)
            exit 0
            ;;
    esac

    apprise --title "${PUSHOVER_TITLE}" --body "${PUSHOVER_MESSAGE}" --input-format html \
        "${PUSHOVER_URL_BASE}?url=$(urienc "${PUSHOVER_URL}")&url_title=$(urienc "${PUSHOVER_URL_TITLE}")&priority=$(urienc "${PUSHOVER_PRIORITY}")&format=html"
}

function main() {
    notify
}

main "$@"
