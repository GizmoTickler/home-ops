"""Resilience upgrades: WAL recovery, coalescing trigger queue, worktree isolation, bootstrap tier,
lock policy, netns http/min_ready health probes, rollback pinning, and dashboard/host/retired sync."""
import json, os, subprocess, urllib.error

import pytest
import yaml
from prometheus_client.parser import text_string_to_metric_families

from conftest import ME, compose, make_app, rc


def _cp(rc_=0, out="", err=""):
    return subprocess.CompletedProcess([], rc_, out, err)


# ---- Gap 1: WAL & unconverged fingerprint recovery -------------------------------------------------------
def test_pending_redeploy_and_unconverged_file_trigger_recovery(tmp_path, ctx, mid, clock, monkeypatch):
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])
    dest = tmp_path / "cfg" / "app.conf"
    dest.parent.mkdir()
    dest.write_text("v2\n")
    c = compose()
    mid.add("app", c)
    mid.script["app"] = ["DEPLOYING", "RUNNING"]

    d = make_app(tmp_path / "apps", "app", c,
                 {"files": [{"template": "a.tmpl", "dest": str(dest), "owner": ME, "mode": "0600"}]},
                 {"a.tmpl": "v2\n"})
    ctx.state["pending_redeploy"] = {"app": 1234}
    assert rc.reconcile_app(d, ctx) is True
    assert [m for m, *_ in mid.mutations()] == ["app.redeploy"]
    assert "app" not in ctx.state["pending_redeploy"]

    # Crash between write_file and record_applied without WAL: applied still holds old fingerprint
    mid.calls.clear()
    ctx.state["applied"]["app"]["files"][str(dest)] = rc.fingerprint(ctx, "v1\n")
    mid.script["app"] = ["RUNNING"]
    assert rc.reconcile_app(d, ctx) is True
    assert [m for m, *_ in mid.mutations()] == ["app.redeploy"]
    assert ctx.state["applied"]["app"]["files"][str(dest)] == rc.fingerprint(ctx, "v2\n")


# ---- Gap 2: Coalescing trigger queue & symlink rejection -------------------------------------------------
def test_trigger_symlink_rejection_and_coalesce(tmp_path, mid, clock, monkeypatch):
    tdir = tmp_path / "trig"
    tdir.mkdir()
    target = tmp_path / "victim"
    target.write_text("keep")
    link = tdir / "poke"
    link.symlink_to(target)

    assert rc.trigger_pending(str(link)) is False
    with pytest.raises(RuntimeError):
        rc.consume_trigger(str(link))
    assert target.read_text() == "keep"

    # Regular file consume + --from-trigger short-circuit when already consumed
    link.unlink()
    reg = tdir / "poke"
    reg.write_text("")
    assert rc.trigger_pending(str(reg)) is True
    assert rc.consume_trigger(str(reg)) is True
    assert rc.consume_trigger(str(reg)) is False

    co = tmp_path / "co"
    apps = co / "truenas" / "apps"
    make_app(apps, "alpha", compose(), {})
    mid.add("alpha", compose())
    cfg_p = tmp_path / "cfg.json"
    cfg_p.write_text(json.dumps({
        "repo": "https://github.com/o/r.git", "checkout": str(co),
        "state_dir": str(tmp_path / "state"), "connect_url": "http://c", "connect_token": "t",
        "trigger_file": str(reg),
    }))
    seen = []
    real_run = rc.run_apps
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: _cp(1, "", "none"))
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])

    def fake_git(cfg, c, gate_on=True, partial=False):
        seen.append(len(seen))
        if len(seen) == 1:
            reg.write_text("")  # new poke arrives during pass 0
        return {"head": "a" * 40, "commit": "a" * 40, "status": "pass", "api_ok": True, "deploy": True}

    monkeypatch.setattr(rc, "git_pull", fake_git)
    # Without poke present on entry, --from-trigger skips
    assert rc.main(["--config", str(cfg_p), "--from-trigger"]) == 0
    assert seen == []

    # With poke present, runs pass 0 AND coalesces the mid-run poke into pass 1
    reg.write_text("")
    assert rc.main(["--config", str(cfg_p), "--from-trigger"]) == 0
    assert len(seen) == 2 and not reg.exists()


