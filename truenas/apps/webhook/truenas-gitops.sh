#!/bin/sh
# Wake nas01's host apps-gitops systemd.path watcher on a verified GitHub webhook delivery.
# Runs inside the unprivileged webhook container: zero payload bytes ever touch the host;
# this script only creates a 0-byte /trigger/poke file for main branch pushes or green CI.
set -eu

[ "${GH_REPO:-}" = "GizmoTickler/home-ops" ] || exit 0

case "${GITHUB_EVENT:-}" in
  ping)
    exit 0
    ;;
  push)
    [ "${GH_REF:-}" = "refs/heads/main" ] || exit 0
    ;;
  check_run)
    [ "${GH_ACTION:-}" = "completed" ] || exit 0
    [ "${GH_CHECK_NAME:-}" = "Lint nas01 apps" ] || exit 0
    [ "${GH_CHECK_CONCLUSION:-}" = "success" ] || exit 0
    [ "${GH_HEAD_BRANCH:-main}" = "main" ] || exit 0
    ;;
  *)
    exit 0
    ;;
esac

: > /trigger/poke
