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

CHAPTARR_APPLICATION_URL="${CHAPTARR_APPLICATION_URL:?CHAPTARR_APPLICATION_URL env var is required}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local event_type
    event_type="$(_jq '.eventType // empty')"

    case "${event_type}" in
        Test)
            printf -v PUSHOVER_TITLE "Test Notification"
            printf -v PUSHOVER_MESSAGE "Howdy this is a test notification from Chaptarr"
            printf -v PUSHOVER_URL "%s" "${CHAPTARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "Open Chaptarr"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Download|ReleaseImport)
            local is_upgrade author book_titles download_client
            is_upgrade="$(_jq 'if .isUpgrade == true then "true" else "false" end')"
            author="$(htmlesc "$(_jq '.author.name // empty')")"
            book_titles="$(htmlesc "$(_jq '[.books[]?.title // empty, .book.title // empty] | unique | map(select(. != "")) | join(", ")')")"
            if [[ -z "${book_titles}" ]]; then
                book_titles="$(htmlesc "$(_jq '.release.releaseTitle // "Release Imported"')")"
            fi
            download_client="$(htmlesc "$(_jq '.downloadClient // empty')")"

            if [[ "${is_upgrade}" == "true" ]]; then
                printf -v PUSHOVER_TITLE "Book Upgraded"
            else
                printf -v PUSHOVER_TITLE "Book Added"
            fi

            if [[ -n "${author}" ]]; then
                printf -v PUSHOVER_MESSAGE "<b>%s — %s</b>" "${author}" "${book_titles}"
            else
                printf -v PUSHOVER_MESSAGE "<b>%s</b>" "${book_titles}"
            fi
            if [[ -n "${download_client}" ]]; then
                PUSHOVER_MESSAGE+="<small>"$'\n\n'"<b>Client:</b> ${download_client}</small>"
            fi
            printf -v PUSHOVER_URL "%s" "${CHAPTARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "Open Chaptarr"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Health|HealthRestored)
            local level health_type health_msg
            level="$(_jq '.level // empty')"
            health_type="$(htmlesc "$(_jq '.type // "Health Check"')")"
            health_msg="$(htmlesc "$(_jq '.message // "No details available"')")"
            if [[ "${event_type}" == "HealthRestored" ]]; then
                printf -v PUSHOVER_TITLE "Chaptarr Health Restored"
                printf -v PUSHOVER_PRIORITY "low"
            elif [[ "${level}" == "error" ]]; then
                printf -v PUSHOVER_TITLE "Chaptarr Health Error"
                printf -v PUSHOVER_PRIORITY "high"
            else
                printf -v PUSHOVER_TITLE "Chaptarr Health Warning"
                printf -v PUSHOVER_PRIORITY "normal"
            fi
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${health_type}" "${health_msg}"
            printf -v PUSHOVER_URL "%s/system/status" "${CHAPTARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View System Status"
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
