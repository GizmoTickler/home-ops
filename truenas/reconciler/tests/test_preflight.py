"""Preflight: static-IP / host-port conflict detection, live inventory parsing, compose config, validate, pulls."""
import json, os, subprocess

import pytest

from conftest import ME, rc

SHARED = {"mgmt": {"name": "mgmt", "driver": "macvlan"}, "apps": {"name": "apps", "driver": "bridge"}}


def cp(rc_=0, out="", err=""):
    return subprocess.CompletedProcess([], rc_, out, err)


def live(containers=(), host_ips=("192.168.120.10", "192.168.123.154"), listeners=()):
    return rc.Live(list(containers), host_ips, listeners)


def ctr(name, project, ips=None, ports=()):
    return {"name": name, "project": project, "ips": ips or {}, "ports": set(ports)}


def envoy_compose(ip="192.168.123.156", ports=None):
    c = {"services": {"envoy": {"image": "e:1", "networks": {"mgmt": {"ipv4_address": ip},
                                                             "apps": {"ipv4_address": "172.31.200.254"},
                                                             "internal": {"ipv4_address": "10.9.9.9"}}}},
         "networks": {"mgmt": {"external": True}, "apps": {"external": True}, "internal": {}}}
    if ports:
        c["services"]["envoy"]["ports"] = ports
    return c


@pytest.fixture
def no_lan(monkeypatch):
    probed = []
    monkeypatch.setattr(rc, "lan_in_use", lambda ip: probed.append(ip) or False)
    return probed


def test_ip_held_by_other_app(no_lan):
    lv = live([ctr("ix-caddy-caddy-1", "ix-caddy", {"mgmt": "192.168.123.156"})])
    probs = rc.conflicts("envoy", envoy_compose(), lv, SHARED)
    assert probs == ["envoy: mgmt address 192.168.123.156 is in use by container ix-caddy-caddy-1"]


def test_own_containers_and_internal_networks_ignored(no_lan):
    lv = live([ctr("ix-envoy-envoy-1", "ix-envoy", {"mgmt": "192.168.123.156", "apps": "172.31.200.254"}),
               ctr("other", "ix-other", {"ix-other_internal": "10.9.9.9"})])
    assert rc.conflicts("envoy", envoy_compose(), lv, SHARED) == []
    assert no_lan == []  # own macvlan IP: no LAN probe (the host can't reach its macvlan children anyway)


def test_bridge_ip_conflict():
    lv = live([ctr("x", "ix-x", {"apps": "172.31.200.254"})])
    assert rc.conflicts("envoy", envoy_compose(), lv, SHARED, probe_lan=False) == [
        "envoy: apps address 172.31.200.254 is in use by container x"]


def test_macvlan_ip_is_host_alias(no_lan):
    assert rc.conflicts("envoy", envoy_compose(ip="192.168.123.154"), live(), SHARED) == [
        "envoy: mgmt address 192.168.123.154 is a nas01 host address"]


def test_macvlan_ip_answers_on_lan(monkeypatch):
    monkeypatch.setattr(rc, "lan_in_use", lambda ip: ip == "192.168.123.156")
    assert rc.conflicts("envoy", envoy_compose(), live(), SHARED) == [
        "envoy: mgmt address 192.168.123.156 answers on the LAN (another host has it)"]
    assert rc.conflicts("envoy", envoy_compose(), live(), SHARED, probe_lan=False) == []


def test_lan_in_use_ping_and_neigh(monkeypatch):
    out = {"ping": cp(1), "ip": cp(0, "192.168.123.9 dev br0 lladdr aa:bb:cc:dd:ee:ff REACHABLE\n")}
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: out[cmd[0]])
    assert rc.lan_in_use("192.168.123.9")
    out["ip"] = cp(0, "192.168.123.9 dev br0 FAILED\n")
    assert not rc.lan_in_use("192.168.123.9")
    out["ping"] = cp(0)
    assert rc.lan_in_use("192.168.123.9")


PORT = [{"host_ip": "192.168.123.154", "target": 80, "published": 8080, "protocol": "tcp"}]


