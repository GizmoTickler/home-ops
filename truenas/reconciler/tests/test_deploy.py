"""Deploy / wait / rollback state machine with midclt and docker mocked; critical-app ordering and abort."""
import pytest

from conftest import ME, compose, make_app, rc

OLD, NEW = compose("repo/x:1.0@sha256:aa"), compose("repo/x:2.0@sha256:bb")


@pytest.fixture(autouse=True)
def quiet(monkeypatch):
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])
    monkeypatch.setattr(rc, "container_status", lambda n: "c1 Up")


def methods(mid):
    return [m for m, *_ in mid.mutations()]


def test_update_waits_through_deploying(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    mid.script["app"] = ["DEPLOYING"] * 80 + ["RUNNING"]  # 400 s DEPLOYING (qui 2026-11-13: old code gave up at 300)
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is True
    assert methods(mid) == ["app.update"]
    assert mid.apps["app"]["config"] == NEW
    assert ("deploy", "app") in ctx.events and ("rollback", "app") not in ctx.events


def test_deploying_past_per_app_timeout_rolls_back_with_update(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    d = make_app(tmp_path / "apps", "app", NEW, {"deploy_timeout": 60})
    mid.script["app"] = ["DEPLOYING"] * 14 + ["RUNNING"]  # rollback: DEPLOYING a while, then RUNNING
    calls = {"n": 0}
    orig = mid.__call__

    def stuck(*a, **k):  # forward deploy: DEPLOYING for ever
        if a[0] == "app.update":
            calls["n"] += 1
            mid.script["app"] = ["DEPLOYING"] * 1000 if calls["n"] == 1 else ["DEPLOYING", "RUNNING"]
        return orig(*a, **k)
    rc.midclt = stuck
    t0 = clock.now
    assert rc.reconcile_app(d, ctx) is False
    assert 60 <= clock.now - t0 < 200
    upd = [p for m, _, p in mid.calls if m == "app.update"]
    assert upd == [{"custom_compose_config": NEW}, {"custom_compose_config": OLD}]  # exact previous compose
    assert "app.redeploy" not in methods(mid)
    assert ("rollback", "app") in ctx.events and ("rollback_failure", "app") not in ctx.events


def test_terminal_crash_fails_fast(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    mid.script["app"] = ["CRASHED"] * 1000
    d = make_app(tmp_path / "apps", "app", NEW, {"deploy_timeout": 900})
    t0 = clock.now
    assert rc.reconcile_app(d, ctx) is False
    # forward + rollback each give up after SETTLE (+ health/start overhead), far below the 900 s budget
    assert clock.now - t0 < 4 * rc.SETTLE + 30
    assert ("rollback_failure", "app") in ctx.events
    assert any("ROLLBACK FAILED" in ln for ln in ctx.log_lines)


def test_brief_crash_then_running_is_ok(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    mid.script["app"] = ["CRASHED", "CRASHED", "DEPLOYING", "RUNNING"]
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is True


def test_stopped_after_update_is_started(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    mid.script["app"] = ["STOPPED", "RUNNING"]
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is True
    assert methods(mid) == ["app.update", "app.start"]


def test_failed_create_does_not_redeploy(tmp_path, ctx, mid, clock):
    """Envoy 2026-11-13: app.create failed, then `app.redeploy` of the non-existent app 'rolled back'."""
    d = make_app(tmp_path / "apps", "envoy", NEW, {})
    mid.fail["app.create"] = RuntimeError("midclt app.create failed: [EFAULT] Failed 'up' action")
    assert rc.reconcile_app(d, ctx) is False
    assert methods(mid) == ["app.create"]
    assert ("rollback_failure", "envoy") not in ctx.events
    assert any("no previous version to roll back to" in ln for ln in ctx.log_lines)


def test_rollback_exception_is_reported(tmp_path, ctx, mid, clock):
    mid.add("app", OLD)
    mid.script["app"] = ["CRASHED"] * 1000
    d = make_app(tmp_path / "apps", "app", NEW, {})
    orig = mid.__call__
    n = {"u": 0}

    def second_update_fails(*a, **k):
        if a[0] == "app.update":
            n["u"] += 1
            if n["u"] == 2:
                raise RuntimeError("midclt app.update failed: boom")
        return orig(*a, **k)
    rc.midclt = second_update_fails
    assert rc.reconcile_app(d, ctx) is False
    assert ("rollback_failure", "app") in ctx.events


def test_file_only_change_rolls_back_file_and_reapplies(tmp_path, ctx, mid, clock):
    dest = tmp_path / "cfg" / "app.conf"
    dest.parent.mkdir()
    dest.write_text("old\n")
    mid.add("app", OLD)
    mid.script["app"] = ["CRASHED"] * 10 + ["RUNNING"]
    d = make_app(tmp_path / "apps", "app", OLD,
                 {"files": [{"template": "a.tmpl", "dest": str(dest), "owner": ME, "mode": "0600"}]},
                 {"a.tmpl": "new\n"})
    assert rc.reconcile_app(d, ctx) is False
    assert dest.read_text() == "old\n"
    m = methods(mid)  # forward: redeploy (+ start of the CRASHED app); rollback: app.update(previous compose)
    assert m.count("app.redeploy") == 1 and m.index("app.redeploy") < m.index("app.update")
    assert [p for x, _, p in mid.calls if x == "app.update"] == [{"custom_compose_config": OLD}]


def test_preflight_failure_touches_nothing(tmp_path, ctx, mid, clock, monkeypatch):
    monkeypatch.setattr(rc, "preflight", lambda *a: ["web: mgmt address 192.168.123.156 is in use by container x"])
    mid.add("app", OLD)
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is False
    assert mid.mutations() == []
    assert ("preflight_failure", "app") in ctx.events


def test_dry_run_touches_nothing(tmp_path, ctx, mid, clock):
    ctx.dry = True
    mid.add("app", OLD)
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is True
    assert mid.mutations() == []
    assert any("would deploy" in ln for ln in ctx.log_lines)


def test_order_critical_last():
    metas = {"b": {}, "a": {}, "envoy": {"critical": True, "order": 20}, "pocket-id": {"critical": True, "order": 10},
             "onepassword-connect": {"critical": True, "order": 0}, "zz": {"critical": True}}
    assert rc.order_apps(list(metas), metas) == ["a", "b", "onepassword-connect", "pocket-id", "envoy", "zz"]


def test_critical_failure_aborts_rest(tmp_path, ctx, monkeypatch):
    seen = []
    metas = {"a": {}, "onepassword-connect": {"critical": True, "order": 0}, "envoy": {"critical": True, "order": 20}}

    def fake(appdir, c, meta=None):
        n = appdir.rsplit("/", 1)[1]
        seen.append(n)
        return n != "onepassword-connect"
    monkeypatch.setattr(rc, "reconcile_app", fake)
    res = rc.run_apps(str(tmp_path), list(metas), metas, ctx)
    assert seen == ["a", "onepassword-connect"]
    assert res == {"a": True, "onepassword-connect": False, "envoy": False}
    assert any("aborting the run; skipped: envoy" in ln for ln in ctx.log_lines)


def test_noncritical_failure_does_not_abort(tmp_path, ctx, monkeypatch):
    metas = {"a": {}, "b": {}, "envoy": {"critical": True}}
    monkeypatch.setattr(rc, "reconcile_app", lambda d, c, m=None: not d.endswith("/a"))
    assert rc.run_apps(str(tmp_path), list(metas), metas, ctx) == {"a": False, "b": True, "envoy": True}


def test_deploy_error_with_secret_is_masked(tmp_path, ctx, mid, clock, capsys):
    ctx.secrets["op://v/i/token"] = "TOPSECRET-123"
    mid.add("app", OLD)
    mid.fail["app.update"] = RuntimeError("midclt app.update failed: env TOKEN=TOPSECRET-123 invalid")
    d = make_app(tmp_path / "apps", "app", NEW, {})
    assert rc.reconcile_app(d, ctx) is False
    out = capsys.readouterr().out + "\n".join(ctx.log_lines)
    assert "TOPSECRET-123" not in out and "<op://v/i/token>" in out
