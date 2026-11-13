"""main(): one full run with midclt/docker mocked -- ordering, metrics file, state, log, dry-run side effects."""
import json, os, subprocess

import pytest
from prometheus_client.parser import text_string_to_metric_families

from conftest import compose, make_app, rc


@pytest.fixture
def setup(tmp_path, mid, clock, monkeypatch):
    co = tmp_path / "co"
    apps = co / "truenas" / "apps"
    for n in ("alpha", "envoy", "onepassword-connect"):
        c = compose(f"repo/{n}:1@sha256:aa")
        mid.add(n, c)
        meta = {"critical": True, "order": 0 if n.startswith("one") else 20} if n != "alpha" else {}
        make_app(apps, n, c, meta)
    mid.add("stray", {})
    tf = tmp_path / "textfile"
    tf.mkdir()
    cfg = {"repo": "https://github.com/o/r.git", "checkout": str(co), "state_dir": str(tmp_path / "state"),
           "metrics_dir": str(tf), "connect_url": "http://c", "connect_token": "t"}
    p = tmp_path / "config.json"
    p.write_text(json.dumps(cfg))
    order = []
    real = rc.reconcile_app
    monkeypatch.setattr(rc, "reconcile_app", lambda d, c, m=None: order.append(os.path.basename(d)) or real(d, c, m))
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: subprocess.CompletedProcess(cmd, 1, "", "no such network"))
    return {"cfg": cfg, "config": str(p), "tf": tf, "order": order}


def test_full_run_writes_metrics_state_and_log(setup):
    assert rc.main(["--config", setup["config"], "--no-pull"]) == 0
    assert setup["order"] == ["alpha", "onepassword-connect", "envoy"]
    text = (setup["tf"] / "apps_gitops.prom").read_text()
    names = {f.name for f in text_string_to_metric_families(text)}
    assert {"truenas_apps_gitops_app_ok", "truenas_apps_gitops_run_duration_seconds",
            "truenas_apps_gitops_commit_info", "truenas_apps_gitops_consecutive_failed_runs",
            "truenas_apps_gitops_unmanaged_apps"} <= names
    sd = setup["cfg"]["state_dir"]
    st = json.load(open(os.path.join(sd, "state.json")))
    assert set(st["applied"]) == {"alpha", "envoy", "onepassword-connect"}
    assert st["metrics"]["consecutive_failed_runs"] == 0
    log = open(os.path.join(sd, "reconcile.log")).read()
    assert "stray: on nas01 but NOT in git" in log and "run: OK (3/3 converged)" in log
    for f in ("reconcile.log", "state.json", ".lock", ".fingerprint-key"):
        assert os.stat(os.path.join(sd, f)).st_mode & 0o777 == 0o600


def test_dry_run_writes_no_metrics_or_state(setup, mid):
    assert rc.main(["--config", setup["config"], "--no-pull", "--dry-run"]) == 0
    assert not (setup["tf"] / "apps_gitops.prom").exists()
    assert not os.path.exists(os.path.join(setup["cfg"]["state_dir"], "state.json"))
    assert mid.mutations() == []


def test_lock_held_exits_quietly(setup):
    import fcntl
    sd = setup["cfg"]["state_dir"]
    os.makedirs(sd, mode=0o700, exist_ok=True)
    with open(os.path.join(sd, ".lock"), "w") as f:
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert rc.main(["--config", setup["config"], "--no-pull"]) == 0
    assert setup["order"] == []
