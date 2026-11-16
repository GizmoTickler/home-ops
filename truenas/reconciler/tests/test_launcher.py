"""Tests for modular reconcile facade, tree digest, and N-1 fallback supervisor (launcher.py)."""

import importlib.util, io, json, os, subprocess

from prometheus_client.parser import text_string_to_metric_families

from conftest import rc

LAUNCHER_PATH = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "host", "launcher.py"))


def _load_launcher():
    spec = importlib.util.spec_from_file_location("truenas_launcher", LAUNCHER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _git(cwd, *args):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
    return subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, text=True, env=env).stdout.strip()


def _parse_prom(path):
    fams = {f.name: f for f in text_string_to_metric_families(open(path).read())}
    return fams


def test_facade_and_reconciler_tree_digest(tmp_path):
    for attr in ("main", "reconcile_app", "git_pull", "preflight", "reconcile_dashboards",
                 "reconcile_host", "reconciler_tree_digest", "_write_supervised_nonce"):
        assert hasattr(rc, attr)

    rdir = tmp_path / "rec"
    (rdir / "reconciler_lib").mkdir(parents=True)
    (rdir / "reconcile.py").write_text("print('entry')\n")
    (rdir / "reconciler_lib" / "a.py").write_text("X = 1\n")
    d1 = rc.reconciler_tree_digest(str(rdir))
    (rdir / "reconciler_lib" / "a.py").write_text("X = 2\n")
    d2 = rc.reconciler_tree_digest(str(rdir))
    assert d1 != d2


def _make_git_setup(tmp_path):
    origin = tmp_path / "origin"
    origin.mkdir()
    _git(origin, "init", "-q", "-b", "main")
    rec_dir = origin / "truenas" / "reconciler"
    (rec_dir / "reconciler_lib").mkdir(parents=True)
    (rec_dir / "reconciler_lib" / "helper.py").write_text(
        "import os\n"
        "def finish(rc_code):\n"
        "    nf, nn = os.environ.get('RECONCILE_NONCE_FILE'), os.environ.get('RECONCILE_RUN_NONCE')\n"
        "    if nf and nn:\n"
        "        open(nf, 'w').write(nn)\n"
        "    return rc_code\n"
    )
    marker = tmp_path / "run_log.json"
    (rec_dir / "reconcile.py").write_text(
        "import json, os, sys\n"
        "sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))\n"
        "from reconciler_lib.helper import finish\n"
        f"open({str(marker)!r}, 'a').write(json.dumps({{'path': __file__, 'argv': sys.argv[1:]}}) + '\\n')\n"
        "sys.exit(finish(0))\n"
    )
    _git(origin, "add", "-A")
    _git(origin, "commit", "-qm", "good")
    sha1 = _git(origin, "rev-parse", "HEAD")

    co = tmp_path / "co"
    subprocess.run(["git", "clone", "-q", f"file://{origin}", str(co)], check=True)
    sd = tmp_path / "state"
    sd.mkdir(mode=0o700)
    md = tmp_path / "metrics"
    md.mkdir()
    cfg_p = tmp_path / "cfg.json"
    cfg_p.write_text(json.dumps({
        "repo": f"file://{origin}",
        "checkout": str(co),
        "branch": "main",
        "state_dir": str(sd),
        "metrics_dir": str(md),
        "connect_url": "http://127.0.0.1:9",
        "connect_token": "tok",
    }))
    return origin, co, sd, md, cfg_p, marker, sha1


