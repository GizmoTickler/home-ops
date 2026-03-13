#!/usr/bin/env bash
set -euo pipefail

# Receives ntopng webhook payload (JSON via stdin) and forwards to AlertManager
# ntopng format: {"version":"0.2","timestamp":N,"alerts":[{...},...]}
# AlertManager format: [{"labels":{"alertname":"...","severity":"..."},"annotations":{...}}]

ALERTMANAGER_URL="${ALERTMANAGER_URL:-http://vmalertmanager-stack.observability.svc.cluster.local:9093}"
PAYLOAD="${NTOPNG_PAYLOAD}"

if [[ -z "${PAYLOAD}" ]]; then
    echo "ERROR: No payload received"
    exit 1
fi

# Use jq to transform ntopng alerts into AlertManager format
AM_PAYLOAD=$(echo "${PAYLOAD}" | jq -c '
  [.alerts[]? // empty | {
    labels: {
      alertname: ("ntopng_" + (.alert_entity // "unknown") + "_" + (.alert_id // .type // "alert" | tostring)),
      severity: (
        if (.severity // 5) <= 3 then "critical"
        elif (.severity // 5) <= 5 then "warning"
        else "info"
        end
      ),
      source: "ntopng",
      instance: (.ip // .cli_ip // .srv_ip // "unknown"),
      alert_type: (.type // .alert_id // "unknown" | tostring),
      entity: (.alert_entity // "unknown"),
      interface: (.ifname // .ifid // "unknown" | tostring),
      vlan: (.vlan_id // 0 | tostring)
    },
    annotations: {
      summary: (.msg // .alert_name // "ntopng alert"),
      description: (. | tostring | .[0:1024])
    },
    startsAt: ((.tstamp // .first_seen // now) | todate),
    endsAt: ((.tstamp_end // .last_seen // (now + 300)) | todate)
  }]
')

# Skip if no alerts were produced
if [[ "${AM_PAYLOAD}" == "[]" || -z "${AM_PAYLOAD}" ]]; then
    echo "No alerts to forward"
    exit 0
fi

curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "${AM_PAYLOAD}" \
    "${ALERTMANAGER_URL}/api/v2/alerts"

echo "Forwarded $(echo "${AM_PAYLOAD}" | jq 'length') alerts to AlertManager"
