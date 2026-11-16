"""CLI entrypoint, tree digest self-exec check, supervised nonce emission, and coalescing pass loop."""

import argparse, fcntl, hashlib, json, os, re, subprocess, sys

from . import runtime as rt


def reconciler_tree_digest(reconciler_dir):
    """Hash reconcile.py and all reconciler_lib/*.py files so any submodule update triggers self-exec."""
    h = hashlib.sha256()
    candidates = []
    entry = os.path.join(reconciler_dir, "reconcile.py")
    if os.path.isfile(entry):
        candidates.append(("reconcile.py", entry))
    lib_dir = os.path.join(reconciler_dir, "reconciler_lib")
    if os.path.isdir(lib_dir):
        for fn in sorted(os.listdir(lib_dir)):
            if fn.endswith(".py"):
                p = os.path.join(lib_dir, fn)
                if os.path.isfile(p):
                    candidates.append((f"reconciler_lib/{fn}", p))
    for rel, path in candidates:
        h.update(rel.encode("utf-8"))
        h.update(b"\0")
        with open(path, "rb") as f:
            h.update(f.read())
        h.update(b"\0")
    return h.hexdigest()


def _write_supervised_nonce():
    """Write supervisor completion nonce right before a controlled exit (skipped on uncaught crash or execv)."""
    nfile = os.environ.get("RECONCILE_NONCE_FILE")
    nonce = os.environ.get("RECONCILE_RUN_NONCE")
    if not nfile or not nonce:
        return
    try:
        fd = os.open(nfile, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write(nonce)
    except OSError:
        pass


def _main_inner(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=rt.DEFAULT_CONFIG)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--app", action="append")
    ap.add_argument("--no-pull", action="store_true")
    ap.add_argument("--no-ci-gate", action="store_true", help="emergency: deploy the branch tip without CI check")
    ap.add_argument("--wait-lock", type=int, default=None, help="seconds to wait for .lock (default 0, or 300 with --app)")
    ap.add_argument("--if-locked", choices=("skip", "fail"), default=None,
                    help="action on lock timeout (default skip, or fail with --app)")
    ap.add_argument("--trigger-file", default=None, help="coalesced webhook trigger file path")
    ap.add_argument("--from-trigger", action="store_true", help="exit 0 immediately if poke already consumed under lock")
    a = ap.parse_args(argv)
    t0 = rt._now()
    with open(a.config) as f:
        cfg = json.load(f)
    sd = cfg["state_dir"]
    os.makedirs(sd, mode=0o700, exist_ok=True)
    lfd = os.open(os.path.join(sd, ".lock"), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    lockf = os.fdopen(lfd, "r+")
    wait_s = a.wait_lock if a.wait_lock is not None else (300 if a.app else 0)
    on_locked = a.if_locked or ("fail" if a.app else "skip")
    lock_deadline = rt._now() + max(0, wait_s)
    while True:
        try:
            rt.fcntl.flock(lockf, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except BlockingIOError:
            if rt._now() >= lock_deadline:
                print("another reconcile is running", file=sys.stderr)
                lockf.close()
                return rt.EX_TEMPFAIL if on_locked == "fail" else 0
            rt._sleep(1)

    tpath = rt._trigger_path(cfg, a.trigger_file)
    reexec_env = os.environ.get("RECONCILE_GATE_INFO")
    from_self_exec = reexec_env is not None
    if tpath and not a.dry_run:
        try:
            had_poke = rt.consume_trigger(tpath)
        except Exception as e:  # noqa: BLE001
            print(f"trigger error: {e}", file=sys.stderr)
            had_poke = False
        if a.from_trigger and not from_self_exec and not a.no_pull and not had_poke:
            lockf.close()
            return 0

    ctx = rt.Ctx(cfg, a.dry_run)
    if not a.dry_run:
        os.fchmod(lfd, 0o600)
    rt.secure_state(cfg, ctx)
    ctx.state = rt.load_json(os.path.join(sd, "state.json"), {})
    me = os.path.realpath(rt.__file__)
    reconciler_dir = os.path.dirname(me)
    before_digest = reconciler_tree_digest(reconciler_dir)
    gate_on = rt.cfg_get(cfg, "ci_gate")["enabled"] and not a.no_ci_gate

    ok_all = True
    for pass_no in range(rt.MAX_COALESCE_PASSES):
        pass_t0 = rt._now() if pass_no > 0 else t0
        ctx.events = []
        ctx.drift = {}
        ctx.ui_changed = False
        ctx.connect_ok = True
        ctx.live = None
        run_info = {"gate": "nopull", "commit": "", "head": "", "api_ok": True, "connect_ok": True}
        deploy = True
        worktree = None
        should_pull = (not a.no_pull) or (pass_no > 0)
        if should_pull:
            gi = rt.git_pull(cfg, ctx, gate_on, partial=bool(a.app))
            deploy = gi.get("deploy", True)
            worktree = gi.get("worktree")
            run_info.update(gate=gi["status"], commit=gi["commit"], head=gi["head"], api_ok=gi["api_ok"], deploy=deploy)
            if not a.dry_run and not a.app and os.path.exists(me) and reconciler_tree_digest(reconciler_dir) != before_digest:
                # the pull updated reconcile.py or reconciler_lib/: re-exec new code (release lock first)
                ctx.log("reconciler updated by git -- re-executing new version")
                rt.write_runlog(cfg, ctx)
                lockf.close()
                os.environ["RECONCILE_GATE_INFO"] = json.dumps(run_info)
                os.execv(sys.executable, [sys.executable, me, *sys.argv[1:], "--no-pull"])
        elif reexec_env:
            run_info.update(json.loads(reexec_env))
            os.environ.pop("RECONCILE_GATE_INFO", None)
            reexec_env = None
            deploy = run_info.get("deploy", True)
        else:
            try:
                run_info["commit"] = subprocess.run(["git", "-C", cfg["checkout"], "rev-parse", "HEAD"],
                                                    capture_output=True, text=True, timeout=30).stdout.strip()
            except Exception:  # noqa: BLE001
                pass

        checkout = worktree or cfg["checkout"]
        try:
            root = os.path.join(checkout, "truenas", "apps")
            apps = sorted(d for d in os.listdir(root) if os.path.isfile(os.path.join(root, d, "compose.yaml")))
            all_apps = list(apps)
            if a.app:
                apps = [x for x in apps if x in a.app]
            ctx.public_tokens = set(all_apps)
            metas = {}
            for n in apps:
                try:
                    metas[n] = rt.load_app(os.path.join(root, n))[1]
                except Exception as e:  # noqa: BLE001
                    ctx.log(f"{n}: ERROR reading app.yaml: {e}")
                    raw_meta = rt.read_file(os.path.join(root, n, "app.yaml")) or ""
                    if re.search(r"(?m)^\s*critical\s*:\s*true\b", raw_meta):
                        om = re.search(r"(?m)^\s*order\s*:\s*(-?\d+)\b", raw_meta)
                        metas[n] = {"critical": True, "order": int(om.group(1)) if om else -1}
                    else:
                        metas[n] = {}
            results, unmanaged = {}, 0
            if not a.app and deploy:
                if not rt.reconcile_host(checkout, ctx):
                    results["_host"] = False
            if not rt.ensure_networks(os.path.join(checkout, "truenas", "networks.yaml"), ctx):
                results["_networks"] = False
            if deploy:
                results.update(rt.run_apps(root, apps, metas, ctx))
            else:
                ctx.log("ci-gate: nothing has passed CI yet on a fresh checkout -- no app reconciled")
                results["_ci_gate"] = False
            if not a.app and deploy:
                if not rt.reconcile_retired(checkout, ctx):
                    results["_retired"] = False
            if not a.app:
                live_names = {x["name"] for x in rt.midclt("app.query")}
                for extra in sorted(live_names - set(all_apps)):
                    ctx.log(f"{extra}: on nas01 but NOT in git (unmanaged)")
                    unmanaged += 1
            if ctx.ui_changed and not a.dry_run:
                try:
                    rt.midclt("app.metadata.generate", job=True)
                    ctx.log("UI metadata regenerated")
                except Exception as e:  # noqa: BLE001
                    ctx.log(f"UI metadata regenerate failed: {e}")
            try:
                removed, kept = rt.prune_backups(rt.backup_root(cfg), all_apps, rt.cfg_get(cfg, "backup_retention_days"),
                                                 rt.cfg_get(cfg, "backup_keep_newest"), rt._now(), a.dry_run, ctx.log)
                run_info["backup_bytes"] = kept
                ctx.log(f"backup: {len(removed)} file(s) {'would be ' if a.dry_run else ''}removed by retention; "
                        f"{kept // (1024 * 1024)} MiB kept")
            except Exception as e:  # noqa: BLE001
                ctx.log(f"backup: retention failed: {e}")
            ok_all = all(results.values())
            run_info["duration"] = rt._now() - pass_t0
            run_info["drift"] = ctx.drift
            run_info["connect_ok"] = ctx.connect_ok
            if deploy and ok_all and not a.app and not a.dry_run and len(run_info.get("commit", "")) == 40:
                ctx.state["converged_commit"] = run_info["commit"]
            run_info["converged"] = (ctx.state.get("converged_commit") == run_info.get("commit")) \
                if (deploy and run_info.get("commit")) else False
            if not a.dry_run:
                mstate = rt.update_metric_state(ctx.state.get("metrics", {}), ctx.events, results, ok_all, not a.app, int(rt._now()))
                ctx.state["metrics"] = mstate
                if not a.app:
                    rt.post_github_status(cfg, ctx, results, ok_all, run_info)
                rt.atomic_json(os.path.join(sd, "state.json"), ctx.state)
                if not a.app:
                    rt.write_metrics(cfg, results, unmanaged, ok_all, mstate, run_info)
            bad = sorted(k for k, v in results.items() if not v)
            ctx.log(f"run: {'OK' if ok_all else 'FAILED'} ({len(results) - len(bad)}/{len(results)} converged"
                    + (f"; not converged: {', '.join(bad)}" if bad else "") + f") in {run_info['duration']:.0f} s"
                    + (" (dry-run)" if a.dry_run else ""))
            rt.write_runlog(cfg, ctx)
        finally:
            rt.cleanup_worktree(cfg["checkout"], worktree)

        can_coalesce = (
            not a.dry_run and not a.app and tpath and rt.trigger_pending(tpath)
            and (not a.no_pull or from_self_exec) and pass_no + 1 < rt.MAX_COALESCE_PASSES
        )
        if not can_coalesce:
            break
        rt.consume_trigger(tpath)
        ctx.log("trigger poked during reconcile -- running coalesced pass")
    return 0 if ok_all else 1


def main(argv=None):
    rc_code = _main_inner(argv)
    _write_supervised_nonce()
    return rc_code
