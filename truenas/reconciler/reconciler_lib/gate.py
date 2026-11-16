"""Git fetch, fail-closed CI deploy gate, ephemeral worktree isolation, and GitHub commit status reporting."""

import json, os, re, shutil, subprocess, tempfile, urllib.error, urllib.parse

from . import runtime as rt


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
        raise rt.GateAPIError(f"cannot derive owner/repo from {url}")
    return m.group(1), m.group(2)


def make_checker(cfg, ctx, cache):
    gate = rt.cfg_get(cfg, "ci_gate")
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
            with rt.urllib.request.urlopen(rt.urllib.request.Request(url, headers=h), timeout=int(gate["timeout"])) as r:
                v = check_verdict(json.load(r))
        except (urllib.error.URLError, OSError, ValueError) as e:
            raise rt.GateAPIError(f"GitHub check-runs API: {e}") from e
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
    except rt.GateAPIError as e:
        return current or None, "outage", f"{e} -- staying on the deployed commit (fail closed)"
    return current or None, "held", ("no passing truenas commit newer than the deployed one; "
                                     + (", ".join(skipped) or "none checked"))


def git_pull(cfg, ctx, gate_on=True, partial=False):
    co, repo, br = cfg["checkout"], cfg["repo"], cfg.get("branch", "main")
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")

    def run(*a, check=True):
        return subprocess.run(["git", "-C", co, *a], check=check, capture_output=True, text=True, env=env,
                              timeout=120)
    gate = rt.cfg_get(cfg, "ci_gate")
    fresh = not os.path.isdir(os.path.join(co, ".git"))
    if fresh:
        subprocess.run(["git", "clone", "--depth", "1", "--filter=blob:none", "--sparse", "-b", br, repo, co],
                       check=True, capture_output=True, text=True, env=env, timeout=300)
        run("sparse-checkout", "set", "truenas")
    cache_p = os.path.join(cfg["state_dir"], "ci-gate.json")
    gstate = rt.load_json(cache_p, {})
    current = None
    if not fresh:
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
            check = rt.make_checker(cfg, ctx, cache)
        except Exception as e:  # noqa: BLE001
            def check(_sha, e=e):
                raise rt.GateAPIError(str(e))
        target, status, reason = gate_decide(
            head, current, cands, check,
            lambda a, b: run("merge-base", "--is-ancestor", a, b, check=False).returncode == 0,
            lambda a, b: tree(a) != "" and tree(a) == tree(b))
        info.update(status=status, reason=reason, api_ok=status != "outage")
        if target is None:
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
            rt.atomic_json(cache_p, gstate)
    else:
        target = head
        ctx.log("ci-gate: DISABLED -- deploying the branch tip unchecked")
        if not ctx.dry and not partial:
            gstate["deployed"] = head
            rt.atomic_json(cache_p, gstate)
    cur_head = run("rev-parse", "HEAD").stdout.strip()
    if ctx.dry or partial:
        if target is not None and target != cur_head:
            wt = tempfile.mkdtemp(dir=rt.preflight_tmp(ctx), prefix="wt-")
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
        if target is not None:
            run("reset", "--hard", target)
        info["commit"] = run("rev-parse", "HEAD").stdout.strip()
    ctx.log(f"git {br}@{head[:7]} {'checking' if ctx.dry else 'deploying'} {info['commit'][:7]}")
    return info


def _post_commit_status(api_url, owner, repo, sha, token, context, state, description, timeout=15):
    url = f"{api_url.rstrip('/')}/repos/{owner}/{repo}/statuses/{sha}"
    body = json.dumps({"state": state, "context": context, "description": description[:140]}).encode()
    req = rt.urllib.request.Request(
        url, data=body, method="POST",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json",
                 "Content-Type": "application/json", "User-Agent": "truenas-apps-reconciler",
                 "X-GitHub-Api-Version": "2022-11-28"})
    with rt.urllib.request.urlopen(req, timeout=int(timeout)):
        pass


def post_github_status(cfg, ctx, results, ok_all, run_info):
    """Post commit status to GitHub for the deployed (and held, if gated) SHA(s); deduplicated via state.json."""
    gs = rt.cfg_get(cfg, "github_status")
    token_ref = gs.get("token_ref") or rt.cfg_get(cfg, "ci_gate").get("token_ref")
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


def cleanup_worktree(checkout, wt):
    if not wt:
        return
    try:
        subprocess.run(["git", "-C", checkout, "worktree", "remove", "--force", wt],
                       capture_output=True, text=True, timeout=60)
    except Exception:  # noqa: BLE001
        pass
    shutil.rmtree(wt, ignore_errors=True)