def test_launcher_supervised_clean_controlled_failure_and_fallback(tmp_path):
    launcher = _load_launcher()
    assert launcher.main(["--self-check"]) == 0
    origin, co, sd, md, cfg_p, marker, sha1 = _make_git_setup(tmp_path)

    # 1. Clean rc=0 run promotes last-good-reconciler and sets fallback_active=0
    assert launcher.main(["--config", str(cfg_p)]) == 0
    lkg = sd / "last-good-reconciler"
    assert (lkg / "reconcile.py").is_file() and (lkg / "reconciler_lib" / "helper.py").is_file()
    assert json.loads((lkg / "meta.json").read_text())["commit"] == sha1
    fams = _parse_prom(md / "apps_gitops_launcher.prom")
    assert fams["truenas_apps_gitops_launcher_fallback_active"].samples[0].value == 0.0
    assert fams["truenas_apps_gitops_launcher_last_good_info"].samples[0].labels["commit"] == sha1[:12]

    # 2. Controlled rc=1 (nonce written): preserves old LKG, no fallback
    rec = origin / "truenas" / "reconciler" / "reconcile.py"
    rec.write_text(
        "import os, sys\n"
        "sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))\n"
        "from reconciler_lib.helper import finish\n"
        "sys.exit(finish(1))\n"
    )
    _git(origin, "add", "-A")
    _git(origin, "commit", "-qm", "controlled-fail")
    _git(co, "pull", "-q", "--ff-only")
    assert launcher.main(["--config", str(cfg_p)]) == 1
    assert json.loads((lkg / "meta.json").read_text())["commit"] == sha1
    assert _parse_prom(md / "apps_gitops_launcher.prom")["truenas_apps_gitops_launcher_fallback_active"].samples[0].value == 0.0

    # 3. Uncaught SyntaxError crash in checkout with no fix on origin: executes LKG with --no-pull, sets fallback_active=1
    marker.unlink()
    rec.write_text("def broken(:\n")
    _git(origin, "add", "-A")
    _git(origin, "commit", "-qm", "broken-syntax")
    _git(co, "pull", "-q", "--ff-only")
    assert launcher.main(["--config", str(cfg_p), "--from-trigger"]) == 1
    assert _parse_prom(md / "apps_gitops_launcher.prom")["truenas_apps_gitops_launcher_fallback_active"].samples[0].value == 1.0
    runs = [json.loads(line) for line in marker.read_text().splitlines()]
    assert len(runs) == 1 and "last-good-reconciler" in runs[0]["path"]
    assert "--no-pull" in runs[0]["argv"] and "--from-trigger" not in runs[0]["argv"]
    log_text = (sd / "reconcile.log").read_text()
    assert "CRITICAL: candidate reconciler crashed" in log_text and "executing pinned last-good-reconciler" in log_text


def test_launcher_emergency_self_heal_and_ci_verdict(tmp_path, monkeypatch):
    launcher = _load_launcher()
    origin, co, sd, md, cfg_p, marker, sha1 = _make_git_setup(tmp_path)
    assert launcher.main(["--config", str(cfg_p)]) == 0  # seed LKG = sha1

    # Break checkout at c_bad, then push fix c_fix to origin
    rec = origin / "truenas" / "reconciler" / "reconcile.py"
    rec.write_text("import nonexistent_broken_mod\n")
    _git(origin, "add", "-A")
    _git(origin, "commit", "-qm", "c_bad")
    _git(co, "pull", "-q", "--ff-only")

    rec.write_text(
        "import json, os, sys\n"
        "sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))\n"
        "from reconciler_lib.helper import finish\n"
        f"open({str(marker)!r}, 'a').write(json.dumps({{'path': __file__, 'argv': sys.argv[1:]}}) + '\\n')\n"
        "sys.exit(finish(0))\n"
    )
    _git(origin, "add", "-A")
    _git(origin, "commit", "-qm", "c_fix")
    c_fix = _git(origin, "rev-parse", "HEAD")

    monkeypatch.setattr(launcher, "_ci_check_passed", lambda cfg, sha: sha == c_fix)
    marker.unlink()
    assert launcher.main(["--config", str(cfg_p), "--from-trigger"]) == 0
    assert _git(co, "rev-parse", "HEAD") == c_fix
    assert json.loads((sd / "ci-gate.json").read_text())["deployed"] == c_fix
    assert json.loads((sd / "last-good-reconciler" / "meta.json").read_text())["commit"] == c_fix
    assert _parse_prom(md / "apps_gitops_launcher.prom")["truenas_apps_gitops_launcher_fallback_active"].samples[0].value == 0.0

    # Verify _ci_check_passed GitHub JSON parsing directly
    monkeypatch.undo()
    monkeypatch.setattr(
        launcher.urllib.request,
        "urlopen",
        lambda req, timeout=15: io.BytesIO(json.dumps({"check_runs": [{"status": "completed", "conclusion": "success"}]}).encode()),
    )
    assert launcher._ci_check_passed({"repo": "https://github.com/o/r.git"}, c_fix) is True
    monkeypatch.setattr(
        launcher.urllib.request,
        "urlopen",
        lambda req, timeout=15: io.BytesIO(json.dumps({"check_runs": [{"status": "completed", "conclusion": "failure"}]}).encode()),
    )
    assert launcher._ci_check_passed({"repo": "https://github.com/o/r.git"}, c_fix) is False