@pytest.mark.parametrize("containers,listeners,want", [
    ([], [], []),
    ([ctr("o", "ix-o", ports=[("192.168.123.154", 8080, "tcp")])], [], ["published by container o"]),
    ([ctr("o", "ix-o", ports=[("0.0.0.0", 8080, "tcp")])], [], ["published by container o"]),
    ([ctr("o", "ix-o", ports=[("192.168.123.154", 8080, "udp")])], [], []),
    ([], [("0.0.0.0", 8080, "tcp")], ["already bound by a process"]),
    ([], [("192.168.123.154", 8080, "tcp")], ["already bound by a process"]),
    ([], [("192.168.120.10", 8080, "tcp")], []),
    # our own docker-proxy listener is not a conflict
    ([ctr("me", "ix-app", ports=[("192.168.123.154", 8080, "tcp")])], [("192.168.123.154", 8080, "tcp")], []),
])
def test_port_conflicts(no_lan, containers, listeners, want):
    c = {"services": {"web": {"image": "x", "ports": PORT}}}
    probs = rc.conflicts("app", c, live(containers, listeners=listeners), SHARED)
    assert len(probs) == len(want) and all(w in p for w, p in zip(want, probs))


def test_host_ip_not_configured(no_lan):
    c = {"services": {"web": {"image": "x", "ports": [{"host_ip": "192.168.123.199", "target": 1, "published": 1}]}}}
    assert rc.conflicts("app", c, live(), SHARED) == ["web: host_ip 192.168.123.199 is not configured on nas01"]


def test_collect_live_parses_docker_ip_ss(monkeypatch):
    inspect = [{"Name": "/ix-envoy-envoy-1", "Config": {"Labels": {"com.docker.compose.project": "ix-envoy"}},
                "NetworkSettings": {"Networks": {"mgmt": {"IPAddress": "192.168.123.156"},
                                                 "apps": {"IPAddress": "", "IPAMConfig": {"IPv4Address": "172.31.200.254"}}},
                                    "Ports": {"9100/tcp": [{"HostIp": "192.168.120.10", "HostPort": "9100"},
                                                           {"HostIp": "::", "HostPort": "9100"}],
                                              "53/udp": None}}}]
    outs = {
        ("docker", "ps"): cp(0, "abc\n"),
        ("docker", "inspect"): cp(0, json.dumps(inspect)),
        ("ip", "-4"): cp(0, "1: lo    inet 127.0.0.1/8 scope host lo\n4: br0    inet 192.168.120.10/22 brd x\n"),
        ("ss", "-H"): cp(0, "tcp LISTEN 0 4096 192.168.120.10:9100 0.0.0.0:*\n"
                            "tcp LISTEN 0 4096 *:22 *:*\nudp UNCONN 0 0 [::]:123 [::]:*\n"
                            "tcp LISTEN 0 5 127.0.0.1%lo:631 0.0.0.0:*\n"),
    }
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: outs[tuple(cmd[:2])])
    lv = rc.collect_live()
    c = lv.containers[0]
    assert c["project"] == "ix-envoy" and c["ips"] == {"mgmt": "192.168.123.156", "apps": "172.31.200.254"}
    assert c["ports"] == {("192.168.120.10", 9100, "tcp")}
    assert lv.host_ips == {"127.0.0.1", "192.168.120.10"}
    assert lv.listeners == {("192.168.120.10", 9100, "tcp"), ("0.0.0.0", 22, "tcp"), ("0.0.0.0", 123, "udp"),
                            ("127.0.0.1", 631, "tcp")}


def test_compose_config_masks_and_cleans_up(ctx, monkeypatch):
    ctx.secrets["op://v/i/f"] = "SUPERSECRET"
    seen = {}

    def fake(cmd, **k):
        seen["path"] = cmd[cmd.index("-f") + 1]
        seen["content"] = open(seen["path"]).read()
        seen["mode"] = os.stat(seen["path"]).st_mode & 0o777
        return cp(1, "", "services.web.environment.X: invalid value SUPERSECRET")
    monkeypatch.setattr(rc, "sh", fake)
    err = rc.compose_config_check("app", {"services": {"web": {"environment": {"X": "SUPERSECRET"}}}}, ctx)
    assert "SUPERSECRET" in seen["content"] and seen["mode"] == 0o600
    assert "SUPERSECRET" not in err and "<op://v/i/f>" in err
    assert not os.path.exists(seen["path"]) and not os.listdir(os.path.dirname(os.path.dirname(seen["path"])))


