"""Shared fixtures: the reconciler imported as a module, a fake clock, a fake TrueNAS middleware."""
import copy, json, os, sys, types

import pytest
import yaml

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import reconcile as rc  # noqa: E402

ME = f"{os.getuid()}:{os.getgid()}"


@pytest.fixture
def clock(monkeypatch):
    t = types.SimpleNamespace(now=1_000_000.0)
    monkeypatch.setattr(rc, "_now", lambda: t.now)
    monkeypatch.setattr(rc, "_sleep", lambda s: setattr(t, "now", t.now + s))
    return t


@pytest.fixture
def ctx(tmp_path):
    sd = tmp_path / "state"
    sd.mkdir(mode=0o700)
    c = rc.Ctx({"state_dir": str(sd), "connect_url": "http://connect", "connect_token": "x",
                "repo": "https://github.com/o/r.git", "checkout": str(tmp_path / "co")}, dry=False)
    return c


class FakeMid:
    """Fake `midclt`: apps {name: {"state", "config"}}; `script[name]` = list of states returned by successive
    app.query calls after a deploy (last one sticky); `fail` = {method: exception}."""

    MUTATING = {"app.update", "app.create", "app.redeploy", "app.stop", "app.start", "app.delete",
                "app.metadata.generate"}

    def __init__(self):
        self.apps, self.calls, self.script, self.fail = {}, [], {}, {}
        self.armed = False  # `script` drives app.query only after the first mutating call

    def add(self, name, config, state="RUNNING"):
        self.apps[name] = {"state": state, "config": copy.deepcopy(config), "custom_app": True}

    def mutations(self):
        return [c for c in self.calls if c[0] in self.MUTATING]

    def __call__(self, *args, payload=None, job=False, timeout=900):
        m = args[0]
        self.calls.append((m, args[1:], copy.deepcopy(payload)))
        if m in self.MUTATING:
            self.armed = True
        if m in self.fail:
            raise self.fail[m]
        if m == "app.query":
            if len(args) > 1:
                name = json.loads(args[1])[0][2]
                a = self.apps.get(name)
                if not a:
                    return []
                seq = self.script.get(name) if self.armed else None
                if seq:
                    a["state"] = seq.pop(0) if len(seq) > 1 else seq[0]
                return [{"name": name, "state": a["state"], "custom_app": True}] if a["state"] != "ABSENT" else []
            return [{"name": n} for n in self.apps]
        if m == "app.config":
            return copy.deepcopy(self.apps[args[1]]["config"])
        if m == "app.update":
            self.apps[args[1]]["config"] = copy.deepcopy(payload["custom_compose_config"])
            return {}
        if m == "app.create":
            self.apps[payload["app_name"]] = {"state": "DEPLOYING", "config": payload["custom_compose_config"]}
            return {}
        return {}


@pytest.fixture
def mid(monkeypatch):
    f = FakeMid()
    monkeypatch.setattr(rc, "midclt", f)
    return f


def make_app(root, name, compose, meta=None, files=None):
    d = root / name
    d.mkdir(parents=True)
    (d / "compose.yaml").write_text(yaml.safe_dump(compose))
    if meta is not None:
        (d / "app.yaml").write_text(yaml.safe_dump(meta))
    for fname, content in (files or {}).items():
        (d / fname).write_text(content)
    return str(d)


def compose(image="repo/x:1.0@sha256:aa", **extra):
    c = {"services": {"web": {"image": image, "restart": "unless-stopped"}}}
    c.update(extra)
    return c
