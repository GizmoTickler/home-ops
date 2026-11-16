"""Per-app reconcile state machine, preflight/apply/rollback, rollback image pinning, and dependency ordering."""

import difflib, os, re

from . import runtime as rt


def pin_rollback_images(name, live_compose, desired_compose, ctx):
    """Best-effort local tag of previous images before updating so offline rollbacks do not need a pull."""
    if ctx.dry or not live_compose or live_compose == desired_compose:
        return
    desired_svcs = (desired_compose or {}).get("services") or {}
    for svc_name, old_svc in (live_compose.get("services") or {}).items():
        old_img = (old_svc or {}).get("image")
        new_img = (desired_svcs.get(svc_name) or {}).get("image")
        if old_img and old_img != new_img and rt.image_present(old_img):
            r = rt.sh(["docker", "image", "tag", old_img, f"ix-rollback/{name}/{svc_name}:prev"], timeout=30)
            if r.returncode != 0:
                ctx.log(f"{name}: warning pinning rollback image {svc_name}: {r.stderr.strip()[-200:]}")


def reconcile_app(appdir, ctx, meta=None):
    name = os.path.basename(appdir)
    src, m = rt.load_app(appdir)
    meta = m if meta is None else meta

    desired = ctx.render_obj(src)
    rendered_files = []
    for fs in meta.get("files", []):
        with open(os.path.join(appdir, fs["template"])) as f:
            content = ctx.render_str(f.read())
        rendered_files.append((fs, content))

    app = rt.app_state(name)
    live = rt.midclt("app.config", name) if app else None
    if app and not app.get("custom_app"):
        ctx.log(f"{name}: exists on nas01 as a CATALOG app; refusing to convert automatically (migrate by hand)")
        return False

    # drift: live state that differs from what this reconciler last applied = edited outside git (TrueNAS UI)
    applied = ctx.state.setdefault("applied", {})
    drifted = rt.drift_report(applied.get(name), rt.live_fingerprints(ctx, live, meta)) if app else []
    ctx.drift[name] = bool(drifted)
    if drifted:
        ctx.event("drift", name)
        ctx.log(f"{name}: DRIFT -- live {', '.join(drifted)} changed outside git since the last deploy"
                + (" (will be re-applied from git)" if not ctx.dry else ""))

    compose_diff = live != desired
    dirs_changed = any([rt.ensure_dir(d, ctx, name) for d in meta.get("dirs", [])])  # list: ensure ALL dirs

    file_changes = []
    for fs, content in rendered_files:
        cur = rt.read_file(fs["dest"])
        if cur != content:
            file_changes.append((fs, content, cur))

    ini_todo, ini_missing = [], []
    for spec in meta.get("ini", []):
        if not os.path.lexists(spec["path"]):
            ctx.log(f"{name}: ini file {spec['path']} missing (app not initialised yet?) -- skipped")
            continue
        ch, missing = rt.ini_changes(spec)
        for k in missing:
            # a pinned setting that matches nothing is a silent no-op -> fail the app (keys are verbatim, section-blind)
            ctx.log(f"{name}: ERROR ini key {k!r} not present in {spec['path']} -- app.yaml does not match the file")
            ini_missing.append(k)
        if ch:
            ini_todo.append((spec, ch))

    want_fp = rt.desired_fingerprints(ctx, desired, rendered_files, meta)
    pending = bool((ctx.state.get("pending_redeploy") or {}).get(name))
    prev_applied = applied.get(name) or {}
    unconverged = bool(app and prev_applied and (
        any((prev_applied.get("files") or {}).get(dest) not in (None, wfp)
            for dest, wfp in want_fp["files"].items())
        or any(os.path.lexists(ipath) and (prev_applied.get("ini") or {}).get(ipath) not in (None, wfp)
               for ipath, wfp in want_fp["ini"].items())
    ))

    def record_applied(dash_fp=None):
        if not ctx.dry:
            fp = rt.live_fingerprints(ctx, desired, meta)
            if dash_fp is not None:
                fp["dashboards"] = dash_fp
            elif "dashboards" in (applied.get(name) or {}):
                fp["dashboards"] = applied[name]["dashboards"]
            applied[name] = fp
            (ctx.state.get("pending_redeploy") or {}).pop(name, None)
            rt.save_state(ctx)

    if not (compose_diff or file_changes or ini_todo or dirs_changed or pending or unconverged):
        state = app.get("state") if app else None
        if ctx.dry:  # dry-run exercises every preflight, so a bad image/config/IP shows up before it is deployed
            ctx.log(f"{name}: config in sync, state is {state} (would start)" if state != "RUNNING"
                    else f"{name}: in sync ({state})")
            probs = rt.preflight(name, meta, desired, rendered_files, ctx)
            for p in probs:
                ctx.log(f"{name}: PREFLIGHT FAILED: {p}")
            if probs:
                ctx.event("preflight_failure", name)
                return False
            dok, _ = rt.reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
            if not dok:
                return False
            ctx.log(f"{name}: preflight OK")
            return not ini_missing
        if state != "RUNNING":
            ctx.log(f"{name}: config in sync, state is {state} -- starting")
            timeout = int(meta.get("deploy_timeout", rt.cfg_get(ctx.cfg, "deploy_timeout")))
            ctx.live = None
            try:
                rt.ensure_started(name, ctx)
                ok = rt.wait_running(name, ctx, timeout) and rt.healthy(meta, name, ctx)
            except Exception as e:  # noqa: BLE001
                ctx.log(f"{name}: start FAILED: {ctx.mask(str(e))}")
                ok = False
            if ok:
                dok, dash_fp = rt.reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
                if not dok:
                    ctx.event("deploy_failure", name)
                    return False
                ctx.log(f"{name}: started (RUNNING{', healthy' if meta.get('health') else ''})")
                record_applied(dash_fp)
                ctx.ui_changed |= rt.apply_ui(name, meta, desired, ctx)
                return not ini_missing
            ctx.event("deploy_failure", name)
            ctx.log(f"{name}: start/health FAILED (was {state})")
            return False
        dok, dash_fp = rt.reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
        if dok:
            ctx.log(f"{name}: in sync ({state})")
            record_applied(dash_fp)
        else:
            ctx.log(f"{name}: dashboard sync FAILED ({state})")
        ctx.ui_changed |= rt.apply_ui(name, meta, desired, ctx)
        return dok and (not ini_missing)

    # ---- report ----
    if (pending or unconverged) and not (compose_diff or file_changes or ini_todo or dirs_changed):
        ctx.log(f"{name}: recovering unfinished/unconverged config deploy")
    if compose_diff:
        paths = rt.changed_paths(live, desired) if live is not None else ["(app absent: create)"]
        ctx.log(f"{name}: compose differs at: " + ", ".join(paths[:40]) + (" ..." if len(paths) > 40 else "")
                + " (values not logged)")
    for fs, content, cur in file_changes:
        # never print file contents: templated files carry secrets, and the OLD values are not maskable
        old_l, new_l = (cur or "").splitlines(), content.splitlines()
        changed = sum(1 for x in difflib.ndiff(old_l, new_l) if x[:2] in ("+ ", "- "))
        ctx.log(f"{name}: file {fs['dest']} differs ({'absent' if cur is None else f'{changed} changed lines'};"
                f" contents not logged)")
    for spec, ch in ini_todo:
        ctx.log(f"{name}: ini {spec['path']}: " + ", ".join(f"{k}: {o!r} -> {n!r}" for k, (o, n) in ch.items()))

    # ---- preflight: nothing running is touched unless all checks pass ----
    probs = rt.preflight(name, meta, desired, rendered_files, ctx)
    for p in probs:
        ctx.log(f"{name}: PREFLIGHT FAILED: {p}")
    if probs:
        ctx.event("preflight_failure", name)
        ctx.log(f"{name}: not deployed (preflight)" + (" (dry-run)" if ctx.dry else ""))
        return False
    ctx.log(f"{name}: preflight OK")
    if ctx.dry:
        rt.reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
        ctx.log(f"{name}: would deploy (dry-run)")
        return True

    # ---- apply, with rollback ----
    # Every change -- compose, templated files AND ini patches -- is captured first so a rollback restores all of
    # them, and the app is explicitly (re)started at the end of both paths: on TrueNAS `app.update` does NOT start
    # an app that is stopped (2026-11-11: qbittorrent was left STOPPED after an ini patch + update).
    timeout = int(meta.get("deploy_timeout", rt.cfg_get(ctx.cfg, "deploy_timeout")))
    prev_files, prev_ini = [], []
    bdir = os.path.join(rt.backup_root(ctx.cfg), name)
    ctx.live = None  # anything we do now changes the live inventory
    ctx.event("deploy", name)
    ctx.state.setdefault("pending_redeploy", {})[name] = int(rt._now())
    rt.save_state(ctx)
    try:
        rt.pin_rollback_images(name, live, desired, ctx)
        if ini_todo and app:
            ctx.log(f"{name}: stopping to patch ini")
            rt.midclt("app.stop", name, job=True)
        for spec, _ in ini_todo:
            text, st = rt.read_file_st(spec["path"])
            prev_ini.append((spec["path"], text, st))
            rt.ini_apply(spec, bdir)
        for fs, content, cur in file_changes:
            prev_files.append((fs, cur))
            rt.write_file(fs["dest"], content, fs.get("owner"), int(str(fs.get("mode", "0600")), 8), bdir)
        if compose_diff:
            rt.deploy_compose(name, desired, exists=bool(app))
            ctx.log(f"{name}: compose {'updated' if app else 'created'}")
        elif file_changes or dirs_changed or pending or unconverged:
            rt.midclt("app.redeploy", name, job=True)
            ctx.log(f"{name}: redeployed for file changes")
        rt.ensure_started(name, ctx)
        if rt.wait_running(name, ctx, timeout) and rt.healthy(meta, name, ctx):
            dok, dash_fp = rt.reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
            if not dok:
                raise RuntimeError("dashboards sync failed")
            ctx.log(f"{name}: converged (RUNNING{', healthy' if meta.get('health') else ''})")
            record_applied(dash_fp)
            ctx.ui_changed |= rt.apply_ui(name, meta, desired, ctx)
            return not ini_missing
        raise RuntimeError("not RUNNING/healthy")
    except Exception as e:
        ctx.event("deploy_failure", name)
        ctx.log(f"{name}: deploy FAILED: {ctx.mask(str(e))} -- rolling back")
        rt.rollback(name, meta, app, live, prev_ini, prev_files, timeout, ctx)
        return False