# ---- Gap 3: Ephemeral worktree isolation & converged_commit ----------------------------------------------
def test_dry_run_and_partial_do_not_mutate_checkout_or_gate(tmp_path, ctx, mid, clock, monkeypatch):
    def git(d, *a):
        return subprocess.run(["git", "-C", str(d), *a], check=True, capture_output=True, text=True,
                              env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                                   "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}).stdout.strip()

    origin = tmp_path / "origin"
    origin.mkdir()
    git(origin, "init", "-q", "-b", "main")
    (origin / "truenas/apps/alpha").mkdir(parents=True)
    (origin / "truenas/apps/alpha/compose.yaml").write_text(yaml.safe_dump(compose("repo/a:1@sha256:aa")))
    git(origin, "add", "-A")
    git(origin, "commit", "-qm", "c1")
    c1 = git(origin, "rev-parse", "HEAD")

    monkeypatch.setattr(rc, "make_checker", lambda *a: lambda sha: "success")
    co = tmp_path / "co"
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(co))
    info1 = rc.git_pull(ctx.cfg, ctx)
    assert info1["commit"] == c1

    (origin / "truenas/apps/alpha/compose.yaml").write_text(yaml.safe_dump(compose("repo/a:2@sha256:bb")))
    git(origin, "add", "-A")
    git(origin, "commit", "-qm", "c2")
    c2 = git(origin, "rev-parse", "HEAD")

    # Dry-run creates detached worktree for c2, leaves live checkout & ci-gate on c1
    ctx.dry = True
    info2 = rc.git_pull(ctx.cfg, ctx)
    assert info2["commit"] == c2 and os.path.isdir(info2["worktree"])
    assert "repo/a:2@sha256:bb" in open(os.path.join(info2["worktree"], "truenas/apps/alpha/compose.yaml")).read()
    assert git(co, "rev-parse", "HEAD") == c1
    assert json.load(open(os.path.join(ctx.cfg["state_dir"], "ci-gate.json")))["deployed"] == c1
    rc.cleanup_worktree(str(co), info2["worktree"])
    assert not os.path.exists(info2["worktree"])

    # converged_commit metric reflects converged vs diverged commits
    text = rc.render_metrics({"alpha": True}, 0, True, 1000, {}, {"commit": c2, "head": c2, "gate": "pass", "converged": False, "connect_ok": True})
    fams = {f.name: f for f in text_string_to_metric_families(text)}
    assert fams["truenas_apps_gitops_commit_converged"].samples[0].value == 0.0
    assert fams["truenas_apps_gitops_connect_ok"].samples[0].value == 1.0


# ---- Gap 4: Bootstrap ordering & Connect outage fail-fast ------------------------------------------------
def test_bootstrap_runs_first_and_connect_error_aborts(tmp_path, ctx, monkeypatch):
    metas = {
        "zebra": {},
        "alpha": {},
        "onepassword-connect": {"bootstrap": True, "critical": True, "order": 0},
        "envoy": {"critical": True, "order": 20},
    }
    assert rc.order_apps(list(metas), metas) == ["onepassword-connect", "alpha", "zebra", "envoy"]

    called = []

    def fake(d, c, m=None):
        n = os.path.basename(d)
        called.append(n)
        if n == "alpha":
            raise rc.ConnectUnavailableError("connection refused")
        return True

    monkeypatch.setattr(rc, "reconcile_app", fake)
    res = rc.run_apps(str(tmp_path), list(metas), metas, ctx)
    assert called == ["onepassword-connect", "alpha"]
    assert res == {"onepassword-connect": True, "alpha": False, "zebra": False, "envoy": False}
    assert ctx.connect_ok is False


# ---- Gap 5: Lock contention EX_TEMPFAIL vs skip ---------------------------------------------------------
def test_lock_if_locked_fail_and_app_default(tmp_path, mid):
    import fcntl
    co = tmp_path / "co"
    make_app(co / "truenas/apps", "alpha", compose(), {})
    sd = tmp_path / "state"
    sd.mkdir(mode=0o700)
    cfg_p = tmp_path / "cfg.json"
    cfg_p.write_text(json.dumps({
        "repo": "https://github.com/o/r.git", "checkout": str(co),
        "state_dir": str(sd), "connect_url": "http://c", "connect_token": "t",
    }))
    with open(sd / ".lock", "w") as f:
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert rc.main(["--config", str(cfg_p), "--no-pull", "--if-locked", "fail"]) == rc.EX_TEMPFAIL
        assert rc.main(["--config", str(cfg_p), "--no-pull", "--app", "alpha", "--wait-lock", "0"]) == rc.EX_TEMPFAIL
        assert rc.main(["--config", str(cfg_p), "--no-pull", "--if-locked", "skip"]) == 0


