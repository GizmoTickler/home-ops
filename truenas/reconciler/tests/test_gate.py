"""CI deploy gate: check-run verdicts, the commit decision, API outages (fail closed), and a real-git walk."""
import io, json, os, subprocess, urllib.error

import pytest

from conftest import rc


@pytest.mark.parametrize("runs,want", [
    ([], "none"),
    ([{"status": "in_progress"}], "pending"),
    ([{"status": "completed", "conclusion": "success"}], "success"),
    ([{"status": "completed", "conclusion": "failure"}], "failure"),
    ([{"status": "completed", "conclusion": "cancelled"}], "failure"),
    ([{"id": 1, "started_at": "2026-01-01", "status": "completed", "conclusion": "failure"},
      {"id": 2, "started_at": "2026-01-02", "status": "completed", "conclusion": "success"}], "success"),  # re-run
])
def test_check_verdict(runs, want):
    assert rc.check_verdict({"total_count": len(runs), "check_runs": runs}) == want


def test_repo_slug():
    assert rc.repo_slug("https://github.com/GizmoTickler/home-ops.git") == ("GizmoTickler", "home-ops")
    assert rc.repo_slug("git@github.com:o/r") == ("o", "r")
    with pytest.raises(rc.GateAPIError):
        rc.repo_slug("https://example.com/x")


def decide(verdicts, cands, current="cur", ancestors=(), same=False, head="HEAD"):
    calls = []

    def check(c):
        calls.append(c)
        v = verdicts.get(c, "none")
        if isinstance(v, Exception):
            raise v
        return v
    r = rc.gate_decide(head, current, cands, check, lambda a, b: a in ancestors, lambda a, b: same)
    return r, calls


def test_unchanged_tree_needs_no_api():
    (t, s, _), calls = decide({}, ["c1"], same=True)
    assert (t, s, calls) == ("HEAD", "unchanged", [])


def test_newest_truenas_commit_passed():
    (t, s, _), _ = decide({"c3": "success"}, ["c3", "c2", "cur"])
    assert (t, s) == ("HEAD", "pass")


def test_newest_failing_falls_back_to_newest_passing():
    (t, s, why), calls = decide({"c3": "failure", "c2": "pending", "c1": "success"}, ["c3", "c2", "c1", "cur"])
    assert (t, s) == ("c1", "held") and "c3=failure" in why and "c2=pending" in why
    assert calls == ["c3", "c2", "c1"]


def test_commit_without_check_is_skipped():
    """A multi-commit push only gets a check on its tip; untested middle commits are never deployed."""
    (t, s, _), _ = decide({"c3": "none", "c2": "success"}, ["c3", "c2", "cur"])
    assert (t, s) == ("c2", "held")


def test_never_rewinds_past_deployed_commit():
    (t, s, why), calls = decide({"c3": "failure", "old": "success"}, ["c3", "cur", "old"], ancestors={"old"})
    assert (t, s) == ("cur", "held") and calls == ["c3"]


def test_outage_fails_closed_to_current():
    (t, s, why), _ = decide({"c3": rc.GateAPIError("HTTP 403 rate limit")}, ["c3", "cur"])
    assert (t, s) == ("cur", "outage") and "rate limit" in why


def test_fresh_clone_nothing_passed():
    (t, s, _), _ = decide({"c1": "failure"}, ["c1"], current=None)
    assert t is None and s == "held"


def test_checker_caches_and_maps_errors(ctx, monkeypatch):
    ctx.cfg["repo"] = "https://github.com/o/r.git"
    hits = []

    def urlopen(req, timeout):
        hits.append(req.full_url)
        assert "Authorization" not in req.headers  # public repo: no token by default
        if "bad" in req.full_url:
            raise urllib.error.HTTPError(req.full_url, 403, "rate limited", {}, io.BytesIO(b""))
        body = {"total_count": 1, "check_runs": [{"status": "completed", "conclusion": "success"}]}
        return io.BytesIO(json.dumps(body).encode())
    monkeypatch.setattr(rc.urllib.request, "urlopen", urlopen)
    cache = {}
    check = rc.make_checker(ctx.cfg, ctx, cache)
    assert check("abc") == "success" and check("abc") == "success"
    assert len(hits) == 1 and "check_name=Lint+nas01+apps" in hits[0] and "/repos/o/r/commits/abc/" in hits[0]
    assert cache == {"abc": "success"}
    with pytest.raises(rc.GateAPIError):
        check("bad")


