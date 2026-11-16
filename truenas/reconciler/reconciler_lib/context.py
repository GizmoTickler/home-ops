"""Execution context and 1Password Connect secret rendering."""

import json, time, urllib.error, urllib.parse

from . import runtime as rt


class ConnectUnavailableError(RuntimeError):
    """Raised when the local 1Password Connect API is unreachable or returning 5xx."""


def cfg_get(cfg, key):
    """Return merged configuration value for `key` with CONFIG_DEFAULTS applied."""
    d = rt.CONFIG_DEFAULTS.get(key)
    v = cfg.get(key, d)
    if isinstance(d, dict):
        v = {**d, **(v or {})}
    return v


class Ctx:
    """Per-run reconciliation context holding config, resolved secrets, events, and persisted state."""

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

    def _connect(self, path):
        req = rt.urllib.request.Request(
            self.cfg["connect_url"].rstrip("/") + path,
            headers={"Authorization": f"Bearer {self.cfg['connect_token']}"},
        )
        try:
            with rt.urllib.request.urlopen(req, timeout=20) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code >= 500:
                self.connect_ok = False
                raise rt.ConnectUnavailableError(f"HTTP {e.code} from 1Password Connect") from e
            raise
        except (urllib.error.URLError, OSError) as e:
            self.connect_ok = False
            raise rt.ConnectUnavailableError(f"1Password Connect unreachable: {e}") from e

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
            if rt.ITEM_ID.fullmatch(item):  # item id: for titles a ref can't carry (spaces, parentheses)
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
        return rt.REF.sub(lambda m: self.resolve(m.group(1)), s)

    def render_obj(self, o):
        if isinstance(o, dict):
            return {k: self.render_obj(v) for k, v in o.items()}
        if isinstance(o, list):
            return [self.render_obj(v) for v in o]
        if isinstance(o, str):
            return self.render_str(o)
        return o
