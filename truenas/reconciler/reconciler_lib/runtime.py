"""Shared runtime namespace uniting all reconciler_lib modules and patchable globals."""

import fcntl, os, re, time, urllib, urllib.request  # noqa: F401 - urllib exported for test monkeypatching

DEFAULT_CONFIG = "/mnt/flashstor/secure-scripts/apps-gitops/config.json"
DEFAULT_TRIGGER_FILE = "/mnt/flashstor/apps/webhook-trigger/poke"
APPS_EAST_WEST_SCRIPT = "/mnt/flashstor/secure-scripts/apps-gitops/apps-east-west-nft.py"
EX_TEMPFAIL = 75
MAX_COALESCE_PASSES = 3
REF = re.compile(r"\{\{\s*(op://[^}\s]+)\s*\}\}")
ITEM_ID = re.compile(r"[a-z0-9]{26}")
RUN_TIMEOUT = 600
SETTLE = 20
POLL = 5
MAX_FILE = 4 * 1024 * 1024
BACKUP_TS = re.compile(r"\.(\d{8}T\d{6}Z)$")
IX_APP_CONFIGS = "/mnt/.ix-apps/app_configs"

CONFIG_DEFAULTS = {
    "branch": "main",
    "metrics_dir": None,
    "trigger_file": None,
    "min_ready_seconds": 0,
    "deploy_timeout": RUN_TIMEOUT,
    "backup_dir": None,
    "backup_retention_days": 14,
    "backup_keep_newest": 5,
    "log_max_bytes": 5_000_000,
    "log_keep": 5,
    "preflight": {"enabled": True, "pull_images": True, "probe_lan": True},
    "ci_gate": {"enabled": True, "check_name": "Lint nas01 apps", "api_url": "https://api.github.com",
                "history_depth": 50, "timeout": 15, "token_ref": None},
    "github_status": {"enabled": True, "context": "nas01/apps-gitops", "api_url": "https://api.github.com",
                      "timeout": 15, "token_ref": None},
}

COUNTERS = {
    "deploy": ("deploys", "Deploys attempted (compose/file/ini changes applied)."),
    "deploy_failure": ("deploy_failures", "Deploys that failed (and were rolled back)."),
    "rollback": ("rollbacks", "Rollbacks attempted."),
    "rollback_failure": ("rollback_failures", "Rollbacks that did not end RUNNING and healthy."),
    "preflight_failure": ("preflight_failures", "Deploys refused by preflight (nothing was touched)."),
    "drift": ("drift_detected", "Live app state found changed outside git."),
}

_now = time.time
_sleep = time.sleep

from . import context as _context  # noqa: E402
from . import middleware as _middleware  # noqa: E402
from . import files as _files  # noqa: E402
from . import preflight as _preflight  # noqa: E402
from . import grafana as _grafana  # noqa: E402
from . import deploy as _deploy  # noqa: E402
from . import gate as _gate  # noqa: E402
from . import host as _host  # noqa: E402
from . import housekeeping as _housekeeping  # noqa: E402
from . import cli as _cli  # noqa: E402

ConnectUnavailableError = _context.ConnectUnavailableError
cfg_get = _context.cfg_get
Ctx = _context.Ctx

sh = _middleware.sh
midclt = _middleware.midclt
app_state = _middleware.app_state
ensure_started = _middleware.ensure_started
container_status = _middleware.container_status
wait_running = _middleware.wait_running
container_pid = _middleware.container_pid
container_restarts = _middleware.container_restarts
_http_probe = _middleware._http_probe
healthy = _middleware.healthy
load_networks = _middleware.load_networks
ensure_networks = _middleware.ensure_networks
deploy_compose = _middleware.deploy_compose

parse_owner = _files.parse_owner
ensure_dir = _files.ensure_dir
read_file_st = _files.read_file_st
read_file = _files.read_file
backup_name = _files.backup_name
write_file = _files.write_file
ini_values = _files.ini_values
ini_changes = _files.ini_changes
ini_apply = _files.ini_apply
_svc_tag = _files._svc_tag
image_tag = _files.image_tag
apply_ui = _files.apply_ui
atomic_json = _files.atomic_json
load_json = _files.load_json
backup_root = _files.backup_root
save_state = _files.save_state
_fp_key = _files._fp_key
fingerprint = _files.fingerprint
live_fingerprints = _files.live_fingerprints
desired_fingerprints = _files.desired_fingerprints
drift_report = _files.drift_report
yaml_dump = _files.yaml_dump
changed_paths = _files.changed_paths
load_app = _files.load_app

Live = _preflight.Live
_parse_addr = _preflight._parse_addr
collect_live = _preflight.collect_live
compose_networks = _preflight.compose_networks
desired_ips = _preflight.desired_ips
desired_ports = _preflight.desired_ports
lan_in_use = _preflight.lan_in_use
conflicts = _preflight.conflicts
images = _preflight.images
image_present = _preflight.image_present
preflight_tmp = _preflight.preflight_tmp
write_secret_file = _preflight.write_secret_file
compose_config_check = _preflight.compose_config_check
_stage_validate_dir = _preflight._stage_validate_dir
run_validate = _preflight.run_validate
preflight = _preflight.preflight

_env_lookup = _grafana._env_lookup
_grafana_sync = _grafana._grafana_sync
reconcile_dashboards = _grafana.reconcile_dashboards

pin_rollback_images = _deploy.pin_rollback_images
reconcile_app = _deploy.reconcile_app
rollback = _deploy.rollback
order_apps = _deploy.order_apps
run_apps = _deploy.run_apps

GateAPIError = _gate.GateAPIError
check_verdict = _gate.check_verdict
repo_slug = _gate.repo_slug
make_checker = _gate.make_checker
gate_decide = _gate.gate_decide
git_pull = _gate.git_pull
_post_commit_status = _gate._post_commit_status
post_github_status = _gate.post_github_status
cleanup_worktree = _gate.cleanup_worktree

_trigger_path = _host._trigger_path
_open_trigger_dir = _host._open_trigger_dir
trigger_pending = _host.trigger_pending
consume_trigger = _host.consume_trigger
reconcile_host = _host.reconcile_host
sync_apps_east_west = _host.sync_apps_east_west
reconcile_retired = _host.reconcile_retired

secure_state = _housekeeping.secure_state
prune_backups = _housekeeping.prune_backups
rotate_log = _housekeeping.rotate_log
append_log = _housekeeping.append_log
write_runlog = _housekeeping.write_runlog
update_metric_state = _housekeeping.update_metric_state
esc = _housekeeping.esc
render_metrics = _housekeeping.render_metrics
write_metrics = _housekeeping.write_metrics

reconciler_tree_digest = _cli.reconciler_tree_digest
_write_supervised_nonce = _cli._write_supervised_nonce
main = _cli.main
