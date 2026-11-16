"""State directory permissions, backup retention, log rotation, and node-exporter metrics emission."""

import os, stat, tempfile

from . import runtime as rt


def secure_state(cfg, ctx):
    """State dirs root-only (0700), never following symlinks; report (never touch) group/world-readable files."""
    sd = cfg["state_dir"]
    dirs = [sd]
    br = rt.backup_root(cfg)
    if os.path.isdir(br) and not os.path.islink(br):
        dirs.append(br)
        for e in os.scandir(br):
            if e.is_dir(follow_symlinks=False):
                dirs.append(e.path)
    for d in dirs:
        st = os.lstat(d)
        if not stat.S_ISDIR(st.st_mode) or st.st_mode & 0o077 == 0:
            continue
        if ctx.dry:
            ctx.log(f"state: {d} is {oct(st.st_mode & 0o777)} -- would chmod 0700 (dry-run)")
            continue
        fd = os.open(d, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fchmod(fd, 0o700)
        finally:
            os.close(fd)
        ctx.log(f"state: {d} {oct(st.st_mode & 0o777)} -> 0700")
    for e in os.scandir(sd):
        st = e.stat(follow_symlinks=False)
        if stat.S_ISREG(st.st_mode) and st.st_mode & 0o077 and e.name != ".lock":
            ctx.log(f"state: WARNING {e.path} is {oct(st.st_mode & 0o777)} (group/world-accessible) -- "
                    "not reconciler-managed; fix or delete by hand")


def prune_backups(bdir, app_names, days, keep, now, dry, log=lambda m: None):
    """Retention for the backup dir: keep every file younger than `days`, and always the newest `keep` versions
    of each backed-up file (<app>/<path>.<ts>.prev; legacy <path>.prev) and of each app's loose top-level files
    (grouped by app-name prefix). Regular files only, never follows symlinks, never removes directories.
    Returns (removed paths, bytes remaining)."""
    if not os.path.isdir(bdir) or os.path.islink(bdir):
        return [], 0
    groups = {}

    def add(key, entry):
        st = entry.stat(follow_symlinks=False)
        if stat.S_ISREG(st.st_mode):
            groups.setdefault(key, []).append((st.st_mtime, entry.path, st.st_blocks * 512 or st.st_size))
    names = sorted(app_names, key=len, reverse=True)
    for e in os.scandir(bdir):
        if e.is_dir(follow_symlinks=False):
            for f in os.scandir(e.path):
                base = f.name[:-5] if f.name.endswith(".prev") else f.name
                add((e.name, rt.BACKUP_TS.sub("", base)), f)
        elif e.is_file(follow_symlinks=False):
            app = next((a for a in names if e.name.startswith(a) and e.name[len(a):len(a) + 1] in ("-", ".", "_")),
                       "_unknown")
            add(("_loose", app), e)
    removed, remaining = [], 0
    cutoff = now - days * 86400
    for key, files in groups.items():
        files.sort(reverse=True)
        for i, (mt, path, size) in enumerate(files):
            if i >= keep and mt < cutoff:
                removed.append(path)
                log(f"backup: {'would remove' if dry else 'removing'} {path} ({size // 1024} KiB, "
                    f"{int((now - mt) // 86400)} d old)")
                if not dry:
                    os.unlink(path)
            else:
                remaining += size
    return removed, remaining


def rotate_log(logf, max_bytes, keep):
    try:
        if os.path.getsize(logf) <= max_bytes:
            return
    except FileNotFoundError:
        return
    for i in range(keep - 1, 0, -1):
        if os.path.exists(f"{logf}.{i}"):
            os.replace(f"{logf}.{i}", f"{logf}.{i + 1}")
    os.replace(logf, f"{logf}.1")
    for extra in range(keep + 1, keep + 10):  # a lowered log_keep drops the surplus
        try:
            os.unlink(f"{logf}.{extra}")
        except FileNotFoundError:
            break


def append_log(logf, lines):
    fd = os.open(logf, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "a") as f:
        os.fchmod(f.fileno(), 0o600)
        f.write("\n".join(lines) + "\n")


def write_runlog(cfg, ctx):
    logf = os.path.join(cfg["state_dir"], "reconcile.log")
    rt.rotate_log(logf, int(rt.cfg_get(cfg, "log_max_bytes")), int(rt.cfg_get(cfg, "log_keep")))
    rt.append_log(logf, ctx.log_lines)
    ctx.log_lines = []


def update_metric_state(mstate, events, results, ok_all, full_run, now):
    """Fold this run's events into the persisted counters/timestamps. Pure (testable)."""
    c, ts = mstate.setdefault("counters", {}), mstate.setdefault("last", {})
    rbf = mstate.setdefault("rollback_failed", {})
    for kind, app in events:
        c.setdefault(kind, {})
        c[kind][app] = c[kind].get(app, 0) + 1
        ts.setdefault(kind, {})[app] = now
        if kind == "rollback_failure":
            rbf[app] = 1
    for app, ok in results.items():
        if ok:
            rbf.pop(app, None)
    if full_run:
        mstate["consecutive_failed_runs"] = 0 if ok_all else mstate.get("consecutive_failed_runs", 0) + 1
    return mstate


def esc(v):
    return str(v).replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def render_metrics(results, unmanaged, ok_all, now, mstate, run_info, prev_success=None):
    out = []

    def fam(name, typ, help_, samples):
        out.append(f"# HELP truenas_apps_gitops_{name} {help_}")
        out.append(f"# TYPE truenas_apps_gitops_{name} {typ}")
        for labels, v in samples:
            lab = ",".join(f'{k}="{esc(x)}"' for k, x in labels.items())
            out.append(f"truenas_apps_gitops_{name}{{{lab}}} {v}" if lab else f"truenas_apps_gitops_{name} {v}")

    fam("last_run_timestamp_seconds", "gauge", "Last reconciler run.", [({}, now)])
    real_results = {a: ok for a, ok in results.items() if not a.startswith("_")}
    fam("app_ok", "gauge", "1 if the app converged to git on the last run.",
        [({"app": a}, 1 if ok else 0) for a, ok in sorted(real_results.items())])
    fam("unmanaged_apps", "gauge", "Apps on nas01 not present in git.", [({}, unmanaged)])
    succ = now if ok_all else prev_success
    if succ is not None:
        fam("last_success_timestamp_seconds", "gauge", "Last run in which every app converged.", [({}, succ)])
    fam("run_duration_seconds", "gauge", "Duration of the last run.", [({}, round(run_info.get("duration", 0), 1))])
    fam("consecutive_failed_runs", "gauge", "Runs in a row in which at least one app did not converge.",
        [({}, mstate.get("consecutive_failed_runs", 0))])
    fam("commit_info", "gauge", "Commit the reconciler deployed on the last run (head = branch tip).",
        [({"commit": run_info.get("commit", "")[:12], "head": run_info.get("head", "")[:12],
           "gate": run_info.get("gate", "")}, 1)])
    fam("ci_gate_api_ok", "gauge", "0 if the GitHub check-runs API could not be queried (gate failed closed).",
        [({}, 1 if run_info.get("api_ok", True) else 0)])
    fam("ci_gate_held", "gauge", "1 if the branch tip's truenas/ changes are held back (CI failed/pending/missing).",
        [({}, 1 if run_info.get("gate") == "held" else 0)])
    fam("connect_ok", "gauge", "1 if 1Password Connect API was healthy during the run.",
        [({}, 1 if run_info.get("connect_ok", True) else 0)])
    fam("commit_converged", "gauge", "1 if all apps converged onto the checked-out commit.",
        [({}, 1 if run_info.get("converged", ok_all) else 0)])
    c, ts = mstate.get("counters", {}), mstate.get("last", {})
    apps = sorted(set(real_results) | {a for k in c.values() for a in k if not a.startswith("_")})
    for kind, (stem, help_) in rt.COUNTERS.items():
        fam(f"{stem}_total", "counter", help_, [({"app": a}, c.get(kind, {}).get(a, 0)) for a in apps])
    for kind, (stem, help_) in rt.COUNTERS.items():
        last = {a: t for a, t in ts.get(kind, {}).items() if not a.startswith("_")}
        if last:
            fam(f"last_{kind}_timestamp_seconds", "gauge", f"Last time: {help_}",
                [({"app": a}, int(t)) for a, t in sorted(last.items())])
    rbf = mstate.get("rollback_failed", {})
    fam("app_rollback_failed", "gauge", "1 while the app's last rollback failed and it has not converged since.",
        [({"app": a}, 1 if a in rbf else 0) for a in apps])
    drift = run_info.get("drift", {})
    fam("app_drift", "gauge", "1 if the app's live config was changed outside git (detected on the last run).",
        [({"app": a}, 1 if drift.get(a) else 0) for a in sorted(drift)])
    if "backup_bytes" in run_info:
        fam("backup_bytes", "gauge", "Bytes kept in the reconciler backup dir after retention.",
            [({}, run_info["backup_bytes"])])
    return "\n".join(out) + "\n"


def write_metrics(cfg, results, unmanaged, ok_all, mstate=None, run_info=None, now=None):
    d = cfg.get("metrics_dir")
    if not d or not os.path.isdir(d):
        return
    now = int(now if now is not None else rt._now())
    prev_success = None
    prev = os.path.join(d, "apps_gitops.prom")
    for ln in (rt.read_file(prev) or "").splitlines():
        if ln.startswith("truenas_apps_gitops_last_success_timestamp_seconds"):
            prev_success = ln.split()[-1]
    text = rt.render_metrics(results, unmanaged, ok_all, now, mstate or {}, run_info or {}, prev_success)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".apps_gitops.prom.")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(text)
            os.fchmod(f.fileno(), 0o644)
        os.replace(tmp, prev)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise
