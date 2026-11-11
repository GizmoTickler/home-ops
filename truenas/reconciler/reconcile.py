#!/usr/bin/env python3
"""nas01 app reconciler: git (home-ops truenas/apps/<app>/) -> TrueNAS custom apps.

Runs on nas01 as root (cron, every 10 min). git is the source of truth; nas01 converges to it.

Per app directory truenas/apps/<app>/:
  compose.yaml   required. The app's full compose. Any string may contain {{ op://Vault/Item/[Section/]field }};
                 references are resolved through the local 1Password Connect server at deploy time (never in git).
  app.yaml       optional:
                   dirs:  [{path, owner: "uid:gid", mode: "0755"}]           ensured before deploy
                   files: [{template, dest, owner, mode}]                    template (in the app dir) rendered with
                                                                             op refs -> dest; app redeployed on change
                   ini:   [{path, set: {"Section\\Key": "value"}}]           existing `key=value` lines rewritten
                                                                             (app stopped, patched, started)
                   health: {http: URL, insecure: true} | {tcp: "host:port"}  probed after RUNNING (120 s);
                                                                             failure -> rollback
  *.tmpl         templates referenced by app.yaml.

Behaviour per run, per app (one failing app never blocks the others):
  render -> compare with live (`app.config`, files, ini) -> if different: deploy -> wait RUNNING -> else roll back.
  Apps present on nas01 but absent from git are reported (never deleted).

Usage: reconcile.py [--dry-run] [--app NAME] [--config PATH] [--no-pull]
Config (root 0600 JSON, NOT in git): see DEFAULT_CONFIG. Exit 0 if every app converged, 1 otherwise.
"""
import argparse, difflib, fcntl, json, os, re, stat, subprocess, sys, tempfile, time, urllib.parse, urllib.request
import yaml

DEFAULT_CONFIG = "/mnt/flashstor/secure-scripts/apps-gitops/config.json"
REF = re.compile(r"\{\{\s*(op://[^}\s]+)\s*\}\}")
RUN_TIMEOUT = 300  # s to wait for an app to reach RUNNING after a deploy


class Ctx:
    def __init__(self, cfg, dry):
        self.cfg, self.dry = cfg, dry
        self.secrets = {}  # ref -> value (this run only)
        self._vaults, self._items = {}, {}
        self.log_lines = []

    def log(self, msg):
        line = f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} {msg}"
        print(line, flush=True)
        self.log_lines.append(line)

    def mask(self, text):
        for ref, val in self.secrets.items():
            if val and len(val) >= 4:
                text = text.replace(val, f"<{ref}>")
        return text

    # ---- 1Password Connect -------------------------------------------------------------------------------
    def _connect(self, path):
        req = urllib.request.Request(self.cfg["connect_url"].rstrip("/") + path,
                                     headers={"Authorization": f"Bearer {self.cfg['connect_token']}"})
        with urllib.request.urlopen(req, timeout=20) as r:
            return json.load(r)

    def resolve(self, ref):
        if ref in self.secrets:
            return self.secrets[ref]
        parts = ref[len("op://"):].split("/")
        if len(parts) not in (3, 4):
            raise ValueError(f"bad reference {ref}")
        vault, item, field = parts[0], parts[1], parts[-1]
        section = parts[2] if len(parts) == 4 else None
        if vault not in self._vaults:
            q = urllib.parse.quote(f'name eq "{vault}"')
            v = self._connect(f"/v1/vaults?filter={q}")
            if not v:
                raise KeyError(f"vault {vault} not found")
            self._vaults[vault] = v[0]["id"]
        vid = self._vaults[vault]
        key = (vid, item)
        if key not in self._items:
            q = urllib.parse.quote(f'title eq "{item}"')
            it = self._connect(f"/v1/vaults/{vid}/items?filter={q}")
            if not it:
                raise KeyError(f"item {vault}/{item} not found")
            self._items[key] = self._connect(f"/v1/vaults/{vid}/items/{it[0]['id']}")
        full = self._items[key]
        sections = {s["id"]: s.get("label", "") for s in full.get("sections", [])}
        for f in full.get("fields", []):
            if f.get("label") != field and f.get("id") != field:
                continue
            if section is not None and sections.get((f.get("section") or {}).get("id"), "") != section:
                continue
            if "value" not in f:
                raise KeyError(f"{ref} has no value")
            self.secrets[ref] = f["value"]
            return f["value"]
        raise KeyError(f"field not found: {ref}")

    def render_str(self, s):
        return REF.sub(lambda m: self.resolve(m.group(1)), s)

    def render_obj(self, o):
        if isinstance(o, dict):
            return {k: self.render_obj(v) for k, v in o.items()}
        if isinstance(o, list):
            return [self.render_obj(v) for v in o]
        if isinstance(o, str):
            return self.render_str(o)
        return o


