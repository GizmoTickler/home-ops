#!/usr/bin/env bash
# Deploy/update qBittorrent on nas01 as a TrueNAS custom app.
#   1. pin the nas01-specific settings in qBittorrent.conf (idempotent):
#      WebUI on 8080; auth exemption only for the k8s nodes (Cilium masquerades pods to node IPs);
#      torrent traffic bound to the VPN address (no interface name: docker names it eth0/eth1).
#   2. create or update the custom app from compose.yaml and redeploy it.
# Needs: ssh alias nas01 (truenas_admin with sudo). No secrets: the WebUI password hash lives in the config.
set -euo pipefail
here="$(cd "$(dirname "$0")/qbittorrent" && pwd)"
conf=/mnt/flashstor/apps/qbittorrent-config/qBittorrent/qBittorrent.conf
ssh nas01 "sudo -n test -f $conf && sudo -n cp -p $conf $conf.prev && sudo -n sed -i \
  -e 's#^WebUI\\\\Port=.*#WebUI\\\\Port=8080#' \
  -e 's#^WebUI\\\\AuthSubnetWhitelist=.*#WebUI\\\\AuthSubnetWhitelist=127.0.0.1/32, 192.168.122.10/32, 192.168.122.11/32, 192.168.122.12/32#' \
  -e 's#^WebUI\\\\AuthSubnetWhitelistEnabled=.*#WebUI\\\\AuthSubnetWhitelistEnabled=true#' \
  -e 's#^Session\\\\Interface=.*#Session\\\\Interface=#' \
  -e 's#^Session\\\\InterfaceName=.*#Session\\\\InterfaceName=#' \
  -e 's#^Session\\\\InterfaceAddress=.*#Session\\\\InterfaceAddress=192.168.90.20#' \
  $conf && sudo -n grep -E '^(WebUI.(Port|AuthSubnetWhitelist)|Session.Interface(Name|Address)?)=' $conf"
compose_json="$(python3 -c 'import json,sys,yaml;print(json.dumps(yaml.safe_load(open(sys.argv[1]))))' "$here/compose.yaml")"
if ssh nas01 "midclt call app.query '[[\"name\",\"=\",\"qbittorrent\"]]'" | grep -q '"name"'; then
  python3 -c 'import json,sys;print(json.dumps({"custom_compose_config":json.loads(sys.argv[1])}))' "$compose_json" \
    | ssh nas01 'midclt call -j app.update qbittorrent -' >/dev/null && echo updated
else
  python3 -c 'import json,sys;print(json.dumps({"app_name":"qbittorrent","custom_app":True,"custom_compose_config":json.loads(sys.argv[1])}))' "$compose_json" \
    | ssh nas01 'midclt call -j app.create -' >/dev/null && echo created
fi
ssh nas01 'midclt call -j app.redeploy qbittorrent' >/dev/null && echo redeployed