# ---- Gap 6: HTTP netns expect + min_ready + rollback image pinning ---------------------------------------
def test_healthy_http_netns_min_ready_and_image_pin(ctx, clock, monkeypatch):
    monkeypatch.setattr(rc, "container_pid", lambda a, s: "4242")
    monkeypatch.setattr(rc, "container_restarts", lambda a: {"c1": (True, 0)})
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: _cp(0, "ok"))
    meta = {"health": {"http": "http://127.0.0.1:9901/ready", "netns": "envoy", "expect": "LIVE", "min_ready": 5}}
    assert rc.healthy(meta, "envoy", ctx) is True

    # Restart count increment during min_ready soak fails health probe
    snaps = iter([{"c1": (True, 0)}, {"c1": (True, 1)}])
    monkeypatch.setattr(rc, "container_restarts", lambda a: next(snaps))
    meta_fast = {"health": {"http": "http://127.0.0.1:9901/ready", "netns": "envoy", "timeout": 4, "min_ready": 5}}
    assert rc.healthy(meta_fast, "envoy", ctx) is False

    # pin_rollback_images tags changed images locally
    tagged = []
    monkeypatch.setattr(rc, "image_present", lambda img: True)
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: tagged.append(cmd) or _cp(0))
    rc.pin_rollback_images("app", compose("repo/x:1@sha256:aa"), compose("repo/x:2@sha256:bb"), ctx)
    assert ["docker", "image", "tag", "repo/x:1@sha256:aa", "ix-rollback/app/web:prev"] in tagged


# ---- Gap 7: Dashboards, host files, and retired apps -----------------------------------------------------
def test_dashboards_host_and_retired_sync(tmp_path, ctx, mid, monkeypatch):
    # Dashboards sync + drift
    appdir = tmp_path / "co/truenas/apps/grafana"
    ddir = appdir / "dashboards"
    ddir.mkdir(parents=True)
    dash = {"id": 99, "uid": "u1", "title": "One", "version": 5}
    (ddir / "one.json").write_text(json.dumps(dash))
    meta = {"dashboards": {
        "dir": "dashboards", "service": "grafana", "folders": {"infrastructure": "f1"},
        "default_folder": "infrastructure",
    }}
    desired = {"services": {"grafana": {"environment": {"GF_SECURITY_ADMIN_USER": "admin", "GF_SECURITY_ADMIN_PASSWORD": "SUPERSECRET-PW"}}}}
    monkeypatch.setattr(rc, "container_pid", lambda a, s: "777")
    monkeypatch.setattr(rc, "_grafana_sync", lambda *a: [{"uid": "u1", "file": "one.json", "status": "updated", "live_fuid": "f1", "live_dashboard": {"uid": "u1", "title": "EditedInUI"}}])
    prev = {"dashboards": {"u1": rc.fingerprint(ctx, {"folderUid": "f1", "dashboard": {"uid": "u1", "title": "Before"}})}}
    ok, fp = rc.reconcile_dashboards(str(appdir), "grafana", meta, desired, prev, ctx)
    assert ok is True and "u1" in fp and ctx.drift["grafana"] is True
    assert "SUPERSECRET-PW" not in "\n".join(ctx.log_lines)

    # Host sync + hook execution
    hsrc = tmp_path / "co/truenas/host"
    hsrc.mkdir(parents=True)
    (hsrc / "helper.sh").write_text("#!/bin/sh\necho hi\n")
    dest_dir = tmp_path / "host_dest"
    dest_dir.mkdir()
    dest_file = dest_dir / "helper.sh"
    (hsrc / "host.yaml").write_text(yaml.safe_dump({"files": [{
        "src": "helper.sh", "dest": str(dest_file), "owner": ME, "mode": "0700", "on_change": ["/bin/true"],
    }]}))
    hooks = []
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: hooks.append(cmd) or _cp(0))
    assert rc.reconcile_host(str(tmp_path / "co"), ctx) is True
    assert dest_file.read_text() == "#!/bin/sh\necho hi\n" and hooks == [["/bin/true"]]

    # Retired app cleanup + catalog refusal (augment FakeMid unfiltered app.query with custom_app matching live TrueNAS)
    mid.add("old-app", compose())
    mid.apps["cat-app"] = {"state": "RUNNING", "config": {}, "custom_app": False}
    orig_mid = mid.__call__

    def mid_with_custom(*args, **kwargs):
        res = orig_mid(*args, **kwargs)
        if args[0] == "app.query" and len(args) == 1:
            return [{"name": item["name"], "custom_app": mid.apps[item["name"]].get("custom_app", True)} for item in res]
        return res

    monkeypatch.setattr(rc, "midclt", mid_with_custom)
    (tmp_path / "co/truenas/retired.yaml").write_text(yaml.safe_dump({"retired": ["old-app"]}))
    assert rc.reconcile_retired(str(tmp_path / "co"), ctx) is True
    assert any(m == "app.delete" and args == ("old-app",) for m, args, _ in mid.mutations())

    (tmp_path / "co/truenas/retired.yaml").write_text(yaml.safe_dump({"retired": ["cat-app"]}))
    assert rc.reconcile_retired(str(tmp_path / "co"), ctx) is False
