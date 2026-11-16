#!/usr/bin/env python3
"""Out-of-checkout supervisor for nas01 reconciler with N-1 fallback and emergency CI-gated self-heal.

Uses Python standard library only so third-party or reconciler package breakage never disables supervision.
"""

import argparse
import hashlib
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_CONFIG = "/mnt/flashstor/secure-scripts/apps-gitops/config.json"
DEFAULT_API_URL = "https://api.github.com"
DEFAULT_CHECK_NAME = "Lint nas01 apps"


def _log_line(state_dir, msg):
    line = f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} {msg}"
    print(line, file=sys.stderr, flush=True)
    if not state_dir or not os.path.isdir(state_dir):
        return
    logf = os.path.join(state_dir, "reconcile.log")
    try:
        fd = os.open(logf, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "a") as f:
            os.fchmod(f.fileno(), 0o600)
            f.write(line + "\n")
    except OSError:
        pass


def _atomic_json(path, obj):
    d = os.path.dirname(path)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".tmp-")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(obj, f, sort_keys=True)
            os.fchmod(f.fileno(), 0o600)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def _tree_digest(reconciler_dir):
    h = hashlib.sha256()
    files = []
    entry = os.path.join(reconciler_dir, "reconcile.py")
    if os.path.isfile(entry):
        files.append(("reconcile.py", entry))
    lib_dir = os.path.join(reconciler_dir, "reconciler_lib")
    if os.path.isdir(lib_dir):
        for fn in sorted(os.listdir(lib_dir)):
            if fn.endswith(".py"):
                p = os.path.join(lib_dir, fn)
                if os.path.isfile(p):
                    files.append((f"reconciler_lib/{fn}", p))
    for rel, path in files:
        h.update(rel.encode("utf-8"))
        h.update(b"\0")
        with open(path, "rb") as f:
            h.update(f.read())
        h.update(b"\0")
    return h.hexdigest()


def _read_lkg_meta(state_dir):
    meta_p = os.path.join(state_dir, "last-good-reconciler", "meta.json")
    try:
        with open(meta_p) as f:
            return json.load(f)
    except (FileNotFoundError, ValueError, OSError):
        return {}


