#!/usr/bin/env python3
"""nas01 app reconciler: git (home-ops truenas/apps/<app>/) -> TrueNAS custom apps.

Runs on nas01 as root (cron, every 10 min). git is the source of truth; nas01 converges to it.

Per app directory truenas/apps/<app>/:
  compose.yaml   required. The app's full compose. Any string may contain {{ op://Vault/Item/[Section/]field }}
                 (Item = title, or the 26-char item id when the title has spaces/parentheses);
                 references are resolved through the local 1Password Connect server at deploy time (never in git).
  app.yaml       optional:
                   dirs:  [{path, owner: "uid:gid", mode: "0755"}]           ensured before deploy
                   files: [{template, dest, owner, mode}]                    template (in the app dir) rendered with
                                                                             op refs -> dest; app redeployed on change
                   ini:   [{path, set: {"Key": "value"}}]                   existing `key=value` lines rewritten
                                                                             (app stopped, patched, started); keys
                                                                             are verbatim + section-blind; a key
                                                                             absent from the file fails the app
                   health: {http: URL, insecure: true} | {tcp: "host:port"}  probed after RUNNING (120 s);
                                                                             failure -> rollback
                   ui:    {title, description, icon: URL, home: URL}         shown in the TrueNAS Apps UI (custom
                                                                             apps have no icon support; the
                                                                             reconciler patches their metadata)
                   critical: true                                            deployed after all other apps, in
                                                                             `order` (lower first); a failure
                                                                             aborts the rest of the run
                   order: 0                                                  critical apps only (default 100)
                   deploy_timeout: 600                                       max s to stay DEPLOYING after a deploy
                   validate: {service, entrypoint, args: [...]}              preflight: run the service's target
                                                                             image on the fully rendered config
                                                                             (e.g. `envoy --mode validate`)
  *.tmpl         templates referenced by app.yaml.

truenas/networks.yaml: shared Docker networks (bridge `apps` for app<->app/Caddy traffic, macvlan `mgmt` on br0)
  that several apps join as `external: true`. Created before any app is reconciled; never changed or deleted
  (a spec mismatch with the live network is reported, not "fixed": recreating a network means stopping its apps).

Behaviour per run:
  git fetch -> CI gate (only commits whose `truenas-apps` check passed are checked out; fail closed) -> per app:
  render -> compare with live (`app.config`, files, ini) + drift check -> if different: preflight (compose config,
  static-IP/host-port conflicts, image pull, `validate`) -> deploy -> wait (state-aware) + health -> else roll back
  (`app.update` with the exact previous compose). Non-critical apps first (one failing app never blocks the others),
  then critical apps one at a time; a failing critical app aborts the run. Apps present on nas01 but absent from
  git are reported (never deleted). Then: backup retention, metrics (node-exporter textfile), log rotation.

Usage: reconcile.py [--dry-run] [--app NAME] [--config PATH] [--no-pull] [--no-ci-gate] [--wait-lock S] [--if-locked skip|fail]
Config (root 0600 JSON, NOT in git): see DEFAULT_CONFIG / CONFIG_DEFAULTS. Exit 0 if every app converged, 1 on failure, 75 on lock contention with --if-locked fail.
"""
import argparse, base64, difflib, fcntl, hashlib, hmac, ipaddress, json, os, re, secrets as _secrets, shutil, ssl, stat, \
    subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request
import yaml

DEFAULT_CONFIG = "/mnt/flashstor/secure-scripts/apps-gitops/config.json"
DEFAULT_TRIGGER_FILE = "/mnt/flashstor/apps/webhook-trigger/poke"
EX_TEMPFAIL = 75
MAX_COALESCE_PASSES = 3
REF = re.compile(r"\{\{\s*(op://[^}\s]+)\s*\}\}")
ITEM_ID = re.compile(r"[a-z0-9]{26}")  # 1Password item ids are 26 lowercase base32 chars
RUN_TIMEOUT = 600   # default s an app may stay DEPLOYING after a deploy (app.yaml deploy_timeout overrides)
SETTLE = 20         # s a terminal state (CRASHED/STOPPED/absent) must persist before the wait gives up
POLL = 5

# Optional config.json keys and their defaults (required: repo, checkout, state_dir, connect_url, connect_token).
CONFIG_DEFAULTS = {
    "branch": "main",
    "metrics_dir": None,
    "trigger_file": None,
    "min_ready_seconds": 0,
    "deploy_timeout": RUN_TIMEOUT,
    "backup_dir": None,                 # default <state_dir>/backup
    "backup_retention_days": 14,
    "backup_keep_newest": 5,            # per backed-up file (per app); loose top-level files: per app name prefix
    "log_max_bytes": 5_000_000,
    "log_keep": 5,
    "preflight": {"enabled": True, "pull_images": True, "probe_lan": True},
    "ci_gate": {"enabled": True, "check_name": "Lint nas01 apps", "api_url": "https://api.github.com",
                "history_depth": 50, "timeout": 15, "token_ref": None},
    "github_status": {"enabled": True, "context": "nas01/apps-gitops", "api_url": "https://api.github.com",
                      "timeout": 15, "token_ref": None},
}

_now = time.time      # patched in tests
_sleep = time.sleep


class ConnectUnavailableError(RuntimeError):
    """Raised when the local 1Password Connect API is unreachable or returning 5xx."""


def cfg_get(cfg, key):
    d = CONFIG_DEFAULTS.get(key)
    v = cfg.get(key, d)
    if isinstance(d, dict):
        v = {**d, **(v or {})}
    return v


class Ctx:
    def __init__(self, cfg, dry):
        self.cfg, self.dry = cfg, dry
        self.secrets = {}  # ref -> value (this run only)
        self._vaults, self._items = {}, {}
        self.log_lines = []
        self.ui_changed = False
        self.connect_ok = True
        self.live = None            # cached docker/host inventory for preflight (invalidated after deploys)
        self.shared_nets = {}       # name -> networks.yaml spec
        self.events = []            # (kind, app) for metrics: deploy, deploy_failure, rollback, rollback_failure, ...
        self.drift = {}             # app -> True/False
        self.state = {}             # persisted reconciler state (applied fingerprints etc.)
        self.public_tokens = set()  # app directory names (public in git): never treated as secret values

    def log(self, msg):
        line = f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} {self.mask(str(msg))}"  # masked by default, every line
        print(line, flush=True)
        self.log_lines.append(line)

    def mask(self, text):
        # A resolved value that is exactly an app name (public in git, e.g. an OIDC client id "autobrr") is not a
        # secret; masking it would rewrite every log line that names the app.
        for ref, val in self.secrets.items():
            if val and len(val) >= 4 and val not in self.public_tokens:
                text = text.replace(val, f"<{ref}>")
        return text

    def event(self, kind, app):
        self.events.append((kind, app))

    # ---- 1Password Connect -------------------------------------------------------------------------------
    def _connect(self, path):
        req = urllib.request.Request(self.cfg["connect_url"].rstrip("/") + path,
                                     headers={"Authorization": f"Bearer {self.cfg['connect_token']}"})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code >= 500:
                self.connect_ok = False
                raise ConnectUnavailableError(f"HTTP {e.code} from 1Password Connect") from e
            raise
        except (urllib.error.URLError, OSError) as e:
            self.connect_ok = False
            raise ConnectUnavailableError(f"1Password Connect unreachable: {e}") from e

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
            if ITEM_ID.fullmatch(item):  # item id: for titles a ref can't carry (spaces, parentheses)
                self._items[key] = self._connect(f"/v1/vaults/{vid}/items/{item}")
            else:
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


# ---- subprocess / TrueNAS middleware ---------------------------------------------------------------------
def sh(cmd, timeout=60, input=None):
    """Run a command, never through a shell. Returns CompletedProcess (text). Patched in tests."""
    return subprocess.run(cmd, input=input, capture_output=True, text=True, timeout=timeout)


def midclt(*args, payload=None, job=False, timeout=900):
    cmd = ["midclt", "call"] + (["-j"] if job else []) + list(args)
    if payload is not None:
        cmd.append("-")
    p = sh(cmd, timeout=timeout, input=None if payload is None else json.dumps(payload))
    if p.returncode != 0:
        # -j prints progress bars on stderr before the error; keep the last meaningful line(s)
        err = "\n".join(x for x in re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", p.stderr).splitlines()
                        if x.strip() and not x.startswith(("Status:", "Total Progress:")))
        raise RuntimeError(f"midclt {' '.join(args)} failed: {err.strip()[-800:]}")
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


def ensure_started(name, ctx):
    a = app_state(name)
    if a and a.get("state") in ("STOPPED", "CRASHED"):
        ctx.log(f"{name}: state {a.get('state')} after deploy -- starting")
        midclt("app.start", name, job=True)


def container_status(name):
    try:
        return sh(["docker", "ps", "-a", "--filter", f"label=com.docker.compose.project=ix-{name}",
                   "--format", "{{.Names}} {{.Status}}"], timeout=30).stdout.strip().replace("\n", "; ")
    except Exception as e:  # noqa: BLE001
        return f"(docker ps failed: {e})"


