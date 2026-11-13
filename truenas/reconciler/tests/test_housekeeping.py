"""Backup retention, state-dir permissions, log rotation, metrics, drift detection."""
import os

import pytest
from prometheus_client.parser import text_string_to_metric_families

from conftest import compose, make_app, rc

DAY = 86400
NOW = 1_800_000_000


def mk(path, age_days, size=10):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"x" * size)
    t = NOW - age_days * DAY
    os.utime(path, (t, t))
    return path


# ---- backups --------------------------------------------------------------------------------------------
def test_prune_keeps_recent_and_newest_n_per_file(tmp_path):
    b = tmp_path / "backup"
    f = "mnt__a__envoy.yaml"
    files = [mk(b / "envoy" / f"{f}.2026010{i}T000000Z.prev", age) for i, age in enumerate([1, 20, 30, 40, 50, 60])]
    legacy = mk(b / "envoy" / f"{f}.prev", 70)
    other = mk(b / "envoy" / "mnt__a__acme.sh.prev", 90)            # the only version of another file: kept
    removed, kept = rc.prune_backups(str(b), ["envoy"], 14, 3, NOW, dry=False)
    assert sorted(removed) == sorted(str(p) for p in files[3:] + [legacy])
    assert all(p.exists() for p in files[:3]) and other.exists()
    assert kept > 0


def test_prune_loose_files_grouped_by_app(tmp_path):
    b = tmp_path / "backup"
    keep_new = mk(b / "autobrr-db-20261113-crossseed.sqlite", 30)  # newest of its app: always kept
    old = mk(b / "autobrr-db-20261001.sqlite", 40)
    qui = mk(b / "qui-db-20261113.sqlite", 30)
    unk = mk(b / "random.bin", 3)
    removed, _ = rc.prune_backups(str(b), ["autobrr", "qui"], 14, 1, NOW, dry=False)
    assert removed == [str(old)]
    assert keep_new.exists() and qui.exists() and unk.exists()


def test_prune_dry_run_and_symlinks(tmp_path):
    b = tmp_path / "backup"
    victim = mk(tmp_path / "elsewhere" / "important", 100)
    (b / "app").mkdir(parents=True)
    os.symlink(victim, b / "app" / "x.prev")
    os.symlink(tmp_path / "elsewhere", b / "linkdir")
    old = [mk(b / "app" / f"y.2026010{i}T000000Z.prev", 100 + i) for i in range(3)]
    removed, _ = rc.prune_backups(str(b), ["app"], 14, 1, NOW, dry=True)
    assert len(removed) == 2 and all(p.exists() for p in old)  # dry-run deletes nothing
    removed, _ = rc.prune_backups(str(b), ["app"], 14, 1, NOW, dry=False)
    assert victim.exists() and os.path.islink(b / "app" / "x.prev")


def test_backup_names_are_timestamped(tmp_path, clock):
    dest = tmp_path / "d" / "f.conf"
    dest.parent.mkdir()
    dest.write_text("v1")
    rc.write_file(str(dest), "v2", f"{os.getuid()}:{os.getgid()}", 0o600, str(tmp_path / "b"))
    clock.now += 5
    rc.write_file(str(dest), "v3", f"{os.getuid()}:{os.getgid()}", 0o600, str(tmp_path / "b"))
    assert sorted(open(tmp_path / "b" / n).read() for n in os.listdir(tmp_path / "b")) == ["v1", "v2"]


# ---- permissions / log ----------------------------------------------------------------------------------
def test_secure_state(ctx, tmp_path):
    sd = ctx.cfg["state_dir"]
    os.chmod(sd, 0o755)
    os.makedirs(os.path.join(sd, "backup", "envoy"))
    os.chmod(os.path.join(sd, "backup"), 0o755)
    stray = os.path.join(sd, "qBittorrent.conf.vpn0-20261111")
    open(stray, "w").write("x")
    os.chmod(stray, 0o644)
    ctx.dry = True
    rc.secure_state(ctx.cfg, ctx)
    assert os.stat(sd).st_mode & 0o777 == 0o755  # dry-run: reported only
    ctx.dry = False
    rc.secure_state(ctx.cfg, ctx)
    for d in (sd, os.path.join(sd, "backup"), os.path.join(sd, "backup", "envoy")):
        assert os.stat(d).st_mode & 0o777 == 0o700
    assert os.path.exists(stray) and os.stat(stray).st_mode & 0o777 == 0o644  # never touched
    assert any("WARNING" in ln and "qBittorrent.conf.vpn0" in ln for ln in ctx.log_lines)


def test_rotate_log_keeps_n(tmp_path):
    lf = str(tmp_path / "reconcile.log")
    for i in range(8):
        rc.append_log(lf, ["x" * 100 + str(i)])
        rc.rotate_log(lf, 50, 3)
    names = sorted(os.listdir(tmp_path))
    assert names == ["reconcile.log.1", "reconcile.log.2", "reconcile.log.3"]
    assert open(lf + ".1").read().strip().endswith("7")
    rc.append_log(lf, ["new"])
    assert os.stat(lf).st_mode & 0o777 == 0o600


