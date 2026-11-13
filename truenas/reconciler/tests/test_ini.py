"""ini pins: change detection, the key-must-exist rule, symlink-safe patching with backup, and rollback."""
import os

import pytest

from conftest import ME, compose, make_app, rc

INI = "[Prefs]\n# Session\\Port=1\nSession\\Port=6881\nWebUI\\Enabled=true\nSession\\Port=9999\n"


def test_ini_changes_and_missing(tmp_path):
    p = tmp_path / "a.conf"
    p.write_text(INI)
    ch, missing = rc.ini_changes({"path": str(p), "set": {"Session\\Port": 7000, "WebUI\\Enabled": "true",
                                                           "Nope": "x"}})
    assert ch == {"Session\\Port": ("6881", "7000")}  # first real occurrence; comments/sections ignored
    assert missing == ["Nope"]


def test_ini_apply_rewrites_preserves_mode_and_backs_up(tmp_path, clock):
    p = tmp_path / "a.conf"
    p.write_text(INI)
    os.chmod(p, 0o640)
    bdir = tmp_path / "backup" / "app"
    rc.ini_apply({"path": str(p), "set": {"Session\\Port": "7000"}}, str(bdir))
    new = p.read_text()
    assert "Session\\Port=7000" in new and "# Session\\Port=1" in new and "6881" not in new
    assert os.stat(p).st_mode & 0o777 == 0o640
    backups = os.listdir(bdir)
    assert len(backups) == 1 and rc.BACKUP_TS.search(backups[0][:-5])
    assert (bdir / backups[0]).read_text() == INI
    assert os.stat(bdir / backups[0]).st_mode & 0o777 == 0o600


def test_write_file_refuses_symlink(tmp_path):
    target = tmp_path / "target"
    target.write_text("x")
    link = tmp_path / "link"
    link.symlink_to(target)
    with pytest.raises(RuntimeError):
        rc.write_file(str(link), "y", ME, 0o600)
    assert target.read_text() == "x"


def test_read_refuses_fifo(tmp_path):
    f = tmp_path / "fifo"
    os.mkfifo(f)
    with pytest.raises(RuntimeError):
        rc.read_file(str(f))


def test_missing_ini_key_fails_app(tmp_path, ctx, mid, monkeypatch):
    p = tmp_path / "a.conf"
    p.write_text(INI)
    c = compose()
    mid.add("app", c)
    d = make_app(tmp_path / "apps", "app", c, {"ini": [{"path": str(p), "set": {"Nope": "1"}}]})
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    assert rc.reconcile_app(d, ctx) is False
    assert mid.mutations() == []
    assert any("ini key 'Nope' not present" in ln for ln in ctx.log_lines)


def test_ini_patch_rolled_back_on_failed_deploy(tmp_path, ctx, mid, clock, monkeypatch):
    p = tmp_path / "a.conf"
    p.write_text(INI)
    c = compose()
    mid.add("app", c)
    mid.script["app"] = ["STOPPED"] + ["CRASHED"] * 100  # never comes back
    d = make_app(tmp_path / "apps", "app", c, {"ini": [{"path": str(p), "set": {"Session\\Port": "7000"}}]})
    monkeypatch.setattr(rc, "apply_ui", lambda *a: False)
    monkeypatch.setattr(rc, "preflight", lambda *a: [])
    monkeypatch.setattr(rc, "container_status", lambda n: "")
    assert rc.reconcile_app(d, ctx) is False
    assert p.read_text() == INI  # restored
    methods = [m for m, *_ in mid.mutations()]
    assert methods.count("app.stop") == 2  # before patch, before restore
    assert ("app.update", ("app",), {"custom_compose_config": c}) in mid.calls  # previous compose re-applied
    assert "app.redeploy" not in methods