def wait_running(name, ctx, timeout=RUN_TIMEOUT):
    """State-aware wait. RUNNING -> True. DEPLOYING/STOPPING -> keep waiting up to `timeout`. CRASHED/STOPPED/absent
    -> failure once that terminal state has persisted for SETTLE s (TrueNAS briefly reports them while recreating)."""
    start, st, terminal_since = _now(), None, None
    while True:
        a = app_state(name)
        st = a.get("state") if a else None
        now = _now()
        if st == "RUNNING":
            return True
        if st in ("CRASHED", "STOPPED", None):
            terminal_since = terminal_since if terminal_since is not None else now
            if now - terminal_since >= SETTLE:
                ctx.log(f"{name}: {st or 'ABSENT'} after deploy (terminal) -- containers: {container_status(name)}")
                return False
        else:
            terminal_since = None
        if now - start >= timeout:
            ctx.log(f"{name}: not RUNNING after {int(timeout)} s (state={st}) -- containers: {container_status(name)}")
            return False
        _sleep(POLL)


def container_pid(app, service):
    out = sh(["docker", "ps", "-q", "--filter", f"label=com.docker.compose.project=ix-{app}",
              "--filter", f"label=com.docker.compose.service={service}"], timeout=30).stdout.split()
    if not out:
        raise RuntimeError(f"no running container for {app}/{service}")
    r = sh(["docker", "inspect", "-f", "{{.State.Pid}}", out[0]], timeout=30)
    if r.returncode != 0:
        raise RuntimeError(f"docker inspect {app}/{service} failed")
    return r.stdout.strip()