# ---- metrics --------------------------------------------------------------------------------------------
def test_metric_state_and_render_parse():
    ms = {}
    ev = [("deploy", "envoy"), ("deploy_failure", "envoy"), ("rollback", "envoy"), ("rollback_failure", "envoy"),
          ("preflight_failure", "qui"), ("drift", "grafana")]
    rc.update_metric_state(ms, ev, {"envoy": False, "qui": False, "grafana": True}, False, True, NOW)
    rc.update_metric_state(ms, [("deploy", "envoy")], {"envoy": False}, False, True, NOW + 600)
    assert ms["counters"]["deploy"]["envoy"] == 2 and ms["consecutive_failed_runs"] == 2
    assert ms["rollback_failed"] == {"envoy": 1}
    info = {"duration": 12.3, "commit": "a" * 40, "head": "b" * 40, "gate": "held", "api_ok": True,
            "drift": {"grafana": True, "envoy": False}, "backup_bytes": 1234}
    text = rc.render_metrics({"envoy": False, "qui": False, "grafana": True}, 1, False, NOW, ms, info, "1799990000")
    fams = {f.name: f for f in text_string_to_metric_families(text)}
    s = lambda n: {tuple(sorted(x.labels.items())): x.value for x in fams[n].samples}
    assert s("truenas_apps_gitops_deploys")[(("app", "envoy"),)] == 2
    assert fams["truenas_apps_gitops_deploys"].type == "counter"
    assert s("truenas_apps_gitops_app_rollback_failed")[(("app", "envoy"),)] == 1
    assert s("truenas_apps_gitops_app_rollback_failed")[(("app", "qui"),)] == 0
    assert s("truenas_apps_gitops_consecutive_failed_runs")[()] == 2
    assert s("truenas_apps_gitops_ci_gate_held")[()] == 1
    assert s("truenas_apps_gitops_last_success_timestamp_seconds")[()] == 1799990000
    assert s("truenas_apps_gitops_app_drift")[(("app", "grafana"),)] == 1
    ci = list(s("truenas_apps_gitops_commit_info"))[0]
    assert dict(ci) == {"commit": "a" * 12, "head": "b" * 12, "gate": "held"}
    assert "truenas_apps_gitops_last_rollback_failure_timestamp_seconds" in fams
    # converging clears the sticky rollback flag; a fully OK run resets the failure streak
    rc.update_metric_state(ms, [], {"envoy": True}, True, True, NOW + 1200)
    assert ms["rollback_failed"] == {} and ms["consecutive_failed_runs"] == 0


def test_partial_run_does_not_touch_streak():
    ms = {"consecutive_failed_runs": 4}
    rc.update_metric_state(ms, [], {"x": True}, True, False, NOW)
    assert ms["consecutive_failed_runs"] == 4


def test_write_metrics_keeps_previous_success(tmp_path):
    d = tmp_path / "tf"
    d.mkdir()
    cfg = {"metrics_dir": str(d)}
    rc.write_metrics(cfg, {"a": True}, 0, True, {}, {}, now=NOW)
    rc.write_metrics(cfg, {"a": False}, 0, False, {}, {}, now=NOW + 600)
    text = (d / "apps_gitops.prom").read_text()
    assert f"truenas_apps_gitops_last_success_timestamp_seconds {NOW}" in text
    list(text_string_to_metric_families(text))  # parses
    assert os.stat(d / "apps_gitops.prom").st_mode & 0o777 == 0o644


# ---- drift ----------------------------------------------------------------------------------------------
def test_drift_report():
    base = {"compose": "c1", "files": {"/f": "f1"}, "ini": {"/i": "i1"}}
    assert rc.drift_report(None, {"compose": "x", "files": {}, "ini": {}}) == []
    assert rc.drift_report(base, {"compose": "c1", "files": {"/f": "f1"}, "ini": {"/i": "i1"}}) == []
    assert rc.drift_report(base, {"compose": "c2", "files": {"/f": "f2"}, "ini": {"/i": "i2"}}) == [
        "compose", "file /f", "ini /i"]


@pytest.fixture
def plain(monkeypatch):
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])


def test_ui_edit_is_reported_as_drift_git_change_is_not(tmp_path, ctx, mid, clock, plain):
    c1 = compose()
    mid.add("app", c1)
    d = make_app(tmp_path / "apps", "app", c1, {})
    assert rc.reconcile_app(d, ctx) is True          # in sync: baseline recorded
    assert ctx.drift["app"] is False
    edited = compose(environment_edit=True)
    mid.apps["app"]["config"] = edited                # someone edits the app in the TrueNAS UI
    assert rc.reconcile_app(d, ctx) is True           # re-applied from git (existing behaviour)
    assert ctx.drift["app"] is True and ("drift", "app") in ctx.events
    assert any("DRIFT -- live compose changed outside git" in ln for ln in ctx.log_lines)
    assert mid.apps["app"]["config"] == c1
    # a change in git is not drift
    ctx.events.clear()
    d2 = make_app(tmp_path / "apps2", "app", compose("repo/x:9@sha256:cc"), {})
    assert rc.reconcile_app(d2, ctx) is True
    assert ctx.drift["app"] is False and ("drift", "app") not in ctx.events


def test_fingerprints_are_keyed_not_plain_hashes(ctx):
    import hashlib
    fp = rc.fingerprint(ctx, "secret-value")
    assert fp != hashlib.sha256(b"secret-value").hexdigest()[:32]
    assert os.stat(os.path.join(ctx.cfg["state_dir"], ".fingerprint-key")).st_mode & 0o777 == 0o600
