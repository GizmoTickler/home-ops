"""Tests for truenas/host/apps-east-west-nft.py and reconciler sync_apps_east_west integration."""

import importlib.util, os, types

from conftest import rc

SCRIPT_PATH = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "host", "apps-east-west-nft.py"))


def _load_mod():
    spec = importlib.util.spec_from_file_location("apps_east_west_nft", SCRIPT_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_classify_containers_and_project_fallback():
    mod = _load_mod()
    records = [
        {"name": "ix-cloudflared-cloudflared-1", "project": "ix-cloudflared", "ip": "172.31.200.5"},
        {"name": "ix-sonarr-sonarr-1", "project": "ix-sonarr", "ip": "172.31.200.23"},
        {"name": "ix-sonarr-recyclarr-1", "project": "sonarr", "ip": "172.31.200.24"},
        {"name": "ix-qbittorrent-gluetun-1", "project": "", "ip": "172.31.200.4"},
        {"name": "ix-pocket-id-pocket-id-1", "project": "pocket-id", "ip": "172.31.200.16"},
        {"name": "ix-it-tools-it-tools-1", "project": "", "ip": "172.31.200.11"},
    ]
    cf_ips, media_ips = mod.classify_containers(records)
    assert cf_ips == ["172.31.200.5"]
    assert media_ips == ["172.31.200.23", "172.31.200.24", "172.31.200.253", "172.31.200.4"]
    assert "172.31.200.16" not in media_ips
    assert "172.31.200.11" not in media_ips


def test_render_ruleset_with_and_without_cloudflared():
    mod = _load_mod()
    full_records = [
        {"name": "ix-cloudflared-cloudflared-1", "project": "cloudflared", "ip": "172.31.200.5"},
        {"name": "ix-sonarr-sonarr-1", "project": "sonarr", "ip": "172.31.200.23"},
    ]
    nft_with_cf = mod.render_ruleset("br-28f3ce2d09e9", full_records)
    assert "notrack" not in nft_with_cf
    assert "ct state" not in nft_with_cf
    assert "chain prerouting" not in nft_with_cf
    assert 'meta ibrname != "br-28f3ce2d09e9" accept' in nft_with_cf
    assert 'meta obrname != "br-28f3ce2d09e9" accept' in nft_with_cf
    assert "set cloudflared_ips" in nft_with_cf
    assert "elements = { 172.31.200.5 }" in nft_with_cf
    assert "ip saddr 172.31.200.254 counter accept" in nft_with_cf
    assert "ip daddr 172.31.200.254 tcp flags & (syn | ack) != syn counter accept" in nft_with_cf
    assert "ip saddr @cloudflared_ips ip daddr 172.31.200.254 tcp dport 8443 counter accept" in nft_with_cf
    assert "ip saddr @cloudflared_ips counter drop" in nft_with_cf
    assert "ip saddr 172.31.200.253 ip daddr 172.31.200.254 tcp dport { 443, 9093 } counter accept" in nft_with_cf
    assert "ip daddr 172.31.200.254 tcp dport 443 counter accept" in nft_with_cf
    assert "ip saddr @media_cluster ip daddr @media_cluster counter accept" in nft_with_cf
    assert nft_with_cf.strip().endswith("counter drop\n    }\n}")

    no_cf_records = [{"name": "ix-radarr-radarr-1", "project": "radarr", "ip": "172.31.200.21"}]
    nft_no_cf = mod.render_ruleset("br-28f3ce2d09e9", no_cf_records)
    assert "cloudflared_ips" not in nft_no_cf
    assert "172.31.200.21" in nft_no_cf
    assert "172.31.200.253" in nft_no_cf


def test_self_check_flag(capsys):
    mod = _load_mod()
    assert mod.main(["--self-check"]) == 0
    assert "self-check OK" in capsys.readouterr().out


def test_sync_apps_east_west_absent_vs_present(monkeypatch, tmp_path):
    logs = []
    ctx = types.SimpleNamespace(dry=False, log=logs.append)

    monkeypatch.setattr(rc, "APPS_EAST_WEST_SCRIPT", str(tmp_path / "missing.py"))
    called = []
    monkeypatch.setattr(rc, "sh", lambda cmd, **kw: called.append(cmd) or types.SimpleNamespace(returncode=0, stderr=""))
    assert rc.sync_apps_east_west(ctx) is True
    assert called == []

    fake_script = tmp_path / "apps-east-west-nft.py"
    fake_script.write_text("#!/usr/bin/env python3\n")
    monkeypatch.setattr(rc, "APPS_EAST_WEST_SCRIPT", str(fake_script))

    dry_ctx = types.SimpleNamespace(dry=True, log=logs.append)
    assert rc.sync_apps_east_west(dry_ctx) is True
    assert called == []

    assert rc.sync_apps_east_west(ctx) is True
    assert called == [["/usr/bin/python3", str(fake_script), "--apply"]]

    monkeypatch.setattr(rc, "sh", lambda cmd, **kw: types.SimpleNamespace(returncode=1, stderr="boom"))
    assert rc.sync_apps_east_west(ctx) is False
    assert any("apps-east-west: FAILED: boom" in x for x in logs)