def container_restarts(app):
    """Return {container_id: (running, restart_count)} for an app's containers."""
    r = sh(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project=ix-{app}"], timeout=30)
    ids = r.stdout.split() if r.returncode == 0 else []
    if not ids:
        return {}
    ins = sh(["docker", "inspect", *ids], timeout=30)
    if ins.returncode != 0:
        return {}
    out = {}
    for c in json.loads(ins.stdout):
        st = c.get("State") or {}
        out[c.get("Id", "")] = (bool(st.get("Running")), int(c.get("RestartCount") or 0))
    return out


def _http_probe(url, insecure=False, expect=None, netns_pid=None):
    """Run an HTTP(S) health probe directly or inside a container's network namespace."""
    if netns_pid is not None:
        script = (
            "import ssl, sys, urllib.request\n"
            "u, ins, exp = sys.argv[1], sys.argv[2] == '1', sys.argv[3]\n"
            "ctx = ssl._create_unverified_context() if ins else None\n"
            "with urllib.request.urlopen(u, timeout=10, context=ctx) as r:\n"
            "    body = r.read(65536).decode('utf-8', 'replace')\n"
            "    if r.status >= 400:\n"
            "        raise SystemExit(f'HTTP {r.status}')\n"
            "    if exp and exp not in body:\n"
            "        raise SystemExit(f'body missing {exp!r}')\n"
        )
        r = sh(["nsenter", f"--net=/proc/{netns_pid}/ns/net", sys.executable, "-c", script,
                url, "1" if insecure else "0", expect or ""], timeout=20)
        if r.returncode != 0:
            raise RuntimeError((r.stderr.strip().splitlines() or r.stdout.strip().splitlines() or ["http probe failed"])[-1])
        return
    c = ssl._create_unverified_context() if insecure else None
    with urllib.request.urlopen(url, timeout=10, context=c) as r:
        body = r.read(65536).decode("utf-8", "replace")
        if r.status >= 400:
            raise RuntimeError(f"HTTP {r.status}")
        if expect and expect not in body:
            raise RuntimeError(f"body missing {expect!r}")


def healthy(meta, name, ctx):
    """Probe after RUNNING. Supports tcp and http inside `netns: <service>`, `expect` body checks, and `min_ready`
    restart-stability confirmation."""
    h = meta.get("health")
    if not h:
        return True
    import socket
    expect = h.get("expect")
    min_ready = int(h.get("min_ready", cfg_get(ctx.cfg, "min_ready_seconds")) or 0)

    def probe_once():
        if "tcp" in h and h.get("netns"):
            host, port = h["tcp"].rsplit(":", 1)
            pid = container_pid(name, h["netns"])
            r = sh(["nsenter", f"--net=/proc/{pid}/ns/net", sys.executable, "-c",
                    "import socket,sys;socket.create_connection((sys.argv[1],int(sys.argv[2])),5).close()",
                    host, port], timeout=20)
            if r.returncode != 0:
                raise RuntimeError((r.stderr.strip().splitlines() or ["probe failed"])[-1])
        elif "tcp" in h:
            host, port = h["tcp"].rsplit(":", 1)
            socket.create_connection((host, int(port)), timeout=5).close()
        elif "http" in h and h.get("netns"):
            pid = container_pid(name, h["netns"])
            _http_probe(h["http"], bool(h.get("insecure")), expect, netns_pid=pid)
        else:
            _http_probe(h["http"], bool(h.get("insecure")), expect)

    deadline, err = _now() + int(h.get("timeout", 120)), None
    while _now() < deadline:
        try:
            probe_once()
            if min_ready > 0:
                before = container_restarts(name)
                if not before or any(not run for run, _ in before.values()):
                    raise RuntimeError("app containers not running for min_ready check")
                _sleep(min_ready)
                after = container_restarts(name)
                if set(after) != set(before) or any(not run for run, _ in after.values()):
                    raise RuntimeError("container restarted/disappeared during min_ready soak")
                for cid, (_, cnt_b) in before.items():
                    if after[cid][1] != cnt_b:
                        raise RuntimeError(f"container {cid[:12]} restarted during min_ready soak")
                probe_once()
            return True
        except Exception as e:  # noqa: BLE001
            err = e
            _sleep(5)
    ctx.log(f"{name}: health probe failed: {err}; containers: {container_status(name)}")
    return False


def load_networks(path):
    if not os.path.exists(path):
        return []
    with open(path) as f:
        return (yaml.safe_load(f) or {}).get("networks") or []


def ensure_networks(path, ctx):
    """Create the shared networks from truenas/networks.yaml that do not exist yet. Returns False on any problem."""
    nets = load_networks(path)
    ctx.shared_nets = {n["name"]: n for n in nets}
    ok = True
    for n in nets:
        name = n["name"]
        r = sh(["docker", "network", "inspect", name], timeout=30)
        if r.returncode == 0:
            live = json.loads(r.stdout)[0]
            subnets = [c.get("Subnet") for c in (live.get("IPAM") or {}).get("Config") or []]
            parent = (live.get("Options") or {}).get("parent")
            if live.get("Driver") != n["driver"] or n["subnet"] not in subnets or parent != n.get("parent"):
                ctx.log(f"network {name}: live spec differs from networks.yaml (driver={live.get('Driver')} "
                        f"subnets={subnets} parent={parent}) -- not changed")
                ok = False
            continue
        cmd = ["docker", "network", "create", "--driver", n["driver"], "--subnet", n["subnet"],
               "--ipv6=false", "--label", "managed-by=truenas-reconciler"]
        if n.get("gateway"):
            cmd += ["--gateway", n["gateway"]]
        if n.get("parent"):
            cmd += ["-o", f"parent={n['parent']}"]
        if n.get("internal"):
            cmd.append("--internal")
        if ctx.dry:
            ctx.log(f"network {name}: absent -- would create ({n['driver']} {n['subnet']})")
            continue
        r = sh(cmd + [name], timeout=60)
        if r.returncode != 0:
            ctx.log(f"network {name}: create FAILED: {r.stderr.strip()[-300:]}")
            ok = False
        else:
            ctx.log(f"network {name}: created ({n['driver']} {n['subnet']})")
    return ok


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


BACKUP_TS = re.compile(r"\.(\d{8}T\d{6}Z)$")


def backup_name(dest, now=None):
    ts = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(now if now is not None else _now()))
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
        cur = read_file(dest)
        if cur is not None:
            os.makedirs(backup_dir, mode=0o700, exist_ok=True)
            bfd = os.open(os.path.join(backup_dir, backup_name(dest)),
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


def ini_values(path):
    have = {}
    for ln in (read_file(path) or "").splitlines():
        if "=" in ln and not ln.lstrip().startswith(("#", ";", "[")):
            k, v = ln.split("=", 1)
            have.setdefault(k, v)
    return have


def ini_changes(spec):
    """Return ({key: (old, new)} for keys whose existing line differs, [missing keys]); missing keys are reported,
    never added."""
    have = ini_values(spec["path"])
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


# ---- TrueNAS UI metadata (custom apps are always "Custom App" with no icon; patch the per-app metadata) ----
IX_APP_CONFIGS = "/mnt/.ix-apps/app_configs"


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
    path = os.path.join(IX_APP_CONFIGS, name, "metadata.yaml")
    text, _ = read_file_st(path)
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
        write_file(path, yaml.safe_dump(doc, sort_keys=False, default_flow_style=False), "0:0", 0o600)
    return True


# ---- preflight -------------------------------------------------------------------------------------------
class Live:
    """Snapshot of what is live on nas01: running containers (compose project, IPs per network, published ports),
    host IPv4 addresses and listening sockets."""

    def __init__(self, containers, host_ips, listeners):
        self.containers, self.host_ips, self.listeners = containers, set(host_ips), set(listeners)


def _parse_addr(a):
    """'192.168.1.2:80' / '*:80' / '[::]:80' / '0.0.0.0%br0:80' -> (ip, port) with wildcard as '0.0.0.0'."""
    ip, _, port = a.rpartition(":")
    ip = ip.strip("[]").split("%", 1)[0]
    if ip in ("*", "::", "", "0.0.0.0"):
        ip = "0.0.0.0"
    try:
        return ip, int(port)
    except ValueError:
        return None


def collect_live():
    ids = sh(["docker", "ps", "-q"], timeout=30).stdout.split()
    containers = []
    if ids:
        r = sh(["docker", "inspect", *ids], timeout=60)
        if r.returncode != 0:
            raise RuntimeError(f"docker inspect failed: {r.stderr.strip()[-200:]}")
        for c in json.loads(r.stdout):
            labels = (c.get("Config") or {}).get("Labels") or {}
            ns = c.get("NetworkSettings") or {}
            ips = {}
            for net, v in (ns.get("Networks") or {}).items():
                ip = (v or {}).get("IPAddress") or ((v or {}).get("IPAMConfig") or {}).get("IPv4Address")
                if ip:
                    ips[net] = ip
            ports = set()
            for k, binds in (ns.get("Ports") or {}).items():
                proto = k.split("/", 1)[1] if "/" in k else "tcp"
                for b in binds or []:
                    hip = b.get("HostIp") or "0.0.0.0"
                    if ":" in hip:  # IPv6 binding: nas01 is IPv4-only
                        continue
                    ports.add((hip, int(b["HostPort"]), proto))
            containers.append({"name": (c.get("Name") or "").lstrip("/"),
                               "project": labels.get("com.docker.compose.project", ""), "ips": ips, "ports": ports})
    host_ips = set()
    for ln in sh(["ip", "-4", "-o", "addr", "show"], timeout=30).stdout.splitlines():
        f = ln.split()
        if "inet" in f:
            host_ips.add(f[f.index("inet") + 1].split("/")[0])
    listeners = set()
    for ln in sh(["ss", "-H", "-l", "-n", "-t", "-u"], timeout=30).stdout.splitlines():
        f = ln.split()
        if len(f) >= 5 and f[0] in ("tcp", "udp"):
            a = _parse_addr(f[4])
            if a:
                listeners.add((a[0], a[1], f[0]))
    return Live(containers, host_ips, listeners)


def compose_networks(compose):
    """compose network key -> (docker network name, external?)"""
    out = {}
    for k, v in (compose.get("networks") or {}).items():
        v = v or {}
        out[k] = (v.get("name") or k, bool(v.get("external")))
    return out


def desired_ips(compose):
    """[(service, docker network name, ip)] for static addresses on external (shared) networks."""
    nets, out = compose_networks(compose), []
    for sname, s in (compose.get("services") or {}).items():
        sn = (s or {}).get("networks")
        if not isinstance(sn, dict):
            continue
        for k, v in sn.items():
            ip = (v or {}).get("ipv4_address")
            name, ext = nets.get(k, (k, False))
            if ip and ext:
                out.append((sname, name, ip))
    return out


def desired_ports(compose):
    """[(service, host_ip, published port, proto)] from long-form port mappings."""
    out = []
    for sname, s in (compose.get("services") or {}).items():
        for p in (s or {}).get("ports") or []:
            if isinstance(p, dict) and p.get("published"):
                pub = str(p["published"]).split("-", 1)[0]
                out.append((sname, p.get("host_ip") or "0.0.0.0", int(pub), p.get("protocol", "tcp")))
    return out


def lan_in_use(ip):
    """True if something on the LAN answers for ip (ping, or a resolved neighbour entry)."""
    if sh(["ping", "-c", "2", "-W", "1", "-n", "-q", ip], timeout=10).returncode == 0:
        return True
    n = sh(["ip", "neigh", "show", "to", ip], timeout=10).stdout
    return "lladdr" in n and not any(s in n for s in ("FAILED", "INCOMPLETE"))


def conflicts(name, compose, live, shared_nets, probe_lan=True):
    """Static-IP and host-port conflicts of `compose` against what is live on nas01, ignoring the app's own
    containers (project ix-<name>). Returns a list of human-readable problems (empty = OK)."""
    own = f"ix-{name}"
    mine = [c for c in live.containers if c["project"] == own]
    others = [c for c in live.containers if c["project"] != own]
    probs = []
    for svc, net, ip in desired_ips(compose):
        holder = next((c["name"] for c in others if c["ips"].get(net) == ip), None)
        if holder:
            probs.append(f"{svc}: {net} address {ip} is in use by container {holder}")
            continue
        if ip in live.host_ips:
            probs.append(f"{svc}: {net} address {ip} is a nas01 host address")
            continue
        if (shared_nets.get(net) or {}).get("driver") == "macvlan" and probe_lan \
                and not any(c["ips"].get(net) == ip for c in mine) and lan_in_use(ip):
            probs.append(f"{svc}: {net} address {ip} answers on the LAN (another host has it)")
    own_binds = set().union(*[c["ports"] for c in mine]) if mine else set()
    for svc, hip, port, proto in desired_ports(compose):
        if hip != "0.0.0.0" and hip not in live.host_ips:
            probs.append(f"{svc}: host_ip {hip} is not configured on nas01")
            continue
        match = lambda a: a[1] == port and a[2] == proto and (a[0] == hip or "0.0.0.0" in (a[0], hip))
        holder = next((c["name"] for c in others if any(match(b) for b in c["ports"])), None)
        if holder:
            probs.append(f"{svc}: {hip}:{port}/{proto} is published by container {holder}")
            continue
        if any(match(l) for l in live.listeners) and not any(match(b) for b in own_binds):
            probs.append(f"{svc}: {hip}:{port}/{proto} is already bound by a process on nas01")
    return probs


def images(compose):
    return sorted({(s or {}).get("image") for s in (compose.get("services") or {}).values() if (s or {}).get("image")})


def image_present(img):
    return sh(["docker", "image", "inspect", "--format", "{{.Id}}", img], timeout=60).returncode == 0


def preflight_tmp(ctx):
    base = os.path.join(ctx.cfg["state_dir"], "preflight")
    os.makedirs(base, mode=0o700, exist_ok=True)
    os.chmod(base, 0o700)
    return tempfile.mkdtemp(dir=base, prefix="pf-")


def write_secret_file(path, content, uid=None, gid=None, mode=0o600):
    """New file (O_EXCL, never follows a symlink); owned by the caller (root) unless uid/gid are given."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    data = content if isinstance(content, (bytes, bytearray)) else content.encode()
    with os.fdopen(fd, "wb") as f:
        f.write(data)
        if uid is not None and (uid, gid) != (os.getuid(), os.getgid()):
            os.fchown(f.fileno(), uid, gid)
        os.fchmod(f.fileno(), mode)


def compose_config_check(name, compose, ctx):
    """`docker compose config` on the rendered compose (written to a root-only temp dir, deleted right after;
    output is never printed, errors are masked). Returns None or an error string."""
    tmp = preflight_tmp(ctx)
    try:
        p = os.path.join(tmp, "compose.yaml")
        write_secret_file(p, yaml.safe_dump(compose, sort_keys=False, default_flow_style=False, width=1000))
        r = sh(["docker", "compose", "-f", p, "-p", f"preflight-{name}", "config", "--quiet"], timeout=60)
        if r.returncode != 0:
            return "docker compose config: " + ctx.mask(r.stderr.strip()[-400:] or f"exit {r.returncode}")
        return None
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def _stage_validate_dir(tmp, idx, src_dir, mine, svc_user):
    """Build a staging directory containing non-templated regular files from src_dir plus rendered `files:`."""
    d = os.path.join(tmp, f"v{idx}")
    os.mkdir(d, 0o700)
    uid, gid = parse_owner(mine[0][0].get("owner"))
    dir_mode = os.lstat(src_dir).st_mode & 0o777 if os.path.isdir(src_dir) and not os.path.islink(src_dir) else (
        0o755 if svc_user and str(svc_user) != f"{uid}:{gid}" else 0o700)
    if (uid, gid) != (os.getuid(), os.getgid()):
        os.chown(d, uid, gid)
    os.chmod(d, dir_mode)
    templated = {os.path.normpath(fs["dest"]) for fs, _ in mine}

    def copy_tree(cur_src, cur_dst):
        for entry in os.scandir(cur_src):
            if entry.is_symlink():
                continue
            dst_p = os.path.join(cur_dst, entry.name)
            if entry.is_dir(follow_symlinks=False):
                os.mkdir(dst_p, 0o755)
                st = entry.stat(follow_symlinks=False)
                if (st.st_uid, st.st_gid) != (os.getuid(), os.getgid()):
                    os.chown(dst_p, st.st_uid, st.st_gid)
                os.chmod(dst_p, st.st_mode & 0o777)
                copy_tree(entry.path, dst_p)
            elif entry.is_file(follow_symlinks=False) and os.path.normpath(entry.path) not in templated:
                fd = os.open(entry.path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                try:
                    st = os.fstat(fd)
                    if stat.S_ISREG(st.st_mode) and st.st_size <= MAX_FILE:
                        data = os.read(fd, MAX_FILE + 1)
                        write_secret_file(dst_p, data, st.st_uid, st.st_gid, st.st_mode & 0o777)
                finally:
                    os.close(fd)

    if os.path.isdir(src_dir) and not os.path.islink(src_dir):
        copy_tree(src_dir, d)
    for fs, content in mine:
        fu, fg = parse_owner(fs.get("owner"))
        out_p = os.path.join(d, os.path.relpath(os.path.normpath(fs["dest"]), src_dir))
        parent = os.path.dirname(out_p)
        if not os.path.isdir(parent):
            os.makedirs(parent, mode=0o755, exist_ok=True)
            if (fu, fg) != (os.getuid(), os.getgid()):
                os.chown(parent, fu, fg)
        if os.path.lexists(out_p):
            os.unlink(out_p)
        write_secret_file(out_p, content, fu, fg, int(str(fs.get("mode", "0600")), 8))
    return d


def run_validate(name, meta, compose, rendered_files, ctx):
    """app.yaml `validate`: run the service's target image (no network, read-only, no caps) with the service's
    volumes, where every volume source that receives rendered files is replaced by a staged file or temp dir
    holding the fully rendered files (preserving host siblings). Returns None or an error string."""
    v = meta.get("validate")
    if not v:
        return None
    svc = (compose.get("services") or {}).get(v["service"])
    if not svc:
        return f"validate: service {v['service']} not in compose"
    img = svc["image"]
    if not image_present(img):
        if ctx.dry:
            ctx.log(f"{name}: validate skipped: image {img} not present yet (would be pulled first)")
            return None
        return f"validate: image {img} not present"
    tmp = preflight_tmp(ctx)
    try:
        args = ["docker", "run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp:size=64m",
                "--entrypoint", v.get("entrypoint", v["service"])]
        if svc.get("user"):
            args += ["--user", str(svc["user"])]
        for i, vol in enumerate(svc.get("volumes") or []):
            if not isinstance(vol, str):
                continue
            parts = vol.split(":")
            src, dst = parts[0], parts[1] if len(parts) > 1 else parts[0]
            src_c = os.path.normpath(src) if src.startswith("/") else src.rstrip("/")
            single = next(((fs, c) for fs, c in rendered_files
                           if fs["dest"].startswith("/") and os.path.normpath(fs["dest"]) == src_c), None)
            if single is not None:
                fs, content = single
                fu, fg = parse_owner(fs.get("owner"))
                f = os.path.join(tmp, f"f{i}-{os.path.basename(src_c)}")
                write_secret_file(f, content, fu, fg, int(str(fs.get("mode", "0600")), 8))
                src = f
            elif src_c.startswith("/"):
                mine = [(fs, c) for fs, c in rendered_files
                        if fs["dest"].startswith("/") and os.path.normpath(fs["dest"]) != src_c
                        and os.path.commonpath([src_c, os.path.normpath(fs["dest"])]) == src_c]
                if mine:
                    src = _stage_validate_dir(tmp, i, src_c, mine, svc.get("user"))
            if src.startswith("/"):
                args += ["-v", f"{src}:{dst}:ro"]
        r = sh(args + [img] + [str(a) for a in v.get("args", [])], timeout=int(v.get("timeout", 120)))
        if r.returncode != 0:
            out = (r.stderr.strip() or r.stdout.strip())[-600:]
            return f"validate ({v['service']}): " + ctx.mask(out or f"exit {r.returncode}")
        return None
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def preflight(name, meta, compose, rendered_files, ctx):
    """All checks that must pass before a running app is touched. Returns a list of problems (empty = OK).
    Not dry-run: missing images are pulled here, before the old containers are stopped."""
    pf = cfg_get(ctx.cfg, "preflight")
    if not pf["enabled"]:
        return []
    probs = []
    e = compose_config_check(name, compose, ctx)
    if e:
        probs.append(e)
    if ctx.live is None:
        ctx.live = collect_live()
    probs += conflicts(name, compose, ctx.live, ctx.shared_nets, pf["probe_lan"])
    for img in images(compose):
        if image_present(img):
            continue
        if ctx.dry or not pf["pull_images"]:
            ctx.log(f"{name}: image {img} not present" + (" -- would pull" if ctx.dry else ""))
            continue
        ctx.log(f"{name}: pulling {img}")
        r = sh(["docker", "pull", "--quiet", img], timeout=1800)
        if r.returncode != 0:
            probs.append(f"pull {img} failed: {ctx.mask(r.stderr.strip()[-300:])}")
    if not probs and meta.get("validate"):
        e = run_validate(name, meta, compose, rendered_files, ctx)
        if e:
            probs.append(e)
        else:
            ctx.log(f"{name}: validate ({meta['validate']['service']}, target image) OK")
    return probs


# ---- drift (live edited outside git) ---------------------------------------------------------------------
def _fp_key(ctx):
    if "_fpkey" not in ctx.__dict__:
        p = os.path.join(ctx.cfg["state_dir"], ".fingerprint-key")
        try:
            k, _ = read_file_st(p)
        except RuntimeError:
            k = None
        if not k:
            k = _secrets.token_hex(32)
            if not ctx.dry:
                write_secret_file(p, k)
        ctx._fpkey = k.strip().encode()
    return ctx._fpkey


def fingerprint(ctx, obj):
    """Keyed hash (HMAC, key in the root-only state dir) so the state file never holds an unsalted secret hash."""
    data = obj if isinstance(obj, str) else json.dumps(obj, sort_keys=True, default=str)
    return hmac.new(_fp_key(ctx), data.encode(), hashlib.sha256).hexdigest()[:32]


def live_fingerprints(ctx, live_compose, meta):
    fp = {"compose": fingerprint(ctx, live_compose) if live_compose is not None else None, "files": {}, "ini": {}}
    for fs in meta.get("files", []):
        try:
            cur = read_file(fs["dest"])
        except RuntimeError:
            cur = None
        fp["files"][fs["dest"]] = fingerprint(ctx, cur) if cur is not None else None
    for spec in meta.get("ini", []):
        try:
            have = ini_values(spec["path"]) if os.path.lexists(spec["path"]) else {}
        except RuntimeError:
            have = {}
        fp["ini"][spec["path"]] = fingerprint(ctx, {k: have.get(k) for k in spec["set"]})
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


def save_state(ctx):
    """Persist ctx.state atomically in live mode when state_dir exists."""
    sd = (ctx.cfg or {}).get("state_dir")
    if ctx.dry or not sd or not os.path.isdir(sd):
        return
    atomic_json(os.path.join(sd, "state.json"), ctx.state)


def desired_fingerprints(ctx, desired_compose, rendered_files, meta):
    """Compute keyed fingerprints for desired compose, rendered files, and desired ini settings."""
    fp = {"compose": fingerprint(ctx, desired_compose), "files": {}, "ini": {}}
    for fs, content in rendered_files:
        fp["files"][fs["dest"]] = fingerprint(ctx, content)
    for spec in meta.get("ini", []):
        fp["ini"][spec["path"]] = fingerprint(ctx, {k: str(v) for k, v in spec["set"].items()})
    return fp


def pin_rollback_images(name, live_compose, desired_compose, ctx):
    """Best-effort local tag of previous images before updating so offline rollbacks do not need a pull."""
    if ctx.dry or not live_compose or live_compose == desired_compose:
        return
    desired_svcs = (desired_compose or {}).get("services") or {}
    for svc_name, old_svc in (live_compose.get("services") or {}).items():
        old_img = (old_svc or {}).get("image")
        new_img = (desired_svcs.get(svc_name) or {}).get("image")
        if old_img and old_img != new_img and image_present(old_img):
            r = sh(["docker", "image", "tag", old_img, f"ix-rollback/{name}/{svc_name}:prev"], timeout=30)
            if r.returncode != 0:
                ctx.log(f"{name}: warning pinning rollback image {svc_name}: {r.stderr.strip()[-200:]}")


def _env_lookup(svc_cfg, key):
    env = (svc_cfg or {}).get("environment")
    if isinstance(env, dict):
        return env.get(key)
    if isinstance(env, list):
        prefix = f"{key}="
        for entry in env:
            if isinstance(entry, str) and entry.startswith(prefix):
                return entry[len(prefix):]
    return None


def _grafana_sync(url, user, password, folders, items, pid, dry):
    """Sync Grafana folders and dashboards via nsenter inside the container netns using stdin JSON."""
    script = r"""
import base64, json, sys, urllib.error, urllib.request
req = json.load(sys.stdin)
base = req["url"].rstrip("/")
auth = base64.b64encode((req["user"] + ":" + req["password"]).encode()).decode()
def api(path, method="GET", body=None):
    hdr = {"Authorization": "Basic " + auth, "Accept": "application/json"}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        hdr["Content-Type"] = "application/json"
    r = urllib.request.Request(base + path, data=data, method=method, headers=hdr)
    with urllib.request.urlopen(r, timeout=15) as resp:
        return json.loads(resp.read().decode())

live_folders = {f["uid"]: f for f in api("/api/folders?limit=200")}
for title, uid in req["folders"].items():
    if uid not in live_folders and not req["dry"]:
        api("/api/folders", "POST", {"uid": uid, "title": title})

out = []
for item in req["dashboards"]:
    uid, fuid, want = item["uid"], item["folderUid"], item["dashboard"]
    try:
        cur = api("/api/dashboards/uid/" + uid)
        live = dict(cur.get("dashboard") or {})
        live.pop("id", None)
        live.pop("version", None)
        live_fuid = (cur.get("meta") or {}).get("folderUid", "")
        if live == want and live_fuid == fuid:
            out.append({"uid": uid, "file": item["file"], "status": "ok"})
            continue
        rec = {"uid": uid, "file": item["file"], "status": "changed" if req["dry"] else "updated",
               "live_fuid": live_fuid, "live_dashboard": live}
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
        rec = {"uid": uid, "file": item["file"], "status": "missing" if req["dry"] else "created"}
    if not req["dry"]:
        api("/api/dashboards/db", "POST", {"dashboard": {**want, "id": None}, "folderUid": fuid, "overwrite": True})
    out.append(rec)
print(json.dumps(out))
"""
    payload = {"url": url, "user": user, "password": password, "folders": folders, "dashboards": items, "dry": bool(dry)}
    r = sh(["nsenter", f"--net=/proc/{pid}/ns/net", sys.executable, "-c", script],
           timeout=120, input=json.dumps(payload))
    if r.returncode != 0:
        raise RuntimeError((r.stderr.strip() or r.stdout.strip())[-400:] or f"exit {r.returncode}")
    return json.loads(r.stdout.strip() or "[]")


def reconcile_dashboards(appdir, name, meta, desired, applied_entry, ctx):
    """Ensure declared Grafana dashboards and folders match git; return (ok, dash_fingerprints_or_None)."""
    dspec = meta.get("dashboards")
    if not dspec:
        return True, None
    ddir = os.path.join(appdir, dspec["dir"])
    if not os.path.isdir(ddir):
        ctx.log(f"{name}: dashboards dir {dspec['dir']} not found")
        return False, None
    folders = dict(dspec.get("folders") or {})
    default_folder = dspec.get("default_folder", "infrastructure")
    fmap = dspec.get("folder_map") or {}
    items, want_fp = [], {}
    for fn in sorted(os.listdir(ddir)):
        if not fn.endswith(".json"):
            continue
        with open(os.path.join(ddir, fn)) as f:
            dash = json.load(f)
        dash.pop("id", None)
        dash.pop("version", None)
        uid = dash.get("uid")
        if not uid:
            ctx.log(f"{name}: dashboard {fn} missing uid")
            return False, None
        fname = fmap.get(fn, default_folder)
        fuid = folders.get(fname, "")
        want_fp[uid] = fingerprint(ctx, {"folderUid": fuid, "dashboard": dash})
        items.append({"uid": uid, "file": fn, "folderUid": fuid, "dashboard": dash})

    svc = dspec.get("service", name)
    svc_cfg = (desired.get("services") or {}).get(svc)
    user = _env_lookup(svc_cfg, dspec.get("user_env", "GF_SECURITY_ADMIN_USER"))
    password = _env_lookup(svc_cfg, dspec.get("pass_env", "GF_SECURITY_ADMIN_PASSWORD"))
    if not user or not password:
        ctx.log(f"{name}: dashboard sync missing credentials env on service {svc}")
        return False, None

    try:
        pid = container_pid(name, svc)
    except Exception as e:  # noqa: BLE001
        if ctx.dry:
            ctx.log(f"{name}: dashboards JSON validated ({len(items)} files; container not running in dry-run)")
            return True, None
        ctx.log(f"{name}: dashboards sync failed locating container: {ctx.mask(str(e))}")
        return False, None

    prev_dash = (applied_entry or {}).get("dashboards") or {}
    try:
        res = _grafana_sync(dspec.get("url", "http://127.0.0.1:3000"), user, password, folders, items, pid, ctx.dry)
    except Exception as e:  # noqa: BLE001
        ctx.log(f"{name}: dashboards sync FAILED: {ctx.mask(str(e))}")
        return False, None

    for row in res:
        st = row.get("status")
        uid, fn = row["uid"], row["file"]
        if st == "ok":
            continue
        if "live_dashboard" in row:
            lfp = fingerprint(ctx, {"folderUid": row.get("live_fuid", ""), "dashboard": row["live_dashboard"]})
            if uid in prev_dash and prev_dash[uid] != lfp:
                ctx.drift[name] = True
                ctx.event("drift", name)
                ctx.log(f"{name}: DRIFT -- dashboard {uid} ({fn}) changed outside git"
                        + (" (will be re-imported)" if ctx.dry else " (re-imported from git)"))
                continue
        ctx.log(f"{name}: dashboard {uid} ({fn}) {'would be ' + st if ctx.dry else st}")
    return True, want_fp


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


def load_app(appdir):
    with open(os.path.join(appdir, "compose.yaml")) as f:
        src = yaml.safe_load(f)
    meta = {}
    if os.path.exists(os.path.join(appdir, "app.yaml")):
        with open(os.path.join(appdir, "app.yaml")) as f:
            meta = yaml.safe_load(f) or {}
    return src, meta


def reconcile_app(appdir, ctx, meta=None):
    name = os.path.basename(appdir)
    src, m = load_app(appdir)
    meta = m if meta is None else meta

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

    # drift: live state that differs from what this reconciler last applied = edited outside git (TrueNAS UI)
    applied = ctx.state.setdefault("applied", {})
    drifted = drift_report(applied.get(name), live_fingerprints(ctx, live, meta)) if app else []
    ctx.drift[name] = bool(drifted)
    if drifted:
        ctx.event("drift", name)
        ctx.log(f"{name}: DRIFT -- live {', '.join(drifted)} changed outside git since the last deploy"
                + (" (will be re-applied from git)" if not ctx.dry else ""))

    compose_diff = live != desired
    dirs_changed = any([ensure_dir(d, ctx, name) for d in meta.get("dirs", [])])  # list: ensure ALL dirs

    file_changes = []
    for fs, content in rendered_files:
        cur = read_file(fs["dest"])
        if cur != content:
            file_changes.append((fs, content, cur))

    ini_todo, ini_missing = [], []
    for spec in meta.get("ini", []):
        if not os.path.lexists(spec["path"]):
            ctx.log(f"{name}: ini file {spec['path']} missing (app not initialised yet?) -- skipped")
            continue
        ch, missing = ini_changes(spec)
        for k in missing:
            # a pinned setting that matches nothing is a silent no-op -> fail the app (keys are verbatim, section-blind)
            ctx.log(f"{name}: ERROR ini key {k!r} not present in {spec['path']} -- app.yaml does not match the file")
            ini_missing.append(k)
        if ch:
            ini_todo.append((spec, ch))

    want_fp = desired_fingerprints(ctx, desired, rendered_files, meta)
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
            fp = live_fingerprints(ctx, desired, meta)
            if dash_fp is not None:
                fp["dashboards"] = dash_fp
            elif "dashboards" in (applied.get(name) or {}):
                fp["dashboards"] = applied[name]["dashboards"]
            applied[name] = fp
            (ctx.state.get("pending_redeploy") or {}).pop(name, None)
            save_state(ctx)

    if not (compose_diff or file_changes or ini_todo or dirs_changed or pending or unconverged):
        state = app.get("state") if app else None
        if ctx.dry:  # dry-run exercises every preflight, so a bad image/config/IP shows up before it is deployed
            ctx.log(f"{name}: config in sync, state is {state} (would start)" if state != "RUNNING"
                    else f"{name}: in sync ({state})")
            probs = preflight(name, meta, desired, rendered_files, ctx)
            for p in probs:
                ctx.log(f"{name}: PREFLIGHT FAILED: {p}")
            if probs:
                ctx.event("preflight_failure", name)
                return False
            dok, _ = reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
            if not dok:
                return False
            ctx.log(f"{name}: preflight OK")
            return not ini_missing
        if state != "RUNNING":
            ctx.log(f"{name}: config in sync, state is {state} -- starting")
            timeout = int(meta.get("deploy_timeout", cfg_get(ctx.cfg, "deploy_timeout")))
            ctx.live = None
            try:
                ensure_started(name, ctx)
                ok = wait_running(name, ctx, timeout) and healthy(meta, name, ctx)
            except Exception as e:  # noqa: BLE001
                ctx.log(f"{name}: start FAILED: {ctx.mask(str(e))}")
                ok = False
            if ok:
                dok, dash_fp = reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
                if not dok:
                    ctx.event("deploy_failure", name)
                    return False
                ctx.log(f"{name}: started (RUNNING{', healthy' if meta.get('health') else ''})")
                record_applied(dash_fp)
                ctx.ui_changed |= apply_ui(name, meta, desired, ctx)
                return not ini_missing
            ctx.event("deploy_failure", name)
            ctx.log(f"{name}: start/health FAILED (was {state})")
            return False
        dok, dash_fp = reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
        if dok:
            ctx.log(f"{name}: in sync ({state})")
            record_applied(dash_fp)
        else:
            ctx.log(f"{name}: dashboard sync FAILED ({state})")
        ctx.ui_changed |= apply_ui(name, meta, desired, ctx)
        return dok and (not ini_missing)

    # ---- report ----
    if (pending or unconverged) and not (compose_diff or file_changes or ini_todo or dirs_changed):
        ctx.log(f"{name}: recovering unfinished/unconverged config deploy")
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

    # ---- preflight: nothing running is touched unless all checks pass ----
    probs = preflight(name, meta, desired, rendered_files, ctx)
    for p in probs:
        ctx.log(f"{name}: PREFLIGHT FAILED: {p}")
    if probs:
        ctx.event("preflight_failure", name)
        ctx.log(f"{name}: not deployed (preflight)" + (" (dry-run)" if ctx.dry else ""))
        return False
    ctx.log(f"{name}: preflight OK")
    if ctx.dry:
        reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
        ctx.log(f"{name}: would deploy (dry-run)")
        return True

    # ---- apply, with rollback ----
    # Every change -- compose, templated files AND ini patches -- is captured first so a rollback restores all of
    # them, and the app is explicitly (re)started at the end of both paths: on TrueNAS `app.update` does NOT start
    # an app that is stopped (2026-11-11: qbittorrent was left STOPPED after an ini patch + update).
    timeout = int(meta.get("deploy_timeout", cfg_get(ctx.cfg, "deploy_timeout")))
    prev_files, prev_ini = [], []
    bdir = os.path.join(backup_root(ctx.cfg), name)
    ctx.live = None  # anything we do now changes the live inventory
    ctx.event("deploy", name)
    ctx.state.setdefault("pending_redeploy", {})[name] = int(_now())
    save_state(ctx)
    try:
        pin_rollback_images(name, live, desired, ctx)
        if ini_todo and app:
            ctx.log(f"{name}: stopping to patch ini")
            midclt("app.stop", name, job=True)
        for spec, _ in ini_todo:
            text, st = read_file_st(spec["path"])
            prev_ini.append((spec["path"], text, st))
            ini_apply(spec, bdir)
        for fs, content, cur in file_changes:
            prev_files.append((fs, cur))
            write_file(fs["dest"], content, fs.get("owner"), int(str(fs.get("mode", "0600")), 8), bdir)
        if compose_diff:
            deploy_compose(name, desired, exists=bool(app))
            ctx.log(f"{name}: compose {'updated' if app else 'created'}")
        elif file_changes or dirs_changed or pending or unconverged:
            midclt("app.redeploy", name, job=True)
            ctx.log(f"{name}: redeployed for file changes")
        ensure_started(name, ctx)
        if wait_running(name, ctx, timeout) and healthy(meta, name, ctx):
            dok, dash_fp = reconcile_dashboards(appdir, name, meta, desired, prev_applied, ctx)
            if not dok:
                raise RuntimeError("dashboards sync failed")
            ctx.log(f"{name}: converged (RUNNING{', healthy' if meta.get('health') else ''})")
            record_applied(dash_fp)
            ctx.ui_changed |= apply_ui(name, meta, desired, ctx)
            return not ini_missing
        raise RuntimeError("not RUNNING/healthy")
    except Exception as e:
        ctx.event("deploy_failure", name)
        ctx.log(f"{name}: deploy FAILED: {ctx.mask(str(e))} -- rolling back")
        rollback(name, meta, app, live, prev_ini, prev_files, timeout, ctx)
        return False


def rollback(name, meta, app, live, prev_ini, prev_files, timeout, ctx):
    """Restore ini + files, then re-apply the exact previous compose with app.update (the same path as a forward
    deploy; it recreates the containers, so restored files are picked up too) and verify RUNNING + health.
    A failed *create* has no previous version: files are restored and nothing is redeployed."""
    if app is None or live is None:
        try:
            for fs, cur in prev_files:
                if cur is not None:
                    write_file(fs["dest"], cur, fs.get("owner"), int(str(fs.get("mode", "0600")), 8))
        except Exception as e2:  # noqa: BLE001
            ctx.log(f"{name}: restoring files after failed create FAILED: {ctx.mask(str(e2))}")
        (ctx.state.get("pending_redeploy") or {}).pop(name, None)
        save_state(ctx)
        a = app_state(name)
        ctx.log(f"{name}: create failed; no previous version to roll back to"
                + (f" (app now exists in state {a.get('state')} -- needs attention)" if a else " (app absent)"))
        return False
    ctx.event("rollback", name)
    try:
        if prev_ini:
            midclt("app.stop", name, job=True)
        for path, text, st in prev_ini:
            write_file(path, text, f"{st.st_uid}:{st.st_gid}", st.st_mode & 0o777)
            ctx.log(f"{name}: restored {path}")
        for fs, cur in prev_files:
            if cur is not None:
                write_file(fs["dest"], cur, fs.get("owner"), int(str(fs.get("mode", "0600")), 8))
        deploy_compose(name, live, exists=True)
        ctx.log(f"{name}: re-applied previous compose (app.update)")
        ensure_started(name, ctx)
        ok = wait_running(name, ctx, timeout) and healthy(meta, name, ctx)
    except Exception as e2:  # noqa: BLE001
        ctx.log(f"{name}: ROLLBACK FAILED: {ctx.mask(str(e2))}")
        ok = False
    if ok:
        ctx.state.setdefault("applied", {})[name] = live_fingerprints(ctx, live, meta)
        (ctx.state.get("pending_redeploy") or {}).pop(name, None)
        save_state(ctx)
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
            results[name] = reconcile_app(os.path.join(root, name), ctx, metas.get(name))
        except ConnectUnavailableError as e:
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


# ---- CI deploy gate --------------------------------------------------------------------------------------
class GateAPIError(Exception):
    pass


def check_verdict(payload):
    """GitHub check-runs response -> 'success' | 'failure' | 'pending' | 'none'."""
    runs = payload.get("check_runs") or []
    if not runs:
        return "none"
    run = max(runs, key=lambda r: (r.get("started_at") or "", r.get("id") or 0))
    if run.get("status") != "completed":
        return "pending"
    return "success" if run.get("conclusion") == "success" else "failure"


def repo_slug(url):
    m = re.search(r"github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$", url)
    if not m:
        raise GateAPIError(f"cannot derive owner/repo from {url}")
    return m.group(1), m.group(2)


def make_checker(cfg, ctx, cache):
    gate = cfg_get(cfg, "ci_gate")
    owner, repo = repo_slug(cfg["repo"])
    token = ctx.resolve(gate["token_ref"]) if gate.get("token_ref") else None

    def check(sha):
        if cache.get(sha) in ("success", "failure"):
            return cache[sha]
        url = (f"{gate['api_url'].rstrip('/')}/repos/{owner}/{repo}/commits/{sha}/check-runs?"
               + urllib.parse.urlencode({"check_name": gate["check_name"], "filter": "latest"}))
        h = {"Accept": "application/vnd.github+json", "User-Agent": "truenas-apps-reconciler",
             "X-GitHub-Api-Version": "2022-11-28"}
        if token:
            h["Authorization"] = f"Bearer {token}"
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=h), timeout=int(gate["timeout"])) as r:
                v = check_verdict(json.load(r))
        except (urllib.error.URLError, OSError, ValueError) as e:
            raise GateAPIError(f"GitHub check-runs API: {e}") from e
        if v in ("success", "failure"):
            cache[sha] = v
        return v
    return check


def gate_decide(head, current, candidates, check, is_ancestor, same_tree):
    """Pick the commit to check out.
    head: fetched branch tip; current: commit checked out now (None on first clone); candidates: commits touching
    truenas/, newest first. check(sha) -> verdict (raises GateAPIError). Never moves backwards from `current`.
    Returns (target, status, reason) with status in pass | unchanged | held | outage."""
    if current and same_tree(head, current):
        return head, "unchanged", "truenas/ unchanged since the deployed commit"
    skipped = []
    try:
        for c in candidates:
            if current and (c == current or is_ancestor(c, current)):
                break  # reached what is deployed: never rewind
            v = check(c)
            if v == "success":
                if c == candidates[0]:
                    return head, "pass", f"{c[:8]} passed"
                return c, "held", f"newest passing truenas commit {c[:8]}; held back: {', '.join(skipped)}"
            skipped.append(f"{c[:8]}={v}")
    except GateAPIError as e:
        return current or None, "outage", f"{e} -- staying on the deployed commit (fail closed)"
    return current or None, "held", ("no passing truenas commit newer than the deployed one; "
                                     + (", ".join(skipped) or "none checked"))


def git_pull(cfg, ctx, gate_on=True, partial=False):
    co, repo, br = cfg["checkout"], cfg["repo"], cfg.get("branch", "main")
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")

    def run(*a, check=True):
        return subprocess.run(["git", "-C", co, *a], check=check, capture_output=True, text=True, env=env,
                              timeout=120)
    gate = cfg_get(cfg, "ci_gate")
    fresh = not os.path.isdir(os.path.join(co, ".git"))
    if fresh:
        subprocess.run(["git", "clone", "--depth", "1", "--filter=blob:none", "--sparse", "-b", br, repo, co],
                       check=True, capture_output=True, text=True, env=env, timeout=300)
        run("sparse-checkout", "set", "truenas")
    cache_p = os.path.join(cfg["state_dir"], "ci-gate.json")
    gstate = load_json(cache_p, {})
    current = None
    if not fresh:
        # the last commit the gate let through (not merely HEAD: a fresh clone that found nothing passing still has
        # the branch tip checked out, recorded as deployed=None). First run with the gate (no record yet): trust
        # what is checked out, which is what is deployed.
        current = gstate["deployed"] if "deployed" in gstate else run("rev-parse", "HEAD").stdout.strip()
    run("fetch", "--depth", str(int(gate["history_depth"]) if gate_on else 1), "origin", br)
    sp = os.path.join(co, ".git", "shallow")
    if gate_on and current and os.path.exists(sp):
        for _ in range(4):
            shallow_now = set(open(sp).read().split())
            if run("cat-file", "-e", f"{current}^{{commit}}", check=False).returncode == 0 and current not in shallow_now:
                break
            run("fetch", "--deepen", "50", "origin", br)
    head = run("rev-parse", "FETCH_HEAD").stdout.strip()
    info = {"head": head, "status": "disabled", "reason": "CI gate disabled", "api_ok": True}
    if gate_on:
        shallow = set()
        if os.path.exists(sp):
            shallow = set(open(sp).read().split())
        cands = [c for c in run("log", "--format=%H", "FETCH_HEAD", "--", "truenas").stdout.split()
                 if c not in shallow]  # a shallow boundary commit "touches" everything
        tree = lambda c: run("rev-parse", f"{c}:truenas", check=False).stdout.strip()
        cache = gstate.get("verdicts", {})
        try:
            check = make_checker(cfg, ctx, cache)
        except Exception as e:  # noqa: BLE001  (token ref unresolvable / bad repo url)
            def check(_sha, e=e):
                raise GateAPIError(str(e))
        target, status, reason = gate_decide(
            head, current, cands, check,
            lambda a, b: run("merge-base", "--is-ancestor", a, b, check=False).returncode == 0,
            lambda a, b: tree(a) != "" and tree(a) == tree(b))
        info.update(status=status, reason=reason, api_ok=status != "outage")
        if target is None:  # fresh clone and nothing passed (or API down): deploy nothing this run
            info["deploy"] = False
        elif run("cat-file", "-e", f"{target}^{{commit}}", check=False).returncode != 0:
            info.update(deploy=False, status="outage", api_ok=True,
                        reason=f"last good commit {target[:8]} is no longer in the fetched history")
            target = None
        ctx.log(f"ci-gate: {info['status']}: {info['reason']}")
        if not ctx.dry and not partial:
            if len(cache) > 500:
                cache = dict(list(cache.items())[-500:])
            gstate["verdicts"] = cache
            if info.get("deploy", True):
                gstate["deployed"] = target
            elif current is None:
                gstate["deployed"] = None
            atomic_json(cache_p, gstate)
    else:
        target = head
        ctx.log("ci-gate: DISABLED -- deploying the branch tip unchecked")
        if not ctx.dry and not partial:  # keep the last-good marker current so re-enabling the gate starts from here
            gstate["deployed"] = head
            atomic_json(cache_p, gstate)
    cur_head = run("rev-parse", "HEAD").stdout.strip()
    if ctx.dry or partial:
        # Never mutate the live shared checkout during --dry-run or single-app runs.
        if target is not None and target != cur_head:
            wt = tempfile.mkdtemp(dir=preflight_tmp(ctx), prefix="wt-")
            try:
                run("worktree", "add", "--detach", wt, target)
                info["worktree"] = wt
                info["commit"] = target
            except Exception:  # noqa: BLE001
                shutil.rmtree(wt, ignore_errors=True)
                raise
        else:
            info["commit"] = target or cur_head
    else:
        if target is not None:  # deploy=False: the checkout (and this script) is never moved to an ungated commit
            run("reset", "--hard", target)
        info["commit"] = run("rev-parse", "HEAD").stdout.strip()
    ctx.log(f"git {br}@{head[:7]} {'checking' if ctx.dry else 'deploying'} {info['commit'][:7]}")
    return info


def _post_commit_status(api_url, owner, repo, sha, token, context, state, description, timeout=15):
    url = f"{api_url.rstrip('/')}/repos/{owner}/{repo}/statuses/{sha}"
    body = json.dumps({"state": state, "context": context, "description": description[:140]}).encode()
    req = urllib.request.Request(
        url, data=body, method="POST",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json",
                 "Content-Type": "application/json", "User-Agent": "truenas-apps-reconciler",
                 "X-GitHub-Api-Version": "2022-11-28"})
    with urllib.request.urlopen(req, timeout=int(timeout)):
        pass


def post_github_status(cfg, ctx, results, ok_all, run_info):
    """Post commit status to GitHub for the deployed (and held, if gated) SHA(s); deduplicated via state.json."""
    gs = cfg_get(cfg, "github_status")
    token_ref = gs.get("token_ref") or cfg_get(cfg, "ci_gate").get("token_ref")
    if not gs.get("enabled") or not token_ref:
        return
    try:
        owner, repo = repo_slug(cfg["repo"])
        token = ctx.resolve(token_ref)
    except Exception as e:  # noqa: BLE001
        ctx.log(f"github-status: WARNING {e}")
        return
    commit, head, gate = run_info.get("commit", ""), run_info.get("head", ""), run_info.get("gate", "")
    deploy = run_info.get("deploy", True)
    real_apps = {k: v for k, v in results.items() if not k.startswith("_")}
    bad = sorted(k for k, v in results.items() if not v)
    targets = []
    if deploy and len(commit) == 40:
        if ok_all:
            desc = f"Deployed on nas01 ({len(real_apps)}/{len(real_apps)} apps converged)"
            prev = ctx.state.get("github_status_last_deployed")
            revs = []
            if isinstance(prev, str) and len(prev) == 40 and prev != commit and os.path.isdir(cfg.get("checkout", "")):
                try:
                    r = subprocess.run(
                        ["git", "-C", cfg["checkout"], "rev-list", "--max-count=20", f"{prev}..{commit}"],
                        capture_output=True, text=True, timeout=15)
                    if r.returncode == 0:
                        lines = r.stdout.split()
                        if lines and lines[0] == commit:
                            revs = list(reversed(lines))
                except Exception:  # noqa: BLE001
                    pass
            if len(revs) > 1:
                for sha in revs[:-1]:
                    targets.append((sha, "success",
                                    f"Deployed on nas01 via {commit[:7]} ({len(real_apps)}/{len(real_apps)} apps converged)"))
            targets.append((commit, "success", desc))
        else:
            targets.append((commit, "failure", f"nas01 reconcile failed: {', '.join(bad)}"))
    if len(head) == 40 and head != commit and gate == "held":
        held_desc = f"Held by CI gate on nas01; deployed {commit[:7]}" if commit else "Held by CI gate on nas01"
        targets.append((head, "pending", held_desc))

    sent = ctx.state.setdefault("github_status", {})
    all_posted_ok = True
    for sha, st, desc in targets:
        clipped = desc[:140]
        want = {"state": st, "description": clipped}
        if sent.get(sha) == want:
            continue
        try:
            _post_commit_status(gs["api_url"], owner, repo, sha, token, gs["context"], st, clipped, gs["timeout"])
            sent.pop(sha, None)
            sent[sha] = want
            ctx.log(f"github-status: {sha[:8]} -> {st} ({clipped})")
        except Exception as e:  # noqa: BLE001
            ctx.log(f"github-status: WARNING {sha[:8]}: {e}")
            all_posted_ok = False
    if len(sent) > 100:
        ctx.state["github_status"] = dict(list(sent.items())[-100:])
    if deploy and ok_all and len(commit) == 40 and all_posted_ok:
        ctx.state["github_status_last_deployed"] = commit


# ---- state / housekeeping --------------------------------------------------------------------------------
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


def secure_state(cfg, ctx):
    """State dirs root-only (0700), never following symlinks; report (never touch) group/world-readable files."""
    sd = cfg["state_dir"]
    dirs = [sd]
    br = backup_root(cfg)
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
                add((e.name, BACKUP_TS.sub("", base)), f)
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


# ---- metrics ---------------------------------------------------------------------------------------------
COUNTERS = {  # event kind -> (metric stem, help)
    "deploy": ("deploys", "Deploys attempted (compose/file/ini changes applied)."),
    "deploy_failure": ("deploy_failures", "Deploys that failed (and were rolled back)."),
    "rollback": ("rollbacks", "Rollbacks attempted."),
    "rollback_failure": ("rollback_failures", "Rollbacks that did not end RUNNING and healthy."),
    "preflight_failure": ("preflight_failures", "Deploys refused by preflight (nothing was touched)."),
    "drift": ("drift_detected", "Live app state found changed outside git."),
}


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
    for kind, (stem, help_) in COUNTERS.items():
        fam(f"{stem}_total", "counter", help_, [({"app": a}, c.get(kind, {}).get(a, 0)) for a in apps])
    for kind, (stem, help_) in COUNTERS.items():
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
    now = int(now if now is not None else _now())
    prev_success = None
    prev = os.path.join(d, "apps_gitops.prom")
    for ln in (read_file(prev) or "").splitlines():
        if ln.startswith("truenas_apps_gitops_last_success_timestamp_seconds"):
            prev_success = ln.split()[-1]
    text = render_metrics(results, unmanaged, ok_all, now, mstate or {}, run_info or {}, prev_success)
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


# ---- trigger / host / retired ----------------------------------------------------------------------------
def _trigger_path(cfg, cli_trigger=None):
    if cli_trigger:
        return cli_trigger
    tf = cfg.get("trigger_file")
    if tf:
        return tf
    if str(cfg.get("checkout", "")).startswith("/mnt/flashstor/") and os.path.isdir(os.path.dirname(DEFAULT_TRIGGER_FILE)):
        return DEFAULT_TRIGGER_FILE
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
        uid, gid = parse_owner(spec.get("owner", "0:0"))
        cur, st = read_file_st(dest)
        prev = hap.get(dest)
        if cur is not None and prev and fingerprint(ctx, cur) != prev:
            ctx.log(f"host: DRIFT -- {dest} modified outside git"
                    + (" (would restore)" if ctx.dry else " (restoring from git)"))
        needs_write = cur != want or st is None or (st.st_uid, st.st_gid, st.st_mode & 0o7777) != (uid, gid, mode)
        if not needs_write:
            hap[dest] = fingerprint(ctx, want)
            continue
        if ctx.dry:
            ctx.log(f"host: would update {dest} (dry-run)")
            continue
        bdir = os.path.join(backup_root(ctx.cfg), "_host")
        write_file(dest, want, spec.get("owner", "0:0"), mode, bdir)
        hap[dest] = fingerprint(ctx, want)
        ctx.log(f"host: updated {dest}")
        if spec.get("on_change"):
            r = sh(spec["on_change"], timeout=60)
            if r.returncode != 0:
                ctx.log(f"host: on_change hook failed for {dest}: {r.stderr.strip()[-300:]}")
                ok = False
            else:
                ctx.log(f"host: on_change hook OK ({spec['on_change'][0]})")
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
    live = {x["name"]: x for x in midclt("app.query")}
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
            midclt("app.delete", rname, job=True)
            (ctx.state.get("applied") or {}).pop(rname, None)
            (ctx.state.get("pending_redeploy") or {}).pop(rname, None)
            imgs = sh(["docker", "images", "--format", "{{.Repository}}:{{.Tag}}", f"ix-rollback/{rname}/*"], timeout=30)
            for tag in imgs.stdout.splitlines():
                if tag.strip():
                    sh(["docker", "rmi", tag.strip()], timeout=30)
            ctx.log(f"{rname}: retired custom app deleted")
        except Exception as e:  # noqa: BLE001
            ctx.log(f"{rname}: delete FAILED: {ctx.mask(str(e))}")
            ok = False
    return ok


def cleanup_worktree(checkout, wt):
    if not wt:
        return
    try:
        subprocess.run(["git", "-C", checkout, "worktree", "remove", "--force", wt],
                       capture_output=True, text=True, timeout=60)
    except Exception:  # noqa: BLE001
        pass
    shutil.rmtree(wt, ignore_errors=True)


# ---- main ------------------------------------------------------------------------------------------------
def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=DEFAULT_CONFIG)
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
    t0 = _now()
    with open(a.config) as f:
        cfg = json.load(f)
    sd = cfg["state_dir"]
    os.makedirs(sd, mode=0o700, exist_ok=True)
    lfd = os.open(os.path.join(sd, ".lock"), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    lockf = os.fdopen(lfd, "r+")
    wait_s = a.wait_lock if a.wait_lock is not None else (300 if a.app else 0)
    on_locked = a.if_locked or ("fail" if a.app else "skip")
    lock_deadline = _now() + max(0, wait_s)
    while True:
        try:
            fcntl.flock(lockf, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except BlockingIOError:
            if _now() >= lock_deadline:
                print("another reconcile is running", file=sys.stderr)
                lockf.close()
                return EX_TEMPFAIL if on_locked == "fail" else 0
            _sleep(1)

    tpath = _trigger_path(cfg, a.trigger_file)
    reexec_env = os.environ.get("RECONCILE_GATE_INFO")
    from_self_exec = reexec_env is not None
    if tpath and not a.dry_run:
        try:
            had_poke = consume_trigger(tpath)
        except Exception as e:  # noqa: BLE001
            print(f"trigger error: {e}", file=sys.stderr)
            had_poke = False
        if a.from_trigger and not from_self_exec and not a.no_pull and not had_poke:
            lockf.close()
            return 0

    ctx = Ctx(cfg, a.dry_run)
    if not a.dry_run:
        os.fchmod(lfd, 0o600)
    secure_state(cfg, ctx)
    ctx.state = load_json(os.path.join(sd, "state.json"), {})
    me = os.path.realpath(__file__)
    before = open(me, "rb").read()
    gate_on = cfg_get(cfg, "ci_gate")["enabled"] and not a.no_ci_gate

    ok_all = True
    for pass_no in range(MAX_COALESCE_PASSES):
        pass_t0 = _now() if pass_no > 0 else t0
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
            gi = git_pull(cfg, ctx, gate_on, partial=bool(a.app))
            deploy = gi.get("deploy", True)
            worktree = gi.get("worktree")
            run_info.update(gate=gi["status"], commit=gi["commit"], head=gi["head"], api_ok=gi["api_ok"], deploy=deploy)
            if not a.dry_run and not a.app and os.path.exists(me) and open(me, "rb").read() != before:
                # the pull updated this very script: re-exec the new code (release the lock first) for this run
                ctx.log("reconciler updated by git -- re-executing new version")
                write_runlog(cfg, ctx)
                lockf.close()
                os.environ["RECONCILE_GATE_INFO"] = json.dumps(run_info)  # carried into the new process's metrics
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
                    metas[n] = load_app(os.path.join(root, n))[1]
                except Exception as e:  # noqa: BLE001
                    ctx.log(f"{n}: ERROR reading app.yaml: {e}")
                    raw_meta = read_file(os.path.join(root, n, "app.yaml")) or ""
                    if re.search(r"(?m)^\s*critical\s*:\s*true\b", raw_meta):
                        om = re.search(r"(?m)^\s*order\s*:\s*(-?\d+)\b", raw_meta)
                        metas[n] = {"critical": True, "order": int(om.group(1)) if om else -1}
                    else:
                        metas[n] = {}
            results, unmanaged = {}, 0
            if not a.app and deploy:
                if not reconcile_host(checkout, ctx):
                    results["_host"] = False
            if not ensure_networks(os.path.join(checkout, "truenas", "networks.yaml"), ctx):
                results["_networks"] = False
            if deploy:
                results.update(run_apps(root, apps, metas, ctx))
            else:
                ctx.log("ci-gate: nothing has passed CI yet on a fresh checkout -- no app reconciled")
                results["_ci_gate"] = False
            if not a.app and deploy:
                if not reconcile_retired(checkout, ctx):
                    results["_retired"] = False
            if not a.app:
                live_names = {x["name"] for x in midclt("app.query")}
                for extra in sorted(live_names - set(all_apps)):
                    ctx.log(f"{extra}: on nas01 but NOT in git (unmanaged)")
                    unmanaged += 1
            if ctx.ui_changed and not a.dry_run:
                try:
                    midclt("app.metadata.generate", job=True)
                    ctx.log("UI metadata regenerated")
                except Exception as e:  # noqa: BLE001
                    ctx.log(f"UI metadata regenerate failed: {e}")
            try:
                removed, kept = prune_backups(backup_root(cfg), all_apps, cfg_get(cfg, "backup_retention_days"),
                                              cfg_get(cfg, "backup_keep_newest"), _now(), a.dry_run, ctx.log)
                run_info["backup_bytes"] = kept
                ctx.log(f"backup: {len(removed)} file(s) {'would be ' if a.dry_run else ''}removed by retention; "
                        f"{kept // (1024 * 1024)} MiB kept")
            except Exception as e:  # noqa: BLE001
                ctx.log(f"backup: retention failed: {e}")
            ok_all = all(results.values())
            run_info["duration"] = _now() - pass_t0
            run_info["drift"] = ctx.drift
            run_info["connect_ok"] = ctx.connect_ok
            if deploy and ok_all and not a.app and not a.dry_run and len(run_info.get("commit", "")) == 40:
                ctx.state["converged_commit"] = run_info["commit"]
            run_info["converged"] = (ctx.state.get("converged_commit") == run_info.get("commit")) \
                if (deploy and run_info.get("commit")) else False
            if not a.dry_run:
                mstate = update_metric_state(ctx.state.get("metrics", {}), ctx.events, results, ok_all, not a.app, int(_now()))
                ctx.state["metrics"] = mstate
                if not a.app:
                    post_github_status(cfg, ctx, results, ok_all, run_info)
                atomic_json(os.path.join(sd, "state.json"), ctx.state)
                if not a.app:
                    write_metrics(cfg, results, unmanaged, ok_all, mstate, run_info)
            bad = sorted(k for k, v in results.items() if not v)
            ctx.log(f"run: {'OK' if ok_all else 'FAILED'} ({len(results) - len(bad)}/{len(results)} converged"
                    + (f"; not converged: {', '.join(bad)}" if bad else "") + f") in {run_info['duration']:.0f} s"
                    + (" (dry-run)" if a.dry_run else ""))
            write_runlog(cfg, ctx)
        finally:
            cleanup_worktree(cfg["checkout"], worktree)

        can_coalesce = (
            not a.dry_run and not a.app and tpath and trigger_pending(tpath)
            and (not a.no_pull or from_self_exec) and pass_no + 1 < MAX_COALESCE_PASSES
        )
        if not can_coalesce:
            break
        consume_trigger(tpath)
        ctx.log("trigger poked during reconcile -- running coalesced pass")
    return 0 if ok_all else 1


def write_runlog(cfg, ctx):
    logf = os.path.join(cfg["state_dir"], "reconcile.log")
    rotate_log(logf, int(cfg_get(cfg, "log_max_bytes")), int(cfg_get(cfg, "log_keep")))
    append_log(logf, ctx.log_lines)
    ctx.log_lines = []


if __name__ == "__main__":
    sys.exit(main())
