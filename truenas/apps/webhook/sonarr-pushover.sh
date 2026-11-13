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

SONARR_PUSHOVER_URL="${1:?}"
PAYLOAD="${2:?}"

SONARR_APPLICATION_URL="${SONARR_APPLICATION_URL:?SONARR_APPLICATION_URL env var is required}"

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
            printf -v PUSHOVER_URL "%s" "${SONARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View Series"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        ManualInteractionRequired)
            local series_title download_client details
            series_title="$(htmlesc "$(_jq '.series.title // "Unknown Series"')")"
            download_client="$(htmlesc "$(_jq '.downloadClient // "Unknown"')")"
            details="$(htmlesc "$(_jq '[.downloadStatusMessages[]?.messages[]?] | join("; ")')")"
            printf -v PUSHOVER_TITLE "Episode Requires Manual Interaction"
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n<b>Client:</b> %s" \
                "${series_title}" "${download_client}"
            if [[ -n "${details}" ]]; then
                PUSHOVER_MESSAGE+=$'\n'"${details}"
            fi
            PUSHOVER_MESSAGE+="</small>"
            printf -v PUSHOVER_URL "%s/activity/queue" "${SONARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View Queue"
            printf -v PUSHOVER_PRIORITY "high"
            ;;
        Download|ImportComplete)
            local is_upgrade series_title series_slug download_client ep_count
            is_upgrade="$(_jq 'if .isUpgrade == true then "true" else "false" end')"
            series_title="$(htmlesc "$(_jq '.series.title // "Unknown Series"')")"
            series_slug="$(_jq '.series.titleSlug // empty')"
            download_client="$(htmlesc "$(_jq '.downloadClient // "Unknown"')")"
            ep_count="$(_jq '(.episodes // []) | length')"

            if [[ "${event_type}" == "ImportComplete" && "${ep_count}" -gt 1 ]]; then
                printf -v PUSHOVER_TITLE "Season Pack Added"
            elif [[ "${is_upgrade}" == "true" ]]; then
                if [[ "${ep_count}" -gt 1 ]]; then
                    printf -v PUSHOVER_TITLE "Episodes Upgraded"
                else
                    printf -v PUSHOVER_TITLE "Episode Upgraded"
                fi
            else
                if [[ "${ep_count}" -gt 1 ]]; then
                    printf -v PUSHOVER_TITLE "Episodes Added"
                else
                    printf -v PUSHOVER_TITLE "Episode Added"
                fi
            fi

            if [[ "${ep_count}" -gt 1 ]]; then
                local season_count ep_label
                season_count="$(_jq '[.episodes[].seasonNumber // empty] | unique | length')"
                if [[ "${season_count}" -eq 1 ]]; then
                    local season_num ep_min ep_max
                    season_num="$(_jq '(.episodes[0].seasonNumber // 0) | tonumber')"
                    ep_min="$(_jq '[.episodes[].episodeNumber // 0] | min | tonumber')"
                    ep_max="$(_jq '[.episodes[].episodeNumber // 0] | max | tonumber')"
                    printf -v ep_label "S%02dE%02d-E%02d (%d episodes)" \
                        "$((10#${season_num}))" "$((10#${ep_min}))" "$((10#${ep_max}))" "${ep_count}"
                else
                    printf -v ep_label "%d episodes" "${ep_count}"
                fi
                printf -v PUSHOVER_MESSAGE "<b>%s (%s)</b><small>\n\n<b>Client:</b> %s</small>" \
                    "${series_title}" "${ep_label}" "${download_client}"
            elif [[ "${ep_count}" -eq 1 ]]; then
                local season_num ep_num ep_title
                season_num="$(_jq '(.episodes[0].seasonNumber // 0) | tonumber')"
                ep_num="$(_jq '(.episodes[0].episodeNumber // 0) | tonumber')"
                ep_title="$(htmlesc "$(_jq '.episodes[0].title // empty')")"
                printf -v PUSHOVER_MESSAGE "<b>%s (S%02dE%02d)</b>" \
                    "${series_title}" "$((10#${season_num}))" "$((10#${ep_num}))"
                if [[ -n "${ep_title}" ]]; then
                    PUSHOVER_MESSAGE+="<small>"$'\n'"${ep_title}</small>"
                fi
                PUSHOVER_MESSAGE+="<small>"$'\n\n'"<b>Client:</b> ${download_client}</small>"
            else
                printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n\n<b>Client:</b> %s</small>" \
                    "${series_title}" "${download_client}"
            fi

            if [[ -n "${series_slug}" ]]; then
                printf -v PUSHOVER_URL "%s/series/%s" "${SONARR_APPLICATION_URL}" "${series_slug}"
            else
                printf -v PUSHOVER_URL "%s" "${SONARR_APPLICATION_URL}"
            fi
            printf -v PUSHOVER_URL_TITLE "View Series"
            printf -v PUSHOVER_PRIORITY "low"
            ;;
        Health|HealthRestored)
            local level health_type health_msg
            level="$(_jq '.level // empty')"
            health_type="$(htmlesc "$(_jq '.type // "Health Check"')")"
            health_msg="$(htmlesc "$(_jq '.message // "No details available"')")"
            if [[ "${event_type}" == "HealthRestored" ]]; then
                printf -v PUSHOVER_TITLE "Sonarr Health Restored"
                printf -v PUSHOVER_PRIORITY "low"
            elif [[ "${level}" == "error" ]]; then
                printf -v PUSHOVER_TITLE "Sonarr Health Error"
                printf -v PUSHOVER_PRIORITY "high"
            else
                printf -v PUSHOVER_TITLE "Sonarr Health Warning"
                printf -v PUSHOVER_PRIORITY "normal"
            fi
            printf -v PUSHOVER_MESSAGE "<b>%s</b><small>\n%s</small>" "${health_type}" "${health_msg}"
            printf -v PUSHOVER_URL "%s/system/status" "${SONARR_APPLICATION_URL}"
            printf -v PUSHOVER_URL_TITLE "View System Status"
            ;;
        *)
            exit 0
            ;;
    esac

    apprise --title "${PUSHOVER_TITLE}" --body "${PUSHOVER_MESSAGE}" --input-format html \
        "${SONARR_PUSHOVER_URL}?url=$(urienc "${PUSHOVER_URL}")&url_title=$(urienc "${PUSHOVER_URL_TITLE}")&priority=$(urienc "${PUSHOVER_PRIORITY}")&format=html"
}

function main() {
    notify
}

main "$@"