def rollback(name, meta, app, live, prev_ini, prev_files, timeout, ctx):
    """Restore ini + files, then re-apply the exact previous compose with app.update (the same path as a forward
    deploy; it recreates the containers, so restored files are picked up too) and verify RUNNING + health.
    A failed *create* has no previous version: files are restored and nothing is redeployed."""
    if app is None or live is None:
        try:
            for fs, cur in prev_files:
                if cur is not None:
                    rt.write_file(fs["dest"], cur, fs.get("owner"), int(str(fs.get("mode", "0600")), 8))
        except Exception as e2:  # noqa: BLE001
            ctx.log(f"{name}: restoring files after failed create FAILED: {ctx.mask(str(e2))}")
        (ctx.state.get("pending_redeploy") or {}).pop(name, None)
        rt.save_state(ctx)
        a = rt.app_state(name)
        ctx.log(f"{name}: create failed; no previous version to roll back to"
                + (f" (app now exists in state {a.get('state')} -- needs attention)" if a else " (app absent)"))
        return False
    ctx.event("rollback", name)
    try:
        if prev_ini:
            rt.midclt("app.stop", name, job=True)
        for path, text, st in prev_ini:
            rt.write_file(path, text, f"{st.st_uid}:{st.st_gid}", st.st_mode & 0o777)
            ctx.log(f"{name}: restored {path}")
        for fs, cur in prev_files:
            if cur is not None:
                rt.write_file(fs["dest"], cur, fs.get("owner"), int(str(fs.get("mode", "0600")), 8))
        rt.deploy_compose(name, live, exists=True)
        ctx.log(f"{name}: re-applied previous compose (app.update)")
        rt.ensure_started(name, ctx)
        ok = rt.wait_running(name, ctx, timeout) and rt.healthy(meta, name, ctx)
    except Exception as e2:  # noqa: BLE001
        ctx.log(f"{name}: ROLLBACK FAILED: {ctx.mask(str(e2))}")
        ok = False
    if ok:
        ctx.state.setdefault("applied", {})[name] = rt.live_fingerprints(ctx, live, meta)
        (ctx.state.get("pending_redeploy") or {}).pop(name, None)
        rt.save_state(ctx)
        ctx.log(f"{name}: after rollback: RUNNING{', healthy' if meta.get('health') else ''}")
    else:
        ctx.event("rollback_failure", name)
        ctx.log(f"{name}: after rollback: NOT RUNNING/healthy -- ROLLBACK FAILED, needs attention")
    return ok