def test_validate_runs_target_image_on_rendered_files(ctx, tmp_path, monkeypatch):
    ctx.secrets["op://v/i/f"] = "SUPERSECRET"
    cdir = tmp_path / "envoy-config"
    comp = {"services": {"envoy": {"image": "envoy:v2@sha256:x", "user": "1000:1000",
                                   "volumes": [f"{cdir}:/etc/envoy:ro", "/mnt/certs:/certs"]}}}
    files = [({"dest": f"{cdir}/envoy.yaml", "owner": ME, "mode": "0600"}, "secret: SUPERSECRET\n")]
    meta = {"validate": {"service": "envoy", "entrypoint": "envoy", "args": ["--mode", "validate", "-c", "/etc/envoy/envoy.yaml"]}}
    seen = {}

    def fake(cmd, **k):
        if cmd[:3] == ["docker", "image", "inspect"]:
            return cp(0)
        seen["cmd"] = cmd
        vols = [cmd[i + 1] for i, x in enumerate(cmd) if x == "-v"]
        seen["vols"] = vols
        src = vols[0].split(":")[0]
        seen["rendered"] = open(os.path.join(src, "envoy.yaml")).read()
        seen["src"] = src
        return cp(1, "", "error in /etc/envoy/envoy.yaml: SUPERSECRET bad")
    monkeypatch.setattr(rc, "sh", fake)
    err = rc.run_validate("envoy", meta, comp, files, ctx)
    cmd = seen["cmd"]
    assert cmd[:3] == ["docker", "run", "--rm"] and "--network" in cmd and cmd[cmd.index("--network") + 1] == "none"
    assert cmd[cmd.index("--entrypoint") + 1] == "envoy" and cmd[cmd.index("--user") + 1] == "1000:1000"
    assert cmd[-5:] == ["envoy:v2@sha256:x", "--mode", "validate", "-c", "/etc/envoy/envoy.yaml"]
    assert seen["vols"][0].endswith(":/etc/envoy:ro") and seen["vols"][1] == "/mnt/certs:/certs:ro"
    assert seen["rendered"] == "secret: SUPERSECRET\n"
    assert not os.path.exists(seen["src"])  # rendered secrets deleted right after
    assert "SUPERSECRET" not in err


def test_validate_ok(ctx, monkeypatch):
    monkeypatch.setattr(rc, "sh", lambda cmd, **k: cp(0, "configuration '/etc/envoy/envoy.yaml' OK"))
    comp = {"services": {"envoy": {"image": "e", "volumes": []}}}
    assert rc.run_validate("envoy", {"validate": {"service": "envoy"}}, comp, [], ctx) is None


def test_preflight_pulls_missing_image_only_when_not_dry(ctx, monkeypatch):
    calls = []

    def fake(cmd, **k):
        calls.append(cmd[:3])
        if cmd[:3] == ["docker", "image", "inspect"]:
            return cp(1)
        return cp(0)
    monkeypatch.setattr(rc, "sh", fake)
    monkeypatch.setattr(rc, "collect_live", lambda: live())
    monkeypatch.setattr(rc, "compose_config_check", lambda *a: None)
    c = {"services": {"w": {"image": "new:2"}}}
    ctx.dry = True
    assert rc.preflight("app", {}, c, [], ctx) == []
    assert ["docker", "pull", "--quiet"] not in calls
    ctx.dry, ctx.live = False, None
    assert rc.preflight("app", {}, c, [], ctx) == []
    assert ["docker", "pull", "--quiet"] in calls


def test_preflight_collects_all_problems(ctx, monkeypatch):
    monkeypatch.setattr(rc, "compose_config_check", lambda *a: "docker compose config: bad")
    monkeypatch.setattr(rc, "collect_live", lambda: live([ctr("x", "ix-x", {"apps": "172.31.200.254"})]))
    monkeypatch.setattr(rc, "image_present", lambda i: True)
    monkeypatch.setattr(rc, "lan_in_use", lambda ip: False)
    ctx.shared_nets = SHARED
    probs = rc.preflight("envoy", {}, envoy_compose(), [], ctx)
    assert probs[0].startswith("docker compose config") and "in use by container x" in probs[1]


def test_preflight_disabled(ctx):
    ctx.cfg["preflight"] = {"enabled": False}
    assert rc.preflight("app", {}, {}, [], ctx) == []
