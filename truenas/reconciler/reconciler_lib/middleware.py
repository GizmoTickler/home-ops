"""Subprocess helpers, TrueNAS middleware (`midclt`), Docker networks, and health probes."""

import json, os, re, socket, ssl, subprocess, sys
import yaml

from . import runtime as rt


def sh(cmd, timeout=60, input=None):
    """Run a command, never through a shell. Returns CompletedProcess (text). Patched in tests."""
    return subprocess.run(cmd, input=input, capture_output=True, text=True, timeout=timeout)


def midclt(*args, payload=None, job=False, timeout=900):
    cmd = ["midclt", "call"] + (["-j"] if job else []) + list(args)
    if payload is not None:
        cmd.append("-")
    p = rt.sh(cmd, timeout=timeout, input=None if payload is None else json.dumps(payload))
    if p.returncode != 0:
        # -j prints progress bars on stderr before the error; keep the last meaningful line(s)
        err = "\n".join(x for x in re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", p.stderr).splitlines()
                        if x.strip() and not x.startswith(("Status:", "Total Progress:")))
        raise RuntimeError(f"midclt {' '.join(args)} failed: {err.strip()[-800:]}")
    out = p.stdout.strip()
    # -j prints job progress lines before the JSON result; the result is the last line
    last = out.splitlines()[-1] if out else "null"
    try:
        return json.loads(last)
    except json.JSONDecodeError:
        return out


def app_state(name):
    r = rt.midclt("app.query", json.dumps([["name", "=", name]]))
    return r[0] if r else None


def ensure_started(name, ctx):
    a = rt.app_state(name)
    if a and a.get("state") in ("STOPPED", "CRASHED"):
        ctx.log(f"{name}: state {a.get('state')} after deploy -- starting")
        rt.midclt("app.start", name, job=True)


def container_status(name):
    try:
        return rt.sh(["docker", "ps", "-a", "--filter", f"label=com.docker.compose.project=ix-{name}",
                      "--format", "{{.Names}} {{.Status}}"], timeout=30).stdout.strip().replace("\n", "; ")
    except Exception as e:  # noqa: BLE001
        return f"(docker ps failed: {e})"


def wait_running(name, ctx, timeout=None):
    """State-aware wait. RUNNING -> True. DEPLOYING/STOPPING -> keep waiting up to `timeout`. CRASHED/STOPPED/absent
    -> failure once that terminal state has persisted for SETTLE s (TrueNAS briefly reports them while recreating)."""
    if timeout is None:
        timeout = rt.RUN_TIMEOUT
    start, st, terminal_since = rt._now(), None, None
    while True:
        a = rt.app_state(name)
        st = a.get("state") if a else None
        now = rt._now()
        if st == "RUNNING":
            return True
        if st in ("CRASHED", "STOPPED", None):
            terminal_since = terminal_since if terminal_since is not None else now
            if now - terminal_since >= rt.SETTLE:
                ctx.log(f"{name}: {st or 'ABSENT'} after deploy (terminal) -- containers: {rt.container_status(name)}")
                return False
        else:
            terminal_since = None
        if now - start >= timeout:
            ctx.log(f"{name}: not RUNNING after {int(timeout)} s (state={st}) -- containers: {rt.container_status(name)}")
            return False
        rt._sleep(rt.POLL)


def container_pid(app, service):
    out = rt.sh(["docker", "ps", "-q", "--filter", f"label=com.docker.compose.project=ix-{app}",
                 "--filter", f"label=com.docker.compose.service={service}"], timeout=30).stdout.split()
    if not out:
        raise RuntimeError(f"no running container for {app}/{service}")
    r = rt.sh(["docker", "inspect", "-f", "{{.State.Pid}}", out[0]], timeout=30)
    if r.returncode != 0:
        raise RuntimeError(f"docker inspect {app}/{service} failed")
    return r.stdout.strip()


