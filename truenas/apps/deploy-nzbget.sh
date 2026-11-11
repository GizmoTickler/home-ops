#!/usr/bin/env bash
# Deploy/update nzbget on nas01 as a TrueNAS custom app.
#   1. render nzbget.conf from the template + 1Password item "nzbget" (Infrastructure vault)
#   2. install it into flashstor/apps/nzbget-config as 1000:1000 0600 (never written to git or local disk)
#   3. create or update the custom app from compose.yaml and restart it
# Needs: op (service account), ssh alias nas01 (truenas_admin with sudo).
set -euo pipefail
here="$(cd "$(dirname "$0")/nzbget" && pwd)"
ref() { op read "op://Infrastructure/nzbget/$1"; }
render() {
  local t; t="$(cat "$here/nzbget.conf.tmpl")"
  local k
  for k in SERVER1_NAME SERVER1_HOST SERVER1_USER SERVER1_PASS SERVER2_NAME SERVER2_HOST SERVER2_USER SERVER2_PASS \
           SERVER3_NAME SERVER3_HOST SERVER3_USER SERVER3_PASS SERVER4_NAME SERVER4_HOST SERVER4_USER SERVER4_PASS \
           RESTRICTED_USER RESTRICTED_PASS CONTROL_PASS; do
    t="${t//__${k}__/$(ref "$k")}"
  done
  if grep -q '__[A-Z0-9_]*__' <<<"$t"; then echo "unrendered placeholder left" >&2; exit 1; fi
  printf '%s\n' "$t"
}
render | ssh nas01 'sudo -n install -d -o 1000 -g 1000 -m 2775 /mnt/flashstor/apps/nzbget-config/queue /mnt/flashstor/apps/nzbget-config/nzb /mnt/flashstor/apps/nzbget-config/scripts \
  && f=/mnt/flashstor/apps/nzbget-config/nzbget.conf \
  && { [ -f "$f" ] && sudo -n cp -p "$f" "$f.prev" || true; } \
  && sudo -n tee "$f.new" >/dev/null && sudo -n chown 1000:1000 "$f.new" && sudo -n chmod 0600 "$f.new" && sudo -n mv "$f.new" "$f"'
# Create or update the custom app; payloads go over stdin ('-') to avoid shell quoting.
compose_json="$(python3 -c 'import json,sys,yaml;print(json.dumps(yaml.safe_load(open(sys.argv[1]))))' "$here/compose.yaml")"
if ssh nas01 "midclt call app.query '[[\"name\",\"=\",\"nzbget\"]]'" | grep -q '"name"'; then
  python3 -c 'import json,sys;print(json.dumps({"custom_compose_config":json.loads(sys.argv[1])}))' "$compose_json" \
    | ssh nas01 'midclt call -j app.update nzbget -' >/dev/null && echo updated
else
  python3 -c 'import json,sys;print(json.dumps({"app_name":"nzbget","custom_app":True,"custom_compose_config":json.loads(sys.argv[1])}))' "$compose_json" \
    | ssh nas01 'midclt call -j app.create -' >/dev/null && echo created
fi
ssh nas01 'midclt call -j app.redeploy nzbget' >/dev/null && echo redeployed
