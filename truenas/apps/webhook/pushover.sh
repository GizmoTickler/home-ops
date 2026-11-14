#!/usr/bin/env bash
# Shared Pushover API helper for nas01 webhook scripts.
# Replaces Python `apprise` (~45 MiB RSS / multi-second import per call) with `jq` + `curl` (<5 MiB RSS).

function send_pushover() {
    local pover_url="${1:?Pushover URL is required}"
    local title="${2:-Notification}"
    local message="${3:-}"
    local url="${4:-}"
    local url_title="${5:-}"
    local priority_raw="${6:-normal}"

    local user_key app_token prio form_body
    if [[ ! "${pover_url}" =~ ^pover://([A-Za-z0-9]+)@([A-Za-z0-9]+)([/?].*)?$ ]]; then
        echo "[ERROR] invalid Pushover URL format" >&2
        return 1
    fi
    user_key="${BASH_REMATCH[1]}"
    app_token="${BASH_REMATCH[2]}"

    case "${priority_raw,,}" in
        lowest|-2)   prio="-2" ;;
        low|-1)      prio="-1" ;;
        normal|0|"") prio="0" ;;
        high|1)      prio="1" ;;
        emergency|2) prio="2" ;;
        *)           prio="0" ;;
    esac

    form_body="$(jq --raw-output --null-input \
        --arg token "${app_token}" \
        --arg user "${user_key}" \
        --arg title "${title}" \
        --arg message "${message}" \
        --arg url "${url}" \
        --arg url_title "${url_title}" \
        --arg priority "${prio}" \
        '
        ({
          token: $token,
          user: $user,
          title: ($title[0:250]),
          message: ((if ($message | length) > 0 then $message else $title end)[0:1024]),
          html: "1",
          priority: $priority
        }
        + (if ($url | length) > 0 then {url: ($url[0:512])} else {} end)
        + (if ($url_title | length) > 0 then {url_title: ($url_title[0:100])} else {} end)
        + (if $priority == "2" then {retry: "60", expire: "3600"} else {} end))
        | to_entries | map("\(.key)=\(.value | @uri)") | join("&")
        ')"

    curl -fsSL --connect-timeout 5 --max-time 15 --retry 2 --retry-delay 1 \
        --data-binary @- https://api.pushover.net/1/messages.json <<<"${form_body}" >/dev/null
}
