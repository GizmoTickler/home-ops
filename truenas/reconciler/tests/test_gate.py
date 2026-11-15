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


def test_lost_last_good_commit_does_not_move_checkout(tmp_path, ctx, origin, monkeypatch):
    verdicts = {}
    monkeypatch.setattr(rc, "make_checker", lambda cfg, c, cache: lambda sha: verdicts.get(sha, "none"))
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(tmp_path / "co"))
    a = commit(origin, "truenas/apps/x/compose.yaml", "v: 1\n", "a")
    verdicts[a] = "success"
    assert rc.git_pull(ctx.cfg, ctx)["commit"] == a
    p = os.path.join(ctx.cfg["state_dir"], "ci-gate.json")
    st = json.load(open(p))
    st["deployed"] = "f" * 40  # e.g. history rewritten / gc'd
    json.dump(st, open(p, "w"))
    commit(origin, "truenas/apps/x/compose.yaml", "v: 2\n", "b: unchecked")
    info = rc.git_pull(ctx.cfg, ctx)  # newest passing commit is still found and deployed
    assert info.get("deploy", True) and info["commit"] == a and info["status"] == "held"

    def boom(sha):
        raise rc.GateAPIError("down")
    monkeypatch.setattr(rc, "make_checker", lambda cfg, c, cache: boom)
    st = json.load(open(p))
    st["deployed"] = "f" * 40
    json.dump(st, open(p, "w"))
    info = rc.git_pull(ctx.cfg, ctx)  # API down AND last good unknown: deploy nothing, never the ungated tip
    assert info["deploy"] is False and info["commit"] == a


def test_shallow_fetch_deepens_to_reach_deployed(tmp_path, ctx, origin, monkeypatch):
    verdicts = {}
    monkeypatch.setattr(rc, "make_checker", lambda cfg, c, cache: lambda sha: verdicts.get(sha, "none"))
    ctx.cfg.update(repo=f"file://{origin}", checkout=str(tmp_path / "co"))
    a = commit(origin, "truenas/apps/x/compose.yaml", "v: 1\n", "a")
    verdicts[a] = "success"
    assert rc.git_pull(ctx.cfg, ctx)["commit"] == a
    for i in range(55):
        head = commit(origin, "kubernetes/noise.txt", f"{i}\n", f"noise {i}")
    info = rc.git_pull(ctx.cfg, ctx)
    assert info["status"] == "unchanged" and info["commit"] == head and info.get("deploy", True)


def test_post_github_status_success_dedup_and_failure(ctx, monkeypatch):
    ctx.cfg["ci_gate"] = {"token_ref": "op://Vault/Item/TOK"}
    monkeypatch.setattr(ctx, "resolve", lambda ref: ctx.secrets.setdefault(ref, "secret-tok-xyz"))
    posts = []

    def fake_urlopen(req, timeout):
        posts.append((req.full_url, dict(req.header_items()), json.loads(req.data.decode())))
        return io.BytesIO(b"{}")

    monkeypatch.setattr(rc.urllib.request, "urlopen", fake_urlopen)
    sha = "a" * 40
    run_info = {"commit": sha, "head": sha, "gate": "pass", "deploy": True}

    rc.post_github_status(ctx.cfg, ctx, {"app1": True, "_skip": True}, True, run_info)
    assert len(posts) == 1
    url, headers, body = posts[0]
    assert url == f"https://api.github.com/repos/o/r/statuses/{sha}"
    assert headers["Authorization"] == "Bearer secret-tok-xyz"
    assert body == {"state": "success", "context": "nas01/apps-gitops",
                    "description": "Deployed on nas01 (1/1 apps converged)"}
    assert ctx.state["github_status_last_deployed"] == sha

    rc.post_github_status(ctx.cfg, ctx, {"app1": True, "_skip": True}, True, run_info)
    assert len(posts) == 1

    rc.post_github_status(ctx.cfg, ctx, {"app1": True, "app2": False}, False, run_info)
    assert len(posts) == 2
    assert posts[1][2]["state"] == "failure"
    assert "app2" in posts[1][2]["description"]


def test_post_github_status_batch_and_held_pending(tmp_path, ctx, origin, monkeypatch):
    c1 = commit(origin, "a.txt", "1\n", "c1")
    c2 = commit(origin, "a.txt", "2\n", "c2")
    c3 = commit(origin, "a.txt", "3\n", "c3")
    co = tmp_path / "co"
    subprocess.run(["git", "clone", "-q", f"file://{origin}", str(co)], check=True)

    ctx.cfg.update(repo="https://github.com/o/r.git", checkout=str(co),
                   ci_gate={"token_ref": "op://Vault/Item/TOK"})
    monkeypatch.setattr(ctx, "resolve", lambda ref: ctx.secrets.setdefault(ref, "secret-tok-xyz"))
    ctx.state["github_status_last_deployed"] = c1
    ctx.state["github_status"] = {c2: {"state": "pending", "description": f"Held by CI gate on nas01; deployed {c1[:7]}"}}

    posts = []

    def fake_urlopen(req, timeout):
        sha = req.full_url.rsplit("/", 1)[-1]
        posts.append((sha, json.loads(req.data.decode())))
        return io.BytesIO(b"{}")

    monkeypatch.setattr(rc.urllib.request, "urlopen", fake_urlopen)
    head = "d" * 40
    rc.post_github_status(ctx.cfg, ctx, {"app1": True}, True, {"commit": c3, "head": head, "gate": "held", "deploy": True})

    by_sha = {sha: body for sha, body in posts}
    assert set(by_sha) == {c2, c3, head}
    assert by_sha[c2]["state"] == "success" and f"via {c3[:7]}" in by_sha[c2]["description"]
    assert by_sha[c3]["state"] == "success" and "1/1 apps converged" in by_sha[c3]["description"]
    assert by_sha[head]["state"] == "pending" and by_sha[head]["description"] == f"Held by CI gate on nas01; deployed {c3[:7]}"
    assert ctx.state["github_status"][c2]["state"] == "success"
    assert ctx.state["github_status_last_deployed"] == c3


def test_post_github_status_api_error_warns_masked_and_does_not_advance(ctx, monkeypatch):
    ctx.cfg["ci_gate"] = {"token_ref": "op://Vault/Item/TOK"}

    def fake_connect(path):
        if "/v1/vaults?" in path:
            return [{"id": "v1"}]
        if "/items?" in path:
            return [{"id": "i1"}]
        return {"fields": [{"label": "TOK", "value": "ghp_super_secret_token"}]}

    monkeypatch.setattr(ctx, "_connect", fake_connect)
    monkeypatch.setattr(rc.urllib.request, "urlopen",
                        lambda req, timeout: (_ for _ in ()).throw(urllib.error.URLError("ghp_super_secret_token")))
    prev = "0" * 40
    sha = "e" * 40
    ctx.state["github_status_last_deployed"] = prev

    rc.post_github_status(ctx.cfg, ctx, {"app1": True}, True, {"commit": sha, "head": sha, "gate": "pass", "deploy": True})
    assert ctx.state["github_status_last_deployed"] == prev
    joined = "\n".join(ctx.log_lines)
    assert "github-status: WARNING" in joined
    assert "<op://Vault/Item/TOK>" in joined
    assert "ghp_super_secret_token" not in joined
