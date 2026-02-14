#!/usr/bin/env bash
set -euo pipefail

SEERR_PUSHOVER_URL=${1:?}
PAYLOAD=${2:?}

SEERR_URL="${SEERR_URL:?SEERR_URL env var is required}"

echo "[DEBUG] Payload: ${PAYLOAD}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local type="$(_jq '.notification_type')"
    local subject="$(_jq '.subject // empty')"
    local message="$(_jq '.message // empty')"
    local media_type="$(_jq '.media.media_type // empty')"
    local tmdb_id="$(_jq '.media.tmdbId // empty')"
    local requested_by="$(_jq '.request.requestedBy_username // empty')"

    if [[ "${type}" == "TEST_NOTIFICATION" ]]; then
        printf -v PUSHOVER_TITLE "Test Notification"
        printf -v PUSHOVER_MESSAGE "Howdy this is a test notification from <b>%s</b>" "Seerr"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "low"
    elif [[ "${type}" == "MEDIA_PENDING" ]]; then
        printf -v PUSHOVER_TITLE "New Request"
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small><small>\n\n<b>Requested by:</b> %s</small>" \
            "${subject}" "${message}" "${requested_by}"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}/requests"
        printf -v PUSHOVER_URL_TITLE "View Requests"
        printf -v PUSHOVER_PRIORITY "normal"
    elif [[ "${type}" == "MEDIA_APPROVED" || "${type}" == "MEDIA_AUTO_APPROVED" ]]; then
        printf -v PUSHOVER_TITLE "Request Approved"
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${subject}" "${message}"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "low"
    elif [[ "${type}" == "MEDIA_AVAILABLE" ]]; then
        printf -v PUSHOVER_TITLE "Now Available"
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${subject}" "${message}"
        if [[ -n "${media_type}" && -n "${tmdb_id}" ]]; then
            printf -v PUSHOVER_URL "%s/%s/%s" "${SEERR_URL}" "${media_type}" "${tmdb_id}"
        else
            printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        fi
        printf -v PUSHOVER_URL_TITLE "View Media"
        printf -v PUSHOVER_PRIORITY "low"
    elif [[ "${type}" == "MEDIA_DECLINED" ]]; then
        printf -v PUSHOVER_TITLE "Request Declined"
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${subject}" "${message}"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "normal"
    elif [[ "${type}" == "MEDIA_FAILED" ]]; then
        printf -v PUSHOVER_TITLE "Request Failed"
        printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${subject}" "${message}"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "high"
    elif [[ "${type}" == ISSUE_* ]]; then
        local issue_type="$(_jq '.issue.issue_type // empty')"
        local reported_by="$(_jq '.issue.reportedBy_username // empty')"
        local comment_msg="$(_jq '.comment.comment_message // empty')"
        if [[ "${type}" == "ISSUE_COMMENT" && -n "${comment_msg}" ]]; then
            printf -v PUSHOVER_TITLE "Issue Comment"
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small><small>\n\n<b>Comment:</b> %s</small>" \
                "${subject}" "${message}" "${comment_msg}"
        else
            printf -v PUSHOVER_TITLE "Issue: %s" "${issue_type:-${subject}}"
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small><small>\n\n<b>Reported by:</b> %s</small>" \
                "${subject}" "${message}" "${reported_by}"
        fi
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "normal"
    else
        printf -v PUSHOVER_TITLE "%s" "${subject:-Seerr Notification}"
        printf -v PUSHOVER_MESSAGE "%s" "${message:-No details available}"
        printf -v PUSHOVER_URL "%s" "${SEERR_URL}"
        printf -v PUSHOVER_URL_TITLE "Open Seerr"
        printf -v PUSHOVER_PRIORITY "low"
    fi

    apprise -vv --title "${PUSHOVER_TITLE}" --body "${PUSHOVER_MESSAGE}" --input-format html \
        "${SEERR_PUSHOVER_URL}?url=${PUSHOVER_URL}&url_title=${PUSHOVER_URL_TITLE}&priority=${PUSHOVER_PRIORITY}&format=html"
}

function main() {
    notify
}

main "$@"