# ---- TrueNAS middleware ----------------------------------------------------------------------------------
def midclt(*args, payload=None, job=False, timeout=900):
    cmd = ["midclt", "call"] + (["-j"] if job else []) + list(args)
    if payload is not None:
        cmd.append("-")
    p = subprocess.run(cmd, input=None if payload is None else json.dumps(payload), capture_output=True,
                       text=True, timeout=timeout)
    if p.returncode != 0:
        raise RuntimeError(f"midclt {' '.join(args)} failed: {p.stderr.strip()[-800:]}")
    out = p.stdout.strip()
    # -j prints job progress lines before the JSON result; the result is the last line
    last = out.splitlines()[-1] if out else "null"
    try:
        return json.loads(last)
    except json.JSONDecodeError:
        return out


def app_state(name):
    r = midclt("app.query", json.dumps([["name", "=", name]]))
    return r[0] if r else None


def wait_running(name, ctx):
    deadline = time.time() + RUN_TIMEOUT
    st = None
    while time.time() < deadline:
        a = app_state(name)
        st = a and a.get("state")
        if st == "RUNNING":
            return True
        if st == "CRASHED" and time.time() > deadline - RUN_TIMEOUT + 60:
            break
        time.sleep(5)
    ctx.log(f"{name}: not RUNNING after deploy (state={st})")
    return False


def healthy(meta, name, ctx):
    h = meta.get("health")
    if not h:
        return True
    import socket, ssl
    deadline, err = time.time() + 120, None
    while time.time() < deadline:
        try:
            if "tcp" in h:
                host, port = h["tcp"].rsplit(":", 1)
                socket.create_connection((host, int(port)), timeout=5).close()
            else:
                c = ssl._create_unverified_context() if h.get("insecure") else None
                with urllib.request.urlopen(h["http"], timeout=10, context=c) as r:
                    if r.status >= 400:
                        raise RuntimeError(f"HTTP {r.status}")
            return True
        except Exception as e:  # noqa: BLE001
            err = e
            time.sleep(5)
    ctx.log(f"{name}: health probe failed: {err}")
    return False


def deploy_compose(name, compose, exists):
    if exists:
        midclt("app.update", name, payload={"custom_compose_config": compose}, job=True)
    else:
        midclt("app.create", payload={"app_name": name, "custom_app": True, "custom_compose_config": compose}, job=True)


# ---- files / dirs / ini ----------------------------------------------------------------------------------
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
            os.fchown(fd, uid, gid)
            os.fchmod(fd, mode)
        finally:
            os.close(fd)
    return True


MAX_FILE = 4 * 1024 * 1024


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
        if st.st_size > MAX_FILE:
            raise RuntimeError(f"{path} is larger than {MAX_FILE} bytes -- refusing")
        data = os.read(fd, MAX_FILE + 1)
    finally:
        os.close(fd)
    return data.decode(), st


def read_file(path):
    return read_file_st(path)[0]