def order_apps(apps, metas):
    """Bootstrap apps first by (order, name), then non-critical apps by name, then critical apps by (order, name)."""
    boot = lambda a: bool((metas.get(a) or {}).get("bootstrap"))
    crit = lambda a: bool((metas.get(a) or {}).get("critical")) and not boot(a)
    b_apps = sorted((a for a in apps if boot(a)), key=lambda a: (int((metas[a] or {}).get("order", 100)), a))
    normal = sorted(a for a in apps if not boot(a) and not crit(a))
    critical = sorted((a for a in apps if crit(a)), key=lambda a: (int((metas[a] or {}).get("order", 100)), a))
    return b_apps + normal + critical


def run_apps(root, apps, metas, ctx):
    results = {}
    ordered = order_apps(apps, metas)
    for i, name in enumerate(ordered):
        try:
            results[name] = rt.reconcile_app(os.path.join(root, name), ctx, metas.get(name))
        except rt.ConnectUnavailableError as e:
            ctx.connect_ok = False
            ctx.log(f"{name}: 1Password Connect unavailable ({ctx.mask(str(e))}) -- aborting remaining apps")
            results[name] = False
            rest = ordered[i + 1:]
            for r in rest:
                results[r] = False
            break
        except Exception as e:  # noqa: BLE001
            ctx.log(f"{name}: ERROR {ctx.mask(str(e))}")
            results[name] = False
        m = metas.get(name) or {}
        if not results[name] and (m.get("critical") or m.get("bootstrap")):
            rest = ordered[i + 1:]
            ctx.log(f"{name}: {'BOOTSTRAP' if m.get('bootstrap') else 'CRITICAL'} app failed -- aborting the run"
                    + (f"; skipped: {', '.join(rest)}" if rest else ""))
            for r in rest:
                results[r] = False
            break
    return results
