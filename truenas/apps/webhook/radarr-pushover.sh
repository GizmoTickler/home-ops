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

RADARR_PUSHOVER_URL="${1:?}"
PAYLOAD="${2:?}"

RADARR_APPLICATION_URL="${RADARR_APPLICATION_URL:?RADARR_APPLICATION_URL env var is required}"

function _jq() {
    jq --raw-output "${1:?}" <<<"${PAYLOAD}"
}

function notify() {
    local event_type
    event_type="$(_jq '.eventType // empty')"

    case "${event_type}" in
        Test)
            printf -v PUSHOVER_TITLE "Test Notification"
            printf -v PUSHOVER_MESSAGE "Howdy this is a test notification"
            printf -v PUSHOVER_URL "%s" "${RADARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View Movies"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        ManualInteractionRequired)
            local movie_title movie_year download_client details
            movie_title="$(htmlesc "$(_jq '.movie.title // "Unknown Movie"')")"
            movie_year="$(_jq '.movie.year // empty')"
            download_client="$(htmlesc "$(_jq '.downloadClient // "Unknown"')")"
            details="$(htmlesc "$(_jq '[.downloadStatusMessages[]?.messages[]?] | join("; ")')")"
            printf -v PUSHOVER_TITLE "Movie Requires Manual Interaction"
            if [[ -n "${movie_year}" ]]; then
                printf -v PUSHOVER_MESSAGE "<b>%s (%s)</b><small>\n<b>Client:</b> %s" \
                    "${movie_title}" "${movie_year}" "${download_client}"
            else
                printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n<b>Client:</b> %s" \
                    "${movie_title}" "${download_client}"
            fi
            if [[ -n "${details}" ]]; then
                PUSHOVER_MESSAGE+=$'\n'"${details}"
            fi
            PUSHOVER_MESSAGE+="</small>"
            printf -v PUSHOVER_URL "%s/activity/queue" "${RADARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View Queue"
            printf -v PUSHOVER_PRIORITY "high"
            ;;
        Download)
            local is_upgrade movie_title movie_year movie_overview tmdb_id download_client quality
            is_upgrade="$(_jq 'if .isUpgrade == true then "true" else "false" end')"
            movie_title="$(htmlesc "$(_jq '.movie.title // "Unknown Movie"')")"
            movie_year="$(_jq '.movie.year // empty')"
            movie_overview="$(htmlesc "$(_jq '.movie.overview // empty')")"
            tmdb_id="$(_jq '.movie.tmdbId // empty')"
            download_client="$(htmlesc "$(_jq '.downloadClient // "Unknown"')")"
            quality="$(htmlesc "$(_jq '.movieFile.quality // empty')")"

            if [[ "${is_upgrade}" == "true" ]]; then
                printf -v PUSHOVER_TITLE "Movie Upgraded"
            else
                printf -v PUSHOVER_TITLE "Movie Added"
            fi

            if [[ -n "${movie_year}" ]]; then
                printf -v PUSHOVER_MESSAGE "<b>%s (%s)</b>" "${movie_title}" "${movie_year}"
            else
                printf -v PUSHOVER_MESSAGE "<b>%s</b>" "${movie_title}"
            fi
            if [[ -n "${movie_overview}" ]]; then
                PUSHOVER_MESSAGE+="<small>"$'\n'"${movie_overview}</small>"
            fi
            PUSHOVER_MESSAGE+="<small>"$'\n\n'"<b>Client:</b> ${download_client}"
            if [[ -n "${quality}" ]]; then
                PUSHOVER_MESSAGE+=" (${quality})"
            fi
            PUSHOVER_MESSAGE+="</small>"

            if [[ -n "${tmdb_id}" ]]; then
                printf -v PUSHOVER_URL "%s/movie/%s" "${RADARR_APPLICATION_URL}" "${tmdb_id}"
            else
                printf -v PUSHOVER_URL "%s" "${RADARR_APPLICATION_URL}"
            fi
            printf -v PUSHOVER_URL_TITLE "View Movie"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Health|HealthRestored)
            local level health_type health_msg
            level="$(_jq '.level // empty')"
            health_type="$(htmlesc "$(_jq '.type // "Health Check"')")"
            health_msg="$(htmlesc "$(_jq '.message // "No details available"')")"
            if [[ "${event_type}" == "HealthRestored" ]]; then
                printf -v PUSHOVER_TITLE "Radarr Health Restored"
                printf -v PUSHOVER_PRIORITY "low"
            elif [[ "${level}" == "error" ]]; then
                printf -v PUSHOVER_TITLE "Radarr Health Error"
                printf -v PUSHOVER_PRIORITY "high"
            else
                printf -v PUSHOVER_TITLE "Radarr Health Warning"
                printf -v PUSHOVER_PRIORITY "normal"
            fi
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${health_type}" "${health_msg}"
            printf -v PUSHOVER_URL "%s/system/status" "${RADARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View System Status"
            ;;
        *)
            exit 0
            ;;
    esac

    apprise --title "${PUSHOVER_TITLE}" --body "${PUSHOVER_MESSAGE}" --input-format html \
        "${RADARR_PUSHOVER_URL}?url=$(urienc "${PUSHOVER_URL}")&url_title=$(urienc "${PUSHOVER_URL_TITLE}")&priority=$(urienc "${PUSHOVER_PRIORITY}")&format=html"
}

function main() {
    notify
}

main "$@"