def container_restarts(app):
    """Return {container_id: (running, restart_count)} for an app's containers."""
    r = rt.sh(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project=ix-{app}"], timeout=30)
    ids = r.stdout.split() if r.returncode == 0 else []
    if not ids:
        return {}
    ins = rt.sh(["docker", "inspect", *ids], timeout=30)
    if ins.returncode != 0:
        return {}
    out = {}
    for c in json.loads(ins.stdout):
        st = c.get("State") or {}
        out[c.get("Id", "")] = (bool(st.get("Running")), int(c.get("RestartCount") or 0))
    return out


def _http_probe(url, insecure=False, expect=None, netns_pid=None):
    """Run an HTTP(S) health probe directly or inside a container's network namespace."""
    if netns_pid is not None:
        script = (
            "import ssl, sys, urllib.request\n"
            "u, ins, exp = sys.argv[1], sys.argv[2] == '1', sys.argv[3]\n"
            "ctx = ssl._create_unverified_context() if ins else None\n"
            "with urllib.request.urlopen(u, timeout=10, context=ctx) as r:\n"
            "    body = r.read(65536).decode('utf-8', 'replace')\n"
            "    if r.status >= 400:\n"
            "        raise SystemExit(f'HTTP {r.status}')\n"
            "    if exp and exp not in body:\n"
            "        raise SystemExit(f'body missing {exp!r}')\n"
        )
        r = rt.sh(["nsenter", f"--net=/proc/{netns_pid}/ns/net", sys.executable, "-c", script,
                   url, "1" if insecure else "0", expect or ""], timeout=20)
        if r.returncode != 0:
            raise RuntimeError((r.stderr.strip().splitlines() or r.stdout.strip().splitlines() or ["http probe failed"])[-1])
        return
    c = ssl._create_unverified_context() if insecure else None
    with rt.urllib.request.urlopen(url, timeout=10, context=c) as r:
        body = r.read(65536).decode("utf-8", "replace")
        if r.status >= 400:
            raise RuntimeError(f"HTTP {r.status}")
        if expect and expect not in body:
            raise RuntimeError(f"body missing {expect!r}")


def healthy(meta, name, ctx):
    """Probe after RUNNING. Supports tcp and http inside `netns: <service>`, `expect` body checks, and `min_ready`
    restart-stability confirmation."""
    h = meta.get("health")
    if not h:
        return True
    expect = h.get("expect")
    min_ready = int(h.get("min_ready", rt.cfg_get(ctx.cfg, "min_ready_seconds")) or 0)

    def probe_once():
        if "tcp" in h and h.get("netns"):
            host, port = h["tcp"].rsplit(":", 1)
            pid = rt.container_pid(name, h["netns"])
            r = rt.sh(["nsenter", f"--net=/proc/{pid}/ns/net", sys.executable, "-c",
                       "import socket,sys;socket.create_connection((sys.argv[1],int(sys.argv[2])),5).close()",
                       host, port], timeout=20)
            if r.returncode != 0:
                raise RuntimeError((r.stderr.strip().splitlines() or ["probe failed"])[-1])
        elif "tcp" in h:
            host, port = h["tcp"].rsplit(":", 1)
            socket.create_connection((host, int(port)), timeout=5).close()
        elif "http" in h and h.get("netns"):
            pid = rt.container_pid(name, h["netns"])
            rt._http_probe(h["http"], bool(h.get("insecure")), expect, netns_pid=pid)
        else:
            rt._http_probe(h["http"], bool(h.get("insecure")), expect)

    deadline, err = rt._now() + int(h.get("timeout", 120)), None
    while rt._now() < deadline:
        try:
            probe_once()
            if min_ready > 0:
                before = rt.container_restarts(name)
                if not before or any(not run for run, _ in before.values()):
                    raise RuntimeError("app containers not running for min_ready check")
                rt._sleep(min_ready)
                after = rt.container_restarts(name)
                if set(after) != set(before) or any(not run for run, _ in after.values()):
                    raise RuntimeError("container restarted/disappeared during min_ready soak")
                for cid, (_, cnt_b) in before.items():
                    if after[cid][1] != cnt_b:
                        raise RuntimeError(f"container {cid[:12]} restarted during min_ready soak")
                probe_once()
            return True
        except Exception as e:  # noqa: BLE001
            err = e
            rt._sleep(5)
    ctx.log(f"{name}: health probe failed: {err}; containers: {rt.container_status(name)}")
    return False


def load_networks(path):
    if not os.path.exists(path):
        return []
    with open(path) as f:
        return (yaml.safe_load(f) or {}).get("networks") or []


def ensure_networks(path, ctx):
    """Create the shared networks from truenas/networks.yaml that do not exist yet. Returns False on any problem."""
    nets = load_networks(path)
    ctx.shared_nets = {n["name"]: n for n in nets}
    ok = True
    for n in nets:
        name = n["name"]
        r = rt.sh(["docker", "network", "inspect", name], timeout=30)
        if r.returncode == 0:
            live = json.loads(r.stdout)[0]
            subnets = [c.get("Subnet") for c in (live.get("IPAM") or {}).get("Config") or []]
            parent = (live.get("Options") or {}).get("parent")
            if live.get("Driver") != n["driver"] or n["subnet"] not in subnets or parent != n.get("parent"):
                ctx.log(f"network {name}: live spec differs from networks.yaml (driver={live.get('Driver')} "
                        f"subnets={subnets} parent={parent}) -- not changed")
                ok = False
            continue
        cmd = ["docker", "network", "create", "--driver", n["driver"], "--subnet", n["subnet"],
               "--ipv6=false", "--label", "managed-by=truenas-reconciler"]
        if n.get("gateway"):
            cmd += ["--gateway", n["gateway"]]
        if n.get("parent"):
            cmd += ["-o", f"parent={n['parent']}"]
        if n.get("internal"):
            cmd.append("--internal")
        if ctx.dry:
            ctx.log(f"network {name}: absent -- would create ({n['driver']} {n['subnet']})")
            continue
        r = rt.sh(cmd + [name], timeout=60)
        if r.returncode != 0:
            ctx.log(f"network {name}: create FAILED: {r.stderr.strip()[-300:]}")
            ok = False
        else:
            ctx.log(f"network {name}: created ({n['driver']} {n['subnet']})")
    return ok


def deploy_compose(name, compose, exists):
    if exists:
        rt.midclt("app.update", name, payload={"custom_compose_config": compose}, job=True)
    else:
        rt.midclt("app.create", payload={"app_name": name, "custom_app": True, "custom_compose_config": compose}, job=True)
