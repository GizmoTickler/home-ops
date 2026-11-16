#!/usr/bin/env python3
"""Debounce and hand off GitHub webhook pokes via launcher.py (falling back to reconcile.py if unseeded)."""
import os
import time

LAUNCHER = "/mnt/flashstor/secure-scripts/apps-gitops/launcher.py"
RECONCILER = "/mnt/flashstor/secure-scripts/apps-gitops/home-ops/truenas/reconciler/reconcile.py"
TRIGGER = "/mnt/flashstor/apps/webhook-trigger/poke"

time.sleep(2)
entry = LAUNCHER if os.path.isfile(LAUNCHER) else RECONCILER
os.execv("/usr/bin/python3", [
    "/usr/bin/python3",
    entry,
    "--from-trigger",
    "--trigger-file", TRIGGER,
    "--wait-lock", "1800",
    "--if-locked", "fail",
])
