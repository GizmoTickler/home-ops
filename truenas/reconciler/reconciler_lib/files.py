"""Symlink-safe file/dir/ini operations, JSON state persistence, UI metadata, and drift fingerprints."""

import hashlib, hmac, json, os, secrets as _secrets, stat, tempfile, time
import yaml

from . import runtime as rt


def parse_owner(o):
    u, g = (o or "0:0").split(":")
    return int(u), int(g)


def ensure_dir(d, ctx, name):
    """Create/own a directory without following symlinks (parents are root-owned; the leaf may be app-owned)."""
    path, mode = d["path"], int(str(d.get("mode", "0755")), 8)
    uid, gid = parse_owner(d.get("owner"))
    try:
        st = os.lstat(path)
        if not stat.S_ISDIR(st.st_mode):
            raise RuntimeError(f"{path} exists and is not a directory (symlink?) -- refusing")
        if (st.st_uid, st.st_gid, st.st_mode & 0o7777) == (uid, gid, mode):
            return False
    except FileNotFoundError:
        st = None
    ctx.log(f"{name}: ensure dir {path} {uid}:{gid} {oct(mode)}" + (" (dry-run)" if ctx.dry else ""))
    if not ctx.dry:
        if st is None:
            os.mkdir(path, 0o700)
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            rt.os.fchown(fd, uid, gid)
            os.fchmod(fd, mode)
        finally:
            os.close(fd)
    return True


def read_file_st(path):
    """(content, stat) of a regular file, never following a symlink and never blocking on a FIFO/device node
    planted by the (app-owned) directory's owner. (None, None) if absent."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return None, None
    except OSError as e:
        raise RuntimeError(f"{path}: refusing to read ({e}); symlink?") from e
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise RuntimeError(f"{path} is not a regular file -- refusing")
        if st.st_size > rt.MAX_FILE:
            raise RuntimeError(f"{path} is larger than {rt.MAX_FILE} bytes -- refusing")
        data = os.read(fd, rt.MAX_FILE + 1)
    finally:
        os.close(fd)
    return data.decode(), st


def read_file(path):
    return rt.read_file_st(path)[0]


def backup_name(dest, now=None):
    ts = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(now if now is not None else rt._now()))
    return dest.strip("/").replace("/", "__") + f".{ts}.prev"


def write_file(dest, content, owner, mode, backup_dir=None):
    """Atomically replace dest. The temp file gets a random name (O_EXCL) in dest's directory and is chowned
    through its descriptor, so a symlink planted by the (app-owned) directory's owner is never followed.
    The previous content is backed up into backup_dir (root-only state dir, timestamped), not next to dest."""
    uid, gid = parse_owner(owner)
    ddir = os.path.dirname(dest)
    if os.path.islink(dest):
        raise RuntimeError(f"{dest} is a symlink -- refusing")
    if backup_dir:
        cur = rt.read_file(dest)
        if cur is not None:
            os.makedirs(backup_dir, mode=0o700, exist_ok=True)
            bfd = os.open(os.path.join(backup_dir, rt.backup_name(dest)),
                          os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
            with os.fdopen(bfd, "w") as b:
                b.write(cur)
    fd, tmp = tempfile.mkstemp(dir=ddir, prefix=".reconcile-")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(content)
            f.flush()
            rt.os.fchown(f.fileno(), uid, gid)
            os.fchmod(f.fileno(), mode)
        os.replace(tmp, dest)  # rename never follows a symlink at dest
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def ini_values(path):
    have = {}
    for ln in (rt.read_file(path) or "").splitlines():
        if "=" in ln and not ln.lstrip().startswith(("#", ";", "[")):
            k, v = ln.split("=", 1)
            have.setdefault(k, v)
    return have


def ini_changes(spec):
    """Return ({key: (old, new)} for keys whose existing line differs, [missing keys]); missing keys are reported,
    never added."""
    have = rt.ini_values(spec["path"])
    out, missing = {}, []
    for k, v in spec["set"].items():
        v = str(v)
        if k not in have:
            missing.append(k)
        elif have[k] != v:
            out[k] = (have[k], v)
    return out, missing


def ini_apply(spec, backup_dir):
    path = spec["path"]
    text, st = rt.read_file_st(path)
    if text is None:
        raise RuntimeError(f"{path} vanished")
    lines = text.splitlines()
    want = {k: str(v) for k, v in spec["set"].items()}
    new = []
    for ln in lines:
        if "=" in ln and not ln.lstrip().startswith(("#", ";", "[")):
            k = ln.split("=", 1)[0]
            if k in want:
                ln = f"{k}={want[k]}"
        new.append(ln)
    mode = st.st_mode & 0o777  # never carry setuid/setgid/sticky from an app-owned file
    rt.write_file(path, "\n".join(new) + "\n", f"{st.st_uid}:{st.st_gid}", mode, backup_dir)


def _svc_tag(svc):
    img = (svc or {}).get("image", "")
    ref = img.split("@", 1)[0]
    if ":" in ref.rsplit("/", 1)[-1]:
        return ref.rsplit(":", 1)[1]
    return None


def image_tag(compose, name=None, service=None):
    svcs = compose.get("services") or {}
    for key in (service, name):
        if key and key in svcs:
            tag = _svc_tag(svcs[key])
            if tag:
                return tag
    for svc in svcs.values():
        tag = _svc_tag(svc)
        if tag:
            return tag
    return None


def apply_ui(name, meta, compose, ctx):
    """Return True if the app's metadata.yaml was (or would be) changed."""
    ui = meta.get("ui")
    if not ui:
        return False
    path = os.path.join(rt.IX_APP_CONFIGS, name, "metadata.yaml")
    text, _ = rt.read_file_st(path)
    if text is None:
        return False
    doc = yaml.safe_load(text) or {}
    md = doc.setdefault("metadata", {})
    want = {k: ui[k] for k in ("title", "description", "icon", "home") if k in ui}
    tag = image_tag(compose, name=name, service=ui.get("service"))
    if tag:
        want["app_version"] = tag
    top = {"human_version": f"{tag}_custom"} if tag else {}
    if all(md.get(k) == v for k, v in want.items()) and all(doc.get(k) == v for k, v in top.items()):
        return False
    ctx.log(f"{name}: UI metadata -> {', '.join(sorted(want))}" + (" (dry-run)" if ctx.dry else ""))
    if not ctx.dry:
        md.update(want)
        doc.update(top)
        rt.write_file(path, yaml.safe_dump(doc, sort_keys=False, default_flow_style=False), "0:0", 0o600)
    return True


