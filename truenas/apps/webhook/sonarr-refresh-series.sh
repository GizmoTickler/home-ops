#!/usr/bin/env bash
set -euo pipefail

# Sonarr API target comes from env (fixed), never from the request; the series id must be numeric.
SONARR_URL="${SONARR_URL:?SONARR_URL env var is required}"
if [[ "${SONARR_EVENT_TYPE:-}" != "Test" && ! "${SONARR_SERIES_ID:-}" =~ ^[0-9]+$ ]]; then
    echo "[ERROR] invalid series id"; exit 1
fi

function refresh() {
    if [[ "${SONARR_EVENT_TYPE}" == "Test" ]]; then
        echo "[DEBUG] test event received, nothing to do ..."
    elif [[ "${SONARR_EVENT_TYPE}" == "Grab" ]]; then
        episodes=$(
            curl -fsSL --header "X-Api-Key: ${SONARR_API_KEY}" "${SONARR_URL}/api/v3/episode?seriesId=${SONARR_SERIES_ID}" |
                jq --raw-output '[.[] | select((.title == "TBA") or (.title == "TBD"))] | length'
        )
        if ((episodes > 0)); then
            echo "[INFO] episode titles found with TBA/TBD titles, refreshing series ${SONARR_SERIES_TITLE} ..."
            curl -fsSL --request POST \
                --header "X-Api-Key: ${SONARR_API_KEY}" \
                --header "Content-Type: application/json" \
                --data-binary "$(jo name=RefreshSeries seriesId="${SONARR_SERIES_ID}")" \
                "${SONARR_URL}/api/v3/command" &>/dev/null
        fi
    fi
}

function main() {
    refresh
}

main "$@"
