"""Coalescing trigger file handling, host script syncing (`truenas/host/`), and app retirement."""

import os, stat
import yaml

from . import runtime as rt


def _trigger_path(cfg, cli_trigger=None):
    if cli_trigger:
        return cli_trigger
    tf = cfg.get("trigger_file")
    if tf:
        return tf
    if str(cfg.get("checkout", "")).startswith("/mnt/flashstor/") and os.path.isdir(os.path.dirname(rt.DEFAULT_TRIGGER_FILE)):
        return rt.DEFAULT_TRIGGER_FILE
    return None


def _open_trigger_dir(path):
    parent, name = os.path.dirname(path), os.path.basename(path)
    if not parent or not name or os.path.islink(parent):
        return None, name
    try:
        return os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW), name
    except OSError:
        return None, name


def trigger_pending(path):
    if not path:
        return False
    dfd, name = _open_trigger_dir(path)
    if dfd is None:
        return False
    try:
        st = os.stat(name, dir_fd=dfd, follow_symlinks=False)
        return stat.S_ISREG(st.st_mode)
    except OSError:
        return False
    finally:
        os.close(dfd)


def consume_trigger(path):
    if not path:
        return False
    dfd, name = _open_trigger_dir(path)
    if dfd is None:
        return False
    try:
        try:
            st = os.stat(name, dir_fd=dfd, follow_symlinks=False)
        except FileNotFoundError:
            return False
        if not stat.S_ISREG(st.st_mode):
            raise RuntimeError(f"{path} is not a regular file (symlink/FIFO?) -- refusing")
        os.unlink(name, dir_fd=dfd)
        return True
    finally:
        os.close(dfd)


def sync_apps_east_west(ctx):
    """Re-apply apps bridge East-West nftables policy when the host helper is installed."""
    if ctx.dry or not os.path.isfile(rt.APPS_EAST_WEST_SCRIPT):
        return True
    r = rt.sh(["/usr/bin/python3", rt.APPS_EAST_WEST_SCRIPT, "--apply"], timeout=30)
    if r.returncode != 0:
        ctx.log(f"apps-east-west: FAILED: {r.stderr.strip()[-300:]}")
        return False
    return True


def reconcile_host(checkout, ctx):
    """Sync files declared in truenas/host/host.yaml when their destination parent directory exists."""
    hdir = os.path.join(checkout, "truenas", "host")
    hfile = os.path.join(hdir, "host.yaml")
    if not os.path.exists(hfile):
        return True
    spec_doc = yaml.safe_load(open(hfile)) or {}
    ok = True
    hap = ctx.state.setdefault("host_applied", {})
    for spec in spec_doc.get("files") or []:
        src = os.path.join(hdir, spec["src"])
        dest = spec["dest"]
        if not os.path.isdir(os.path.dirname(dest)):
            continue
        with open(src) as f:
            want = f.read()
        mode = int(str(spec.get("mode", "0700")), 8)
        uid, gid = rt.parse_owner(spec.get("owner", "0:0"))
        cur, st = rt.read_file_st(dest)
        prev = hap.get(dest)
        if cur is not None and prev and rt.fingerprint(ctx, cur) != prev:
            ctx.log(f"host: DRIFT -- {dest} modified outside git"
                    + (" (would restore)" if ctx.dry else " (restoring from git)"))
        needs_write = cur != want or st is None or (st.st_uid, st.st_gid, st.st_mode & 0o7777) != (uid, gid, mode)
        if not needs_write:
            hap[dest] = rt.fingerprint(ctx, want)
            continue
        if ctx.dry:
            ctx.log(f"host: would update {dest} (dry-run)")
            continue
        bdir = os.path.join(rt.backup_root(ctx.cfg), "_host")
        rt.write_file(dest, want, spec.get("owner", "0:0"), mode, bdir)
        hap[dest] = rt.fingerprint(ctx, want)
        ctx.log(f"host: updated {dest}")
        if spec.get("on_change"):
            r = rt.sh(spec["on_change"], timeout=60)
            if r.returncode != 0:
                ctx.log(f"host: on_change hook failed for {dest}: {r.stderr.strip()[-300:]}")
                ok = False
            else:
                ctx.log(f"host: on_change hook OK ({spec['on_change'][0]})")
    if not rt.sync_apps_east_west(ctx):
        ok = False
    return ok


def reconcile_retired(checkout, ctx):
    """Stop and delete retired custom apps declared in truenas/retired.yaml (never deletes host datasets)."""
    rfile = os.path.join(checkout, "truenas", "retired.yaml")
    if not os.path.exists(rfile):
        return True
    doc = yaml.safe_load(open(rfile)) or {}
    names = [str(x) for x in (doc.get("retired") or [])]
    if not names:
        return True
    live = {x["name"]: x for x in rt.midclt("app.query")}
    ok = True
    for rname in names:
        if rname not in live:
            if not ctx.dry:
                (ctx.state.get("applied") or {}).pop(rname, None)
                (ctx.state.get("pending_redeploy") or {}).pop(rname, None)
            continue
        if not live[rname].get("custom_app"):
            ctx.log(f"{rname}: listed in retired.yaml as a CATALOG app -- refusing auto-delete")
            ok = False
            continue
        if ctx.dry:
            ctx.log(f"{rname}: retired -- would stop and delete custom app (dry-run)")
            continue
        try:
            rt.midclt("app.delete", rname, job=True)
            (ctx.state.get("applied") or {}).pop(rname, None)
            (ctx.state.get("pending_redeploy") or {}).pop(rname, None)
            imgs = rt.sh(["docker", "images", "--format", "{{.Repository}}:{{.Tag}}", f"ix-rollback/{rname}/*"], timeout=30)
            for tag in imgs.stdout.splitlines():
                if tag.strip():
                    rt.sh(["docker", "rmi", tag.strip()], timeout=30)
            ctx.log(f"{rname}: retired custom app deleted")
        except Exception as e:  # noqa: BLE001
            ctx.log(f"{rname}: delete FAILED: {ctx.mask(str(e))}")
            ok = False
    return ok