def atomic_json(path, obj):
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


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, ValueError):
        return default


def backup_root(cfg):
    return cfg.get("backup_dir") or os.path.join(cfg["state_dir"], "backup")


def save_state(ctx):
    """Persist ctx.state atomically in live mode when state_dir exists."""
    sd = (ctx.cfg or {}).get("state_dir")
    if ctx.dry or not sd or not os.path.isdir(sd):
        return
    rt.atomic_json(os.path.join(sd, "state.json"), ctx.state)


def _fp_key(ctx):
    if "_fpkey" not in ctx.__dict__:
        p = os.path.join(ctx.cfg["state_dir"], ".fingerprint-key")
        try:
            k, _ = rt.read_file_st(p)
        except RuntimeError:
            k = None
        if not k:
            k = _secrets.token_hex(32)
            if not ctx.dry:
                rt.write_secret_file(p, k)
        ctx._fpkey = k.strip().encode()
    return ctx._fpkey


def fingerprint(ctx, obj):
    """Keyed hash (HMAC, key in the root-only state dir) so the state file never holds an unsalted secret hash."""
    data = obj if isinstance(obj, str) else json.dumps(obj, sort_keys=True, default=str)
    return hmac.new(_fp_key(ctx), data.encode(), hashlib.sha256).hexdigest()[:32]


def live_fingerprints(ctx, live_compose, meta):
    fp = {"compose": rt.fingerprint(ctx, live_compose) if live_compose is not None else None, "files": {}, "ini": {}}
    for fs in meta.get("files", []):
        try:
            cur = rt.read_file(fs["dest"])
        except RuntimeError:
            cur = None
        fp["files"][fs["dest"]] = rt.fingerprint(ctx, cur) if cur is not None else None
    for spec in meta.get("ini", []):
        try:
            have = rt.ini_values(spec["path"]) if os.path.lexists(spec["path"]) else {}
        except RuntimeError:
            have = {}
        fp["ini"][spec["path"]] = rt.fingerprint(ctx, {k: have.get(k) for k in spec["set"]})
    return fp


def desired_fingerprints(ctx, desired_compose, rendered_files, meta):
    """Compute keyed fingerprints for desired compose, rendered files, and desired ini settings."""
    fp = {"compose": rt.fingerprint(ctx, desired_compose), "files": {}, "ini": {}}
    for fs, content in rendered_files:
        fp["files"][fs["dest"]] = rt.fingerprint(ctx, content)
    for spec in meta.get("ini", []):
        fp["ini"][spec["path"]] = rt.fingerprint(ctx, {k: str(v) for k, v in spec["set"].items()})
    return fp


def drift_report(baseline, live_fp):
    """Components whose live fingerprint differs from what the reconciler last applied (empty: no drift / unknown)."""
    if not baseline:
        return []
    out = []
    if baseline.get("compose") and live_fp["compose"] and baseline["compose"] != live_fp["compose"]:
        out.append("compose")
    for k in ("files", "ini"):
        for path, f in (live_fp.get(k) or {}).items():
            b = (baseline.get(k) or {}).get(path)
            if b and f and b != f:
                out.append(f"{k[:-1] if k == 'files' else 'ini'} {path}")
    return out


def yaml_dump(o):
    return yaml.safe_dump(o, sort_keys=True, default_flow_style=False, width=200)


def changed_paths(a, b, path=""):
    """Leaf paths where a and b differ (no values: either side may hold a current or rotated secret)."""
    if isinstance(a, dict) and isinstance(b, dict):
        out = []
        for k in sorted(set(a) | set(b), key=str):
            out += changed_paths(a.get(k), b.get(k), f"{path}.{k}" if path else str(k)) if (k in a and k in b) \
                else [f"{path}.{k}" if path else str(k)]
        return out
    if isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        out = []
        for i, (x, y) in enumerate(zip(a, b)):
            out += changed_paths(x, y, f"{path}[{i}]")
        return out
    return [] if a == b else [path or "(root)"]


def load_app(appdir):
    with open(os.path.join(appdir, "compose.yaml")) as f:
        src = yaml.safe_load(f)
    meta = {}
    if os.path.exists(os.path.join(appdir, "app.yaml")):
        with open(os.path.join(appdir, "app.yaml")) as f:
            meta = yaml.safe_load(f) or {}
    return src, meta