def write_file(dest, content, owner, mode, backup_dir=None):
    """Atomically replace dest. The temp file gets a random name (O_EXCL) in dest's directory and is chowned
    through its descriptor, so a symlink planted by the (app-owned) directory's owner is never followed.
    The previous content is backed up into backup_dir (root-only state dir), not next to dest."""
    uid, gid = parse_owner(owner)
    ddir = os.path.dirname(dest)
    if os.path.islink(dest):
        raise RuntimeError(f"{dest} is a symlink -- refusing")
    if backup_dir:
        cur = read_file(dest)
        if cur is not None:
            os.makedirs(backup_dir, mode=0o700, exist_ok=True)
            bfd = os.open(os.path.join(backup_dir, dest.strip("/").replace("/", "__") + ".prev"),
                          os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
            with os.fdopen(bfd, "w") as b:
                b.write(cur)
    fd, tmp = tempfile.mkstemp(dir=ddir, prefix=".reconcile-")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(content)
            f.flush()
            os.fchown(f.fileno(), uid, gid)
            os.fchmod(f.fileno(), mode)
        os.replace(tmp, dest)  # rename never follows a symlink at dest
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def ini_changes(spec):
    """Return {key: (old, new)} for keys whose existing line differs; missing keys are reported, not added."""
    path = spec["path"]
    lines = (read_file(path) or "").splitlines()
    have = {}
    for ln in lines:
        if "=" in ln and not ln.lstrip().startswith(("#", ";", "[")):
            k, v = ln.split("=", 1)
            have.setdefault(k, v)
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
    text, st = read_file_st(path)
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
    write_file(path, "\n".join(new) + "\n", f"{st.st_uid}:{st.st_gid}", mode, backup_dir)


# ---- one app ---------------------------------------------------------------------------------------------
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


def reconcile_app(appdir, ctx):
    name = os.path.basename(appdir)
    with open(os.path.join(appdir, "compose.yaml")) as f:
        src = yaml.safe_load(f)
    meta = {}
    if os.path.exists(os.path.join(appdir, "app.yaml")):
        with open(os.path.join(appdir, "app.yaml")) as f:
            meta = yaml.safe_load(f) or {}

    desired = ctx.render_obj(src)
    rendered_files = []
    for fs in meta.get("files", []):
        with open(os.path.join(appdir, fs["template"])) as f:
            content = ctx.render_str(f.read())
        rendered_files.append((fs, content))

    app = app_state(name)
    live = midclt("app.config", name) if app else None
    if app and not app.get("custom_app"):
        ctx.log(f"{name}: exists on nas01 as a CATALOG app; refusing to convert automatically (migrate by hand)")
        return False

    compose_diff = live != desired
    dirs_changed = any([ensure_dir(d, ctx, name) for d in meta.get("dirs", [])])  # list: ensure ALL dirs

    file_changes = []
    for fs, content in rendered_files:
        cur = read_file(fs["dest"])
        if cur != content:
            file_changes.append((fs, content, cur))

    ini_todo = []
    for spec in meta.get("ini", []):
        if not os.path.lexists(spec["path"]):
            ctx.log(f"{name}: ini file {spec['path']} missing (app not initialised yet?) -- skipped")
            continue
        ch, missing = ini_changes(spec)
        for k in missing:
            ctx.log(f"{name}: ini key {k!r} not present in {spec['path']} -- not added")
        if ch:
            ini_todo.append((spec, ch))

    if not (compose_diff or file_changes or ini_todo):
        state = app.get("state") if app else None
        ctx.log(f"{name}: in sync ({state})")
        return True

    # ---- report ----
    if compose_diff:
        paths = changed_paths(live, desired) if live is not None else ["(app absent: create)"]
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
    if ctx.dry:
        return True

    # ---- apply, with rollback ----
    prev_files = []
    try:
        if ini_todo and app:
            ctx.log(f"{name}: stopping to patch ini")
            midclt("app.stop", name, job=True)
        bdir = os.path.join(ctx.cfg["state_dir"], "backup", name)
        for spec, _ in ini_todo:
            ini_apply(spec, bdir)
        for fs, content, cur in file_changes:
            prev_files.append((fs, cur))
            write_file(fs["dest"], content, fs.get("owner"), int(str(fs.get("mode", "0600")), 8), bdir)
        if compose_diff:
            deploy_compose(name, desired, exists=bool(app))
            ctx.log(f"{name}: compose {'updated' if app else 'created'}")
        elif file_changes or dirs_changed:
            midclt("app.redeploy", name, job=True)
            ctx.log(f"{name}: redeployed for file changes")
        if ini_todo and not compose_diff and not file_changes:
            midclt("app.start", name, job=True)
        if wait_running(name, ctx) and healthy(meta, name, ctx):
            ctx.log(f"{name}: converged (RUNNING{', healthy' if meta.get('health') else ''})")
            return True
        raise RuntimeError("not RUNNING/healthy")
    except Exception as e:
        ctx.log(f"{name}: deploy FAILED: {ctx.mask(str(e))} -- rolling back")
        try:
            for fs, cur in prev_files:
                if cur is not None:
                    write_file(fs["dest"], cur, fs.get("owner"), int(str(fs.get("mode", "0600")), 8))
            if compose_diff and live is not None:
                deploy_compose(name, live, exists=True)
                ctx.log(f"{name}: rolled back to previous compose")
            elif prev_files:
                midclt("app.redeploy", name, job=True)
            wait_running(name, ctx)
        except Exception as e2:
            ctx.log(f"{name}: ROLLBACK FAILED: {ctx.mask(str(e2))}")
        return False


# ---- main ------------------------------------------------------------------------------------------------
def git_pull(cfg, ctx):
    co, repo, br = cfg["checkout"], cfg["repo"], cfg.get("branch", "main")
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")
    run = lambda *a: subprocess.run(["git", "-C", co, *a], check=True, capture_output=True, text=True, env=env,
                                    timeout=120)
    if not os.path.isdir(os.path.join(co, ".git")):
        subprocess.run(["git", "clone", "--depth", "1", "--filter=blob:none", "--sparse", "-b", br, repo, co],
                       check=True, capture_output=True, text=True, env=env, timeout=300)
        run("sparse-checkout", "set", "truenas")
    run("fetch", "--depth", "1", "origin", br)
    run("reset", "--hard", "FETCH_HEAD")
    head = run("rev-parse", "--short", "HEAD").stdout.strip()
    ctx.log(f"git {br}@{head}")
    return head


def write_metrics(cfg, results, unmanaged, ok_all):
    d = cfg.get("metrics_dir")
    if not d or not os.path.isdir(d):
        return
    now = int(time.time())
    lines = ["# HELP truenas_apps_gitops_last_run_timestamp_seconds Last reconciler run.",
             "# TYPE truenas_apps_gitops_last_run_timestamp_seconds gauge",
             f"truenas_apps_gitops_last_run_timestamp_seconds {now}",
             "# HELP truenas_apps_gitops_app_ok 1 if the app converged to git on the last run.",
             "# TYPE truenas_apps_gitops_app_ok gauge"]
    for app, ok in sorted(results.items()):
        lines.append(f'truenas_apps_gitops_app_ok{{app="{app}"}} {1 if ok else 0}')
    lines += ["# HELP truenas_apps_gitops_unmanaged_apps Apps on nas01 not present in git.",
              "# TYPE truenas_apps_gitops_unmanaged_apps gauge",
              f"truenas_apps_gitops_unmanaged_apps {unmanaged}"]
    if ok_all:
        lines += ["# TYPE truenas_apps_gitops_last_success_timestamp_seconds gauge",
                  f"truenas_apps_gitops_last_success_timestamp_seconds {now}"]
    else:
        prev = os.path.join(d, "apps_gitops.prom")
        if os.path.exists(prev):
            for ln in open(prev):
                if ln.startswith("truenas_apps_gitops_last_success_timestamp_seconds"):
                    lines += ["# TYPE truenas_apps_gitops_last_success_timestamp_seconds gauge", ln.rstrip()]
    tmp = os.path.join(d, ".apps_gitops.prom.tmp")
    with open(tmp, "w") as f:
        f.write("\n".join(lines) + "\n")
    os.chmod(tmp, 0o644)
    os.replace(tmp, os.path.join(d, "apps_gitops.prom"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=DEFAULT_CONFIG)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--app", action="append")
    ap.add_argument("--no-pull", action="store_true")
    a = ap.parse_args()
    with open(a.config) as f:
        cfg = json.load(f)
    os.makedirs(cfg["state_dir"], mode=0o700, exist_ok=True)
    lockf = open(os.path.join(cfg["state_dir"], ".lock"), "w")
    try:
        fcntl.flock(lockf, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print("another reconcile is running", file=sys.stderr)
        return 0
    ctx = Ctx(cfg, a.dry_run)
    head = "nopull" if a.no_pull else git_pull(cfg, ctx)
    root = os.path.join(cfg["checkout"], "truenas", "apps")
    apps = sorted(d for d in os.listdir(root) if os.path.isfile(os.path.join(root, d, "compose.yaml")))
    if a.app:
        apps = [x for x in apps if x in a.app]
    results, unmanaged = {}, 0
    for name in apps:
        try:
            results[name] = reconcile_app(os.path.join(root, name), ctx)
        except Exception as e:
            ctx.log(f"{name}: ERROR {ctx.mask(str(e))}")
            results[name] = False
    if not a.app:
        live_names = {x["name"] for x in midclt("app.query")}
        for extra in sorted(live_names - set(apps)):
            ctx.log(f"{extra}: on nas01 but NOT in git (unmanaged)")
            unmanaged += 1
    ok_all = all(results.values())
    if not a.dry_run and not a.app:
        write_metrics(cfg, results, unmanaged, ok_all)
    logf = os.path.join(cfg["state_dir"], "reconcile.log")
    if os.path.exists(logf) and os.path.getsize(logf) > 5_000_000:
        os.replace(logf, logf + ".1")
    with open(logf, "a") as f:
        f.write("\n".join(ctx.log_lines) + "\n")
    os.chmod(logf, 0o600)
    return 0 if ok_all else 1


if __name__ == "__main__":
    sys.exit(main())