# ---- real git: origin repo -> reconciler checkout ----------------------------------------------------------
def git(d, *a):
    return subprocess.run(["git", "-C", str(d), *a], check=True, capture_output=True, text=True,
                          env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t",
                               "GIT_COMMITTER_EMAIL": "t@t"}).stdout.strip()


def commit(d, path, content, msg):
    p = d / path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)
    git(d, "add", "-A")
    git(d, "commit", "-qm", msg)
    return git(d, "rev-parse", "HEAD")


@pytest.fixture
def origin(tmp_path):
    o = tmp_path / "origin"
    o.mkdir()
    git(o, "init", "-q", "-b", "main")
    git(o, "config", "uploadpack.allowFilter", "true")
    return o


def test_git_pull_gate_end_to_end(tmp_path, ctx, origin, monkeypatch):
    verdicts = {}
    monkeypatch.setattr(rc, "make_checker", lambda cfg, c, cache: lambda sha: verdicts.get(sha, "none"))
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(tmp_path / "co"))
    a = commit(origin, "truenas/apps/x/compose.yaml", "v: 1\n", "a")
    verdicts[a] = "success"
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["commit"] == a and info["status"] == "pass"

    b = commit(origin, "README.md", "docs\n", "b: no truenas change")   # no check run at all -> unchanged
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["commit"] == b and info["status"] == "unchanged"

    c = commit(origin, "truenas/apps/x/compose.yaml", "v: 2\n", "c: broken")
    verdicts[c] = "failure"
    d = commit(origin, "kubernetes/x.yaml", "k\n", "d")
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["status"] == "held" and info["commit"] == b  # stays on last good, never rewinds to a
    assert (tmp_path / "co/truenas/apps/x/compose.yaml").read_text() == "v: 1\n"

    def boom(sha):
        raise rc.GateAPIError("timed out")
    monkeypatch.setattr(rc, "make_checker", lambda cfg, cx, cache: boom)
    e = commit(origin, "truenas/apps/x/compose.yaml", "v: 3\n", "e: fixed")
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["status"] == "outage" and info["commit"] == b and not info["api_ok"]

    monkeypatch.setattr(rc, "make_checker", lambda cfg, cx, cache: lambda sha: verdicts.get(sha, "none"))
    verdicts[e] = "success"
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["status"] == "pass" and info["commit"] == e
    assert (tmp_path / "co/truenas/apps/x/compose.yaml").read_text() == "v: 3\n"
    assert json.load(open(os.path.join(ctx.cfg["state_dir"], "ci-gate.json")))["deployed"] == e
    assert d  # (d is in history, not a truenas commit)


def test_git_pull_gate_disabled_deploys_tip(tmp_path, ctx, origin, monkeypatch):
    monkeypatch.setattr(rc, "make_checker", lambda *a: pytest.fail("gate must not be queried"))
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(tmp_path / "co"))
    a = commit(origin, "truenas/apps/x/compose.yaml", "v: 1\n", "a")
    info = rc.git_pull(ctx.cfg, ctx, gate_on=False)
    assert info["commit"] == a and info["status"] == "disabled"
    ctx.cfg["ci_gate"] = {"enabled": False}
    assert rc.cfg_get(ctx.cfg, "ci_gate")["enabled"] is False
    assert rc.cfg_get(ctx.cfg, "ci_gate")["check_name"] == "Lint nas01 apps"  # defaults merged


def test_fresh_clone_with_nothing_passing_deploys_nothing(tmp_path, ctx, origin, monkeypatch):
    monkeypatch.setattr(rc, "make_checker", lambda *a: lambda sha: "failure")
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(tmp_path / "co"))
    commit(origin, "truenas/apps/x/compose.yaml", "v: 1\n", "a")
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["deploy"] is False
    # the next run must not treat the (ungated) checked-out tip as "deployed"
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["deploy"] is False
