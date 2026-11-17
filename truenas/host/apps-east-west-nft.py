#!/usr/bin/env python3
"""Enforce L2 East-West microsegmentation on nas01's shared Docker `apps` bridge.

Pure Python stdlib so third-party or reconciler package breakage never disables enforcement.
Enforces a 100% stateless `table bridge apps_east_west` scoped to the `apps` bridge (`ibrname`/`obrname`)
and unloads `nf_conntrack_bridge` so `br0` KVM hairpin (`dns-0`) and Docker SNAT/DNAT conntrack stay untouched.
"""

import argparse
import json
import os
import subprocess
import sys

ENVOY_IP = "172.31.200.254"
WEBHOOK_IP = "172.31.200.253"
CLOUDFLARED_PROJECTS = frozenset({"cloudflared"})
MEDIA_CLUSTER_PROJECTS = frozenset({
    "autobrr",
    "bazarr",
    "chaptarr",
    "nzbget",
    "prowlarr",
    "qbittorrent",
    "qui",
    "radarr",
    "seerr",
    "sonarr",
    "webhook",
})


def _docker_json(args):
    p = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=30)
    if p.returncode != 0:
        raise RuntimeError(f"docker {' '.join(args)} failed: {p.stderr.strip()}")
    return json.loads(p.stdout)


def _project_from_name(name):
    clean = str(name or "").lstrip("/")
    if not clean.startswith("ix-"):
        return ""
    body = clean[3:]
    for proj in sorted(CLOUDFLARED_PROJECTS | MEDIA_CLUSTER_PROJECTS, key=len, reverse=True):
        if body == proj or body.startswith(proj + "-"):
            return proj
    return body.split("-")[0] if body else ""


def inspect_apps_network():
    """Return `(bridge_ifname, container_records)` or `(None, [])` if `apps` network is absent."""
    p = subprocess.run(["docker", "network", "inspect", "apps"], capture_output=True, text=True, timeout=30)
    if p.returncode != 0:
        if "no such network" in p.stderr.lower():
            return None, []
        raise RuntimeError(f"docker network inspect apps failed: {p.stderr.strip()}")
    nets = json.loads(p.stdout)
    if not nets:
        return None, []
    net = nets[0]
    net_id = str(net.get("Id") or "")
    if len(net_id) < 12:
        raise RuntimeError(f"unexpected apps network Id: {net_id!r}")
    br_if = f"br-{net_id[:12]}"

    raw_containers = net.get("Containers") or {}
    cids = sorted(raw_containers.keys())
    labels_by_id = {}
    if cids:
        inspected = _docker_json(["inspect", *cids])
        for item in inspected:
            cid = item.get("Id", "")
            labels = ((item.get("Config") or {}).get("Labels") or {})
            labels_by_id[cid] = {
                "name": item.get("Name", ""),
                "project": labels.get("com.docker.compose.project", ""),
                "service": labels.get("com.docker.compose.service", ""),
            }

    records = []
    for cid, meta in raw_containers.items():
        ipv4 = str(meta.get("IPv4Address") or "").split("/", 1)[0].strip()
        if not ipv4:
            continue
        info = labels_by_id.get(cid, {})
        name = info.get("name") or meta.get("Name", "")
        project = str(info.get("project") or _project_from_name(name)).removeprefix("ix-")
        records.append({
            "id": cid,
            "name": str(name).lstrip("/"),
            "project": project,
            "service": info.get("service", ""),
            "ip": ipv4,
        })
    return br_if, records


def classify_containers(records):
    """Split container records into sorted IPv4 sets `(cloudflared_ips, media_ips)`."""
    cf_ips = set()
    media_ips = {WEBHOOK_IP}
    for rec in records:
        ip = str(rec.get("ip") or "").strip()
        if not ip:
            continue
        project = (str(rec.get("project") or "") or _project_from_name(rec.get("name", ""))).removeprefix("ix-")
        if project in CLOUDFLARED_PROJECTS:
            cf_ips.add(ip)
        if project in MEDIA_CLUSTER_PROJECTS:
            media_ips.add(ip)
    return sorted(cf_ips), sorted(media_ips)


def render_ruleset(br_if, records):
    """Render atomic `table bridge apps_east_west` nftables policy for `br_if`."""
    cf_ips, media_ips = classify_containers(records)
    lines = [
        "table bridge apps_east_west",
        "delete table bridge apps_east_west",
        "",
        "table bridge apps_east_west {",
        "    set media_cluster {",
        "        type ipv4_addr",
        f"        elements = {{ {', '.join(media_ips)} }}",
        "    }",
    ]
    if cf_ips:
        lines += [
            "    set cloudflared_ips {",
            "        type ipv4_addr",
            f"        elements = {{ {', '.join(cf_ips)} }}",
            "    }",
        ]
    lines += [
        "    chain forward {",
        "        type filter hook forward priority filter; policy accept;",
        f'        meta ibrname != "{br_if}" accept',
        f'        meta obrname != "{br_if}" accept',
        "        ether type arp accept",
        f"        ip saddr {ENVOY_IP} counter accept",
        f"        ip daddr {ENVOY_IP} tcp flags & (syn | ack) != syn counter accept",
    ]
    if cf_ips:
        lines += [
            f"        ip saddr @cloudflared_ips ip daddr {ENVOY_IP} tcp dport 8443 counter accept",
            "        ip saddr @cloudflared_ips counter drop",
        ]
    lines += [
        f"        ip saddr {WEBHOOK_IP} ip daddr {ENVOY_IP} tcp dport {{ 443, 9093 }} counter accept",
        f"        ip daddr {ENVOY_IP} tcp dport 443 counter accept",
        "        ip saddr @media_cluster ip daddr @media_cluster counter accept",
        "        counter drop",
        "    }",
        "}",
        "",
    ]
    return "\n".join(lines)


def _which(prog):
    for d in os.environ.get("PATH", "/usr/sbin:/sbin:/usr/bin:/bin").split(os.pathsep):
        p = os.path.join(d, prog)
        if os.path.isfile(p) and os.access(p, os.X_OK):
            return p
    return None


def main(argv=None):
    ap = argparse.ArgumentParser(description="Enforce L2 East-West isolation on nas01 Docker apps bridge")
    ap.add_argument("--self-check", action="store_true", help="validate imports/syntax and exit")
    ap.add_argument("--dry-run", action="store_true", help="print generated ruleset and syntax-check with nft -c")
    ap.add_argument("--apply", action="store_true", help="apply stateless nftables ruleset and unload nf_conntrack_bridge")
    args = ap.parse_args(argv)

    if args.self_check:
        print("apps-east-west-nft: self-check OK")
        return 0

    br_if, records = inspect_apps_network()
    if br_if is None:
        return 0
    ruleset = render_ruleset(br_if, records)

    if args.dry_run:
        print(ruleset, end="")
        nft = _which("nft")
        if nft:
            p = subprocess.run([nft, "-c", "-f", "-"], input=ruleset, text=True, capture_output=True, timeout=30)
            if p.returncode != 0:
                print(p.stderr.strip(), file=sys.stderr)
                return p.returncode
        return 0

    subprocess.run(["nft", "-f", "-"], input=ruleset, text=True, check=True, timeout=30)
    subprocess.run(["modprobe", "-r", "nf_conntrack_bridge"], capture_output=True, timeout=30)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