def promote_last_good(checkout, state_dir):
    """Snapshot `<checkout>/truenas/reconciler` into `<state_dir>/last-good-reconciler/` cleanly."""
    rdir = os.path.join(checkout, "truenas", "reconciler")
    entry = os.path.join(rdir, "reconcile.py")
    if not os.path.isfile(entry) or not os.path.isdir(state_dir):
        return _read_lkg_meta(state_dir)
    sha = ""
    try:
        r = subprocess.run(["git", "-C", checkout, "rev-parse", "HEAD"], capture_output=True, text=True, timeout=30)
        if r.returncode == 0:
            sha = r.stdout.strip()
    except OSError:
        pass
    digest = _tree_digest(rdir)
    lkg_dir = os.path.join(state_dir, "last-good-reconciler")
    cur_meta = _read_lkg_meta(state_dir)
    if cur_meta.get("commit") == sha and cur_meta.get("digest") == digest and os.path.isfile(os.path.join(lkg_dir, "reconcile.py")):
        return cur_meta

    stage = tempfile.mkdtemp(dir=state_dir, prefix=".lkg-stage-")
    old_backup = None
    try:
        os.chmod(stage, 0o700)
        shutil.copy2(entry, os.path.join(stage, "reconcile.py"))
        os.chmod(os.path.join(stage, "reconcile.py"), 0o700)
        src_lib = os.path.join(rdir, "reconciler_lib")
        if os.path.isdir(src_lib):
            dst_lib = os.path.join(stage, "reconciler_lib")
            os.mkdir(dst_lib, 0o700)
            for fn in sorted(os.listdir(src_lib)):
                if fn.endswith(".py") and os.path.isfile(os.path.join(src_lib, fn)):
                    shutil.copy2(os.path.join(src_lib, fn), os.path.join(dst_lib, fn))
                    os.chmod(os.path.join(dst_lib, fn), 0o600)
        meta = {"commit": sha, "digest": digest, "saved_at": int(time.time())}
        fd = os.open(os.path.join(stage, "meta.json"), os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(meta, f, sort_keys=True)
        if os.path.exists(lkg_dir):
            old_backup = os.path.join(state_dir, f".lkg-old-{os.getpid()}-{int(time.time())}")
            os.replace(lkg_dir, old_backup)
        try:
            os.replace(stage, lkg_dir)
        except BaseException:
            if old_backup and os.path.exists(old_backup) and not os.path.exists(lkg_dir):
                os.replace(old_backup, lkg_dir)
            raise
        if old_backup and os.path.exists(old_backup):
            shutil.rmtree(old_backup, ignore_errors=True)
        return meta
    except BaseException:
        shutil.rmtree(stage, ignore_errors=True)
        raise


def write_launcher_metrics(cfg, fallback_active, last_good_commit=""):
    metrics_dir = cfg.get("metrics_dir")
    if not metrics_dir or not os.path.isdir(metrics_dir):
        return
    lines = [
        "# HELP truenas_apps_gitops_launcher_fallback_active 1 if candidate reconciler crashed and launcher ran pinned N-1 fallback.",
        "# TYPE truenas_apps_gitops_launcher_fallback_active gauge",
        f"truenas_apps_gitops_launcher_fallback_active {1 if fallback_active else 0}",
    ]
    if last_good_commit:
        safe_sha = str(last_good_commit)[:12].replace('"', "")
        lines += [
            "# HELP truenas_apps_gitops_launcher_last_good_info Commit SHA of the pinned last-good reconciler snapshot.",
            "# TYPE truenas_apps_gitops_launcher_last_good_info gauge",
            f'truenas_apps_gitops_launcher_last_good_info{{commit="{safe_sha}"}} 1',
        ]
    target = os.path.join(metrics_dir, "apps_gitops_launcher.prom")
    fd, tmp = tempfile.mkstemp(dir=metrics_dir, prefix=".apps_gitops_launcher.prom.")
    try:
        with os.fdopen(fd, "w") as f:
            f.write("\n".join(lines) + "\n")
            os.fchmod(f.fileno(), 0o644)
        os.replace(tmp, target)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def run_supervised(entry, forward_args):
    """Run candidate reconciler and verify completion nonce; returns (exit_code, clean_bool)."""
    if not os.path.isfile(entry):
        return 127, False
    nonce = secrets.token_hex(16)
    fd, nfile = tempfile.mkstemp(prefix=".apps-gitops-nonce-")
    os.close(fd)
    os.chmod(nfile, 0o600)
    env = dict(os.environ)
    env["RECONCILE_NONCE_FILE"] = nfile
    env["RECONCILE_RUN_NONCE"] = nonce
    try:
        p = subprocess.run([sys.executable, entry, *forward_args], env=env)
        try:
            with open(nfile) as f:
                got = f.read().strip()
        except OSError:
            got = ""
        return p.returncode, (got == nonce)
    finally:
        try:
            os.unlink(nfile)
        except FileNotFoundError:
            pass


def _resolve_connect_token(cfg, ref):
    if not ref or not ref.startswith("op://"):
        return None
    parts = ref[len("op://"):].split("/")
    if len(parts) not in (3, 4) or not cfg.get("connect_url") or not cfg.get("connect_token"):
        return None
    vault, item, field = parts[0], parts[1], parts[-1]
    section = parts[2] if len(parts) == 4 else None
    base = cfg["connect_url"].rstrip("/")
    hdr = {"Authorization": f"Bearer {cfg['connect_token']}"}

    def get_json(path):
        with urllib.request.urlopen(urllib.request.Request(base + path, headers=hdr), timeout=15) as r:
            return json.load(r)

    vs = get_json(f"/v1/vaults?filter={urllib.parse.quote(f'name eq \"{vault}\"')}")
    if not vs:
        return None
    vid = vs[0]["id"]
    if re.fullmatch(r"[a-z0-9]{26}", item):
        full = get_json(f"/v1/vaults/{vid}/items/{item}")
    else:
        its = get_json(f"/v1/vaults/{vid}/items?filter={urllib.parse.quote(f'title eq \"{item}\"')}")
        if not its:
            return None
        full = get_json(f"/v1/vaults/{vid}/items/{its[0]['id']}")
    sections = {s["id"]: s.get("label", "") for s in full.get("sections", [])}
    for f in full.get("fields", []):
        if f.get("label") != field and f.get("id") != field:
            continue
        if section is not None and sections.get((f.get("section") or {}).get("id"), "") != section:
            continue
        return f.get("value")
    return None


def _ci_check_passed(cfg, sha):
    gate = cfg.get("ci_gate") or {}
    if not gate.get("enabled", True):
        return True
    m = re.search(r"github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$", cfg.get("repo", ""))
    if not m:
        return False
    owner, repo = m.group(1), m.group(2)
    api_url = str(gate.get("api_url") or DEFAULT_API_URL).rstrip("/")
    check_name = str(gate.get("check_name") or DEFAULT_CHECK_NAME)
    url = f"{api_url}/repos/{owner}/{repo}/commits/{sha}/check-runs?" + urllib.parse.urlencode(
        {"check_name": check_name, "filter": "latest"}
    )
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "truenas-apps-launcher",
               "X-GitHub-Api-Version": "2022-11-28"}
    try:
        tok = _resolve_connect_token(cfg, gate.get("token_ref"))
        if tok:
            headers["Authorization"] = f"Bearer {tok}"
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=int(gate.get("timeout", 15))) as r:
            payload = json.load(r)
    except Exception:
        return False
    runs = payload.get("check_runs") or []
    if not runs:
        return False
    latest = max(runs, key=lambda item: (item.get("started_at") or "", item.get("id") or 0))
    return latest.get("status") == "completed" and latest.get("conclusion") == "success"


