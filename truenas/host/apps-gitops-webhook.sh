#!/bin/sh
# Register systemd.path watcher in /run/systemd/system for apps-gitops webhook trigger.
# TrueNAS wipes /etc/systemd/system on upgrades, so POSTINIT recreates these runtime units at boot.
set -eu

TRIG_DIR="/mnt/flashstor/apps/webhook-trigger"
UNIT_DIR="/run/systemd/system"

if [ -L "$TRIG_DIR" ]; then
    echo "apps-gitops-webhook: refusing symlink at $TRIG_DIR" >&2
    exit 1
fi
mkdir -p "$TRIG_DIR"
chown 1000:1000 "$TRIG_DIR"
chmod 0700 "$TRIG_DIR"

mkdir -p "$UNIT_DIR"

cat > "$UNIT_DIR/apps-gitops-webhook.service" <<'EOF'
[Unit]
Description=apps-gitops webhook-triggered reconcile
After=network-online.target docker.service middlewared.service
StartLimitIntervalSec=0

[Service]
Type=oneshot
User=root
Group=root
ExecStart=/usr/bin/python3 /mnt/flashstor/secure-scripts/apps-gitops/trigger-run.py
TimeoutStartSec=2700
EOF

cat > "$UNIT_DIR/apps-gitops-webhook.path" <<'EOF'
[Unit]
Description=Watch /mnt/flashstor/apps/webhook-trigger/poke for GitHub webhook deliveries
StartLimitIntervalSec=0

[Path]
PathExists=/mnt/flashstor/apps/webhook-trigger/poke
Unit=apps-gitops-webhook.service

[Install]
WantedBy=multi-user.target
EOF

cat > "$UNIT_DIR/apps-east-west-nft.service" <<'EOF'
[Unit]
Description=Enforce L2 East-West nftables isolation on Docker apps bridge
After=docker.service middlewared.service
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
User=root
Group=root
ExecStart=/usr/bin/python3 /mnt/flashstor/secure-scripts/apps-gitops/apps-east-west-nft.py --apply

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl reset-failed apps-gitops-webhook.service apps-gitops-webhook.path apps-east-west-nft.service 2>/dev/null || true
systemctl start apps-gitops-webhook.path
systemctl start apps-east-west-nft.service || true
