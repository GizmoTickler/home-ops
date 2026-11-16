#!/usr/bin/env python3
"""Debounce and hand off GitHub webhook pokes to apps-gitops reconciler (poke is consumed inside .lock)."""
import os
import time

RECONCILER = "/mnt/flashstor/secure-scripts/apps-gitops/home-ops/truenas/reconciler/reconcile.py"
TRIGGER = "/mnt/flashstor/apps/webhook-trigger/poke"

time.sleep(2)
os.execv("/usr/bin/python3", [
    "/usr/bin/python3",
    RECONCILER,
    "--from-trigger",
    "--trigger-file", TRIGGER,
    "--wait-lock", "1800",
    "--if-locked", "fail",
])