def emergency_self_heal(cfg, state_dir, no_ci_gate=False):
    """Fetch origin and advance broken checkout if a newer CI-passing fast-forward commit exists."""
    co = cfg.get("checkout", "")
    br = cfg.get("branch", "main")
    if not os.path.isdir(os.path.join(co, ".git")):
        return None
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")

    def git(*a):
        return subprocess.run(["git", "-C", co, *a], capture_output=True, text=True, env=env, timeout=120)

    if git("fetch", "--depth", "50", "origin", br).returncode != 0:
        return None
    cur_r = git("rev-parse", "HEAD")
    fet_r = git("rev-parse", "FETCH_HEAD")
    if cur_r.returncode != 0 or fet_r.returncode != 0:
        return None
    cur_head, fetch_head = cur_r.stdout.strip(), fet_r.stdout.strip()
    if not fetch_head or fetch_head == cur_head:
        return None
    if git("merge-base", "--is-ancestor", cur_head, fetch_head).returncode != 0:
        return None
    if not no_ci_gate and not _ci_check_passed(cfg, fetch_head):
        return None
    if git("reset", "--hard", fetch_head).returncode != 0:
        return None
    cache_p = os.path.join(state_dir, "ci-gate.json")
    try:
        with open(cache_p) as f:
            gs = json.load(f)
    except (FileNotFoundError, ValueError, OSError):
        gs = {}
    gs["deployed"] = fetch_head
    _atomic_json(cache_p, gs)
    _log_line(state_dir, f"launcher: self-healed checkout from {cur_head[:8]} to CI-passing {fetch_head[:8]}")
    return fetch_head


def main(argv=None):
    forward_args = list(sys.argv[1:] if argv is None else argv)
    ap = argparse.ArgumentParser(add_help=False)
    ap.add_argument("--self-check", action="store_true")
    ap.add_argument("--config", default=DEFAULT_CONFIG)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--app", action="append")
    ap.add_argument("--no-pull", action="store_true")
    ap.add_argument("--no-ci-gate", action="store_true")
    known, _ = ap.parse_known_args(forward_args)

    if known.self_check:
        print("launcher: self-check OK")
        return 0

    try:
        with open(known.config) as f:
            cfg = json.load(f)
    except OSError as e:
        print(f"launcher: failed reading config {known.config}: {e}", file=sys.stderr)
        return 2

    state_dir = cfg.get("state_dir", "")
    checkout = cfg.get("checkout", "")
    entry = os.path.join(checkout, "truenas", "reconciler", "reconcile.py")

    rc, clean = run_supervised(entry, forward_args)
    if clean:
        lkg_meta = _read_lkg_meta(state_dir)
        if not known.dry_run:
            if rc == 0 and not known.app:
                lkg_meta = promote_last_good(checkout, state_dir)
            write_launcher_metrics(cfg, fallback_active=False, last_good_commit=lkg_meta.get("commit", ""))
        return rc

    _log_line(state_dir, f"CRITICAL: candidate reconciler crashed (rc={rc})")

    if not known.dry_run and not known.no_pull:
        healed = emergency_self_heal(cfg, state_dir, no_ci_gate=known.no_ci_gate)
        if healed:
            retry_args = list(forward_args)
            if "--no-pull" not in retry_args:
                retry_args.append("--no-pull")
            retry_args = [x for x in retry_args if x != "--from-trigger"]
            rc2, clean2 = run_supervised(entry, retry_args)
            if clean2:
                lkg_meta = _read_lkg_meta(state_dir)
                if rc2 == 0 and not known.app:
                    lkg_meta = promote_last_good(checkout, state_dir)
                write_launcher_metrics(cfg, fallback_active=False, last_good_commit=lkg_meta.get("commit", ""))
                return rc2

    lkg_meta = _read_lkg_meta(state_dir)
    lkg_entry = os.path.join(state_dir, "last-good-reconciler", "reconcile.py")
    if not known.dry_run:
        write_launcher_metrics(cfg, fallback_active=True, last_good_commit=lkg_meta.get("commit", ""))
    if os.path.isfile(lkg_entry):
        sha8 = (lkg_meta.get("commit") or "unknown")[:8]
        _log_line(state_dir, f"launcher: executing pinned last-good-reconciler ({sha8}) with --no-pull")
        fb_args = [x for x in forward_args if x != "--from-trigger"]
        if "--no-pull" not in fb_args:
            fb_args.append("--no-pull")
        subprocess.run([sys.executable, lkg_entry, *fb_args])
    else:
        _log_line(state_dir, "CRITICAL: no pinned last-good-reconciler available for fallback")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
