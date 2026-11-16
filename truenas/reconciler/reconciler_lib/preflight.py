"""Preflight validation: compose config, static-IP/host-port/LAN conflicts, image pulls, and container `validate`."""

import json, os, shutil, stat, tempfile
import yaml

from . import runtime as rt


class Live:
    """Snapshot of what is live on nas01: running containers (compose project, IPs per network, published ports),
    host IPv4 addresses and listening sockets."""

    def __init__(self, containers, host_ips, listeners):
        self.containers, self.host_ips, self.listeners = containers, set(host_ips), set(listeners)


def _parse_addr(a):
    """'192.168.1.2:80' / '*:80' / '[::]:80' / '0.0.0.0%br0:80' -> (ip, port) with wildcard as '0.0.0.0'."""
    ip, _, port = a.rpartition(":")
    ip = ip.strip("[]").split("%", 1)[0]
    if ip in ("*", "::", "", "0.0.0.0"):
        ip = "0.0.0.0"
    try:
        return ip, int(port)
    except ValueError:
        return None


def collect_live():
    ids = rt.sh(["docker", "ps", "-q"], timeout=30).stdout.split()
    containers = []
    if ids:
        r = rt.sh(["docker", "inspect", *ids], timeout=60)
        if r.returncode != 0:
            raise RuntimeError(f"docker inspect failed: {r.stderr.strip()[-200:]}")
        for c in json.loads(r.stdout):
            labels = (c.get("Config") or {}).get("Labels") or {}
            ns = c.get("NetworkSettings") or {}
            ips = {}
            for net, v in (ns.get("Networks") or {}).items():
                ip = (v or {}).get("IPAddress") or ((v or {}).get("IPAMConfig") or {}).get("IPv4Address")
                if ip:
                    ips[net] = ip
            ports = set()
            for k, binds in (ns.get("Ports") or {}).items():
                proto = k.split("/", 1)[1] if "/" in k else "tcp"
                for b in binds or []:
                    hip = b.get("HostIp") or "0.0.0.0"
                    if ":" in hip:  # IPv6 binding: nas01 is IPv4-only
                        continue
                    ports.add((hip, int(b["HostPort"]), proto))
            containers.append({"name": (c.get("Name") or "").lstrip("/"),
                               "project": labels.get("com.docker.compose.project", ""), "ips": ips, "ports": ports})
    host_ips = set()
    for ln in rt.sh(["ip", "-4", "-o", "addr", "show"], timeout=30).stdout.splitlines():
        f = ln.split()
        if "inet" in f:
            host_ips.add(f[f.index("inet") + 1].split("/")[0])
    listeners = set()
    for ln in rt.sh(["ss", "-H", "-l", "-n", "-t", "-u"], timeout=30).stdout.splitlines():
        f = ln.split()
        if len(f) >= 5 and f[0] in ("tcp", "udp"):
            a = _parse_addr(f[4])
            if a:
                listeners.add((a[0], a[1], f[0]))
    return rt.Live(containers, host_ips, listeners)


def compose_networks(compose):
    """compose network key -> (docker network name, external?)"""
    out = {}
    for k, v in (compose.get("networks") or {}).items():
        v = v or {}
        out[k] = (v.get("name") or k, bool(v.get("external")))
    return out


def desired_ips(compose):
    """[(service, docker network name, ip)] for static addresses on external (shared) networks."""
    nets, out = compose_networks(compose), []
    for sname, s in (compose.get("services") or {}).items():
        sn = (s or {}).get("networks")
        if not isinstance(sn, dict):
            continue
        for k, v in sn.items():
            ip = (v or {}).get("ipv4_address")
            name, ext = nets.get(k, (k, False))
            if ip and ext:
                out.append((sname, name, ip))
    return out


def desired_ports(compose):
    """[(service, host_ip, published port, proto)] from long-form port mappings."""
    out = []
    for sname, s in (compose.get("services") or {}).items():
        for p in (s or {}).get("ports") or []:
            if isinstance(p, dict) and p.get("published"):
                pub = str(p["published"]).split("-", 1)[0]
                out.append((sname, p.get("host_ip") or "0.0.0.0", int(pub), p.get("protocol", "tcp")))
    return out


def lan_in_use(ip):
    """True if something on the LAN answers for ip (ping, or a resolved neighbour entry)."""
    if rt.sh(["ping", "-c", "2", "-W", "1", "-n", "-q", ip], timeout=10).returncode == 0:
        return True
    n = rt.sh(["ip", "neigh", "show", "to", ip], timeout=10).stdout
    return "lladdr" in n and not any(s in n for s in ("FAILED", "INCOMPLETE"))


def conflicts(name, compose, live, shared_nets, probe_lan=True):
    """Static-IP and host-port conflicts of `compose` against what is live on nas01, ignoring the app's own
    containers (project ix-<name>). Returns a list of human-readable problems (empty = OK)."""
    own = f"ix-{name}"
    mine = [c for c in live.containers if c["project"] == own]
    others = [c for c in live.containers if c["project"] != own]
    probs = []
    for svc, net, ip in desired_ips(compose):
        holder = next((c["name"] for c in others if c["ips"].get(net) == ip), None)
        if holder:
            probs.append(f"{svc}: {net} address {ip} is in use by container {holder}")
            continue
        if ip in live.host_ips:
            probs.append(f"{svc}: {net} address {ip} is a nas01 host address")
            continue
        if (shared_nets.get(net) or {}).get("driver") == "macvlan" and probe_lan \
                and not any(c["ips"].get(net) == ip for c in mine) and rt.lan_in_use(ip):
            probs.append(f"{svc}: {net} address {ip} answers on the LAN (another host has it)")
    own_binds = set().union(*[c["ports"] for c in mine]) if mine else set()
    for svc, hip, port, proto in desired_ports(compose):
        if hip != "0.0.0.0" and hip not in live.host_ips:
            probs.append(f"{svc}: host_ip {hip} is not configured on nas01")
            continue
        match = lambda a: a[1] == port and a[2] == proto and (a[0] == hip or "0.0.0.0" in (a[0], hip))
        holder = next((c["name"] for c in others if any(match(b) for b in c["ports"])), None)
        if holder:
            probs.append(f"{svc}: {hip}:{port}/{proto} is published by container {holder}")
            continue
        if any(match(l) for l in live.listeners) and not any(match(b) for b in own_binds):
            probs.append(f"{svc}: {hip}:{port}/{proto} is already bound by a process on nas01")
    return probs


def images(compose):
    return sorted({(s or {}).get("image") for s in (compose.get("services") or {}).values() if (s or {}).get("image")})


def image_present(img):
    return rt.sh(["docker", "image", "inspect", "--format", "{{.Id}}", img], timeout=60).returncode == 0


def preflight_tmp(ctx):
    base = os.path.join(ctx.cfg["state_dir"], "preflight")
    os.makedirs(base, mode=0o700, exist_ok=True)
    os.chmod(base, 0o700)
    return tempfile.mkdtemp(dir=base, prefix="pf-")


def write_secret_file(path, content, uid=None, gid=None, mode=0o600):
    """New file (O_EXCL, never follows a symlink); owned by the caller (root) unless uid/gid are given."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    data = content if isinstance(content, (bytes, bytearray)) else content.encode()
    with os.fdopen(fd, "wb") as f:
        f.write(data)
        if uid is not None and (uid, gid) != (os.getuid(), os.getgid()):
            rt.os.fchown(f.fileno(), uid, gid)
        os.fchmod(f.fileno(), mode)


def compose_config_check(name, compose, ctx):
    """`docker compose config` on the rendered compose (written to a root-only temp dir, deleted right after;
    output is never printed, errors are masked). Returns None or an error string."""
    tmp = preflight_tmp(ctx)
    try:
        p = os.path.join(tmp, "compose.yaml")
        write_secret_file(p, yaml.safe_dump(compose, sort_keys=False, default_flow_style=False, width=1000))
        r = rt.sh(["docker", "compose", "-f", p, "-p", f"preflight-{name}", "config", "--quiet"], timeout=60)
        if r.returncode != 0:
            return "docker compose config: " + ctx.mask(r.stderr.strip()[-400:] or f"exit {r.returncode}")
        return None
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def _stage_validate_dir(tmp, idx, src_dir, mine, svc_user):
    """Build a staging directory containing non-templated regular files from src_dir plus rendered `files:`."""
    d = os.path.join(tmp, f"v{idx}")
    os.mkdir(d, 0o700)
    uid, gid = rt.parse_owner(mine[0][0].get("owner"))
    dir_mode = os.lstat(src_dir).st_mode & 0o777 if os.path.isdir(src_dir) and not os.path.islink(src_dir) else (
        0o755 if svc_user and str(svc_user) != f"{uid}:{gid}" else 0o700)
    if (uid, gid) != (os.getuid(), os.getgid()):
        os.chown(d, uid, gid)
    os.chmod(d, dir_mode)
    templated = {os.path.normpath(fs["dest"]) for fs, _ in mine}

    def copy_tree(cur_src, cur_dst):
        for entry in os.scandir(cur_src):
            if entry.is_symlink():
                continue
            dst_p = os.path.join(cur_dst, entry.name)
            if entry.is_dir(follow_symlinks=False):
                os.mkdir(dst_p, 0o755)
                st = entry.stat(follow_symlinks=False)
                if (st.st_uid, st.st_gid) != (os.getuid(), os.getgid()):
                    os.chown(dst_p, st.st_uid, st.st_gid)
                os.chmod(dst_p, st.st_mode & 0o777)
                copy_tree(entry.path, dst_p)
            elif entry.is_file(follow_symlinks=False) and os.path.normpath(entry.path) not in templated:
                fd = os.open(entry.path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                try:
                    st = os.fstat(fd)
                    if stat.S_ISREG(st.st_mode) and st.st_size <= rt.MAX_FILE:
                        data = os.read(fd, rt.MAX_FILE + 1)
                        write_secret_file(dst_p, data, st.st_uid, st.st_gid, st.st_mode & 0o777)
                finally:
                    os.close(fd)

    if os.path.isdir(src_dir) and not os.path.islink(src_dir):
        copy_tree(src_dir, d)
    for fs, content in mine:
        fu, fg = rt.parse_owner(fs.get("owner"))
        out_p = os.path.join(d, os.path.relpath(os.path.normpath(fs["dest"]), src_dir))
        parent = os.path.dirname(out_p)
        if not os.path.isdir(parent):
            os.makedirs(parent, mode=0o755, exist_ok=True)
            if (fu, fg) != (os.getuid(), os.getgid()):
                os.chown(parent, fu, fg)
        if os.path.lexists(out_p):
            os.unlink(out_p)
        write_secret_file(out_p, content, fu, fg, int(str(fs.get("mode", "0600")), 8))
    return d


def run_validate(name, meta, compose, rendered_files, ctx):
    """app.yaml `validate`: run the service's target image (no network, read-only, no caps) with the service's
    volumes, where every volume source that receives rendered files is replaced by a staged file or temp dir
    holding the fully rendered files (preserving host siblings). Returns None or an error string."""
    v = meta.get("validate")
    if not v:
        return None
    svc = (compose.get("services") or {}).get(v["service"])
    if not svc:
        return f"validate: service {v['service']} not in compose"
    img = svc["image"]
    if not rt.image_present(img):
        if ctx.dry:
            ctx.log(f"{name}: validate skipped: image {img} not present yet (would be pulled first)")
            return None
        return f"validate: image {img} not present"
    tmp = preflight_tmp(ctx)
    try:
        args = ["docker", "run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp:size=64m",
                "--entrypoint", v.get("entrypoint", v["service"])]
        if svc.get("user"):
            args += ["--user", str(svc["user"])]
        for i, vol in enumerate(svc.get("volumes") or []):
            if not isinstance(vol, str):
                continue
            parts = vol.split(":")
            src, dst = parts[0], parts[1] if len(parts) > 1 else parts[0]
            src_c = os.path.normpath(src) if src.startswith("/") else src.rstrip("/")
            single = next(((fs, c) for fs, c in rendered_files
                           if fs["dest"].startswith("/") and os.path.normpath(fs["dest"]) == src_c), None)
            if single is not None:
                fs, content = single
                fu, fg = rt.parse_owner(fs.get("owner"))
                f = os.path.join(tmp, f"f{i}-{os.path.basename(src_c)}")
                write_secret_file(f, content, fu, fg, int(str(fs.get("mode", "0600")), 8))
                src = f
            elif src_c.startswith("/"):
                mine = [(fs, c) for fs, c in rendered_files
                        if fs["dest"].startswith("/") and os.path.normpath(fs["dest"]) != src_c
                        and os.path.commonpath([src_c, os.path.normpath(fs["dest"])]) == src_c]
                if mine:
                    src = _stage_validate_dir(tmp, i, src_c, mine, svc.get("user"))
            if src.startswith("/"):
                args += ["-v", f"{src}:{dst}:ro"]
        r = rt.sh(args + [img] + [str(a) for a in v.get("args", [])], timeout=int(v.get("timeout", 120)))
        if r.returncode != 0:
            out = (r.stderr.strip() or r.stdout.strip())[-600:]
            return f"validate ({v['service']}): " + ctx.mask(out or f"exit {r.returncode}")
        return None
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def preflight(name, meta, compose, rendered_files, ctx):
    """All checks that must pass before a running app is touched. Returns a list of problems (empty = OK).
    Not dry-run: missing images are pulled here, before the old containers are stopped."""
    pf = rt.cfg_get(ctx.cfg, "preflight")
    if not pf["enabled"]:
        return []
    probs = []
    e = rt.compose_config_check(name, compose, ctx)
    if e:
        probs.append(e)
    if ctx.live is None:
        ctx.live = rt.collect_live()
    probs += rt.conflicts(name, compose, ctx.live, ctx.shared_nets, pf["probe_lan"])
    for img in images(compose):
        if rt.image_present(img):
            continue
        if ctx.dry or not pf["pull_images"]:
            ctx.log(f"{name}: image {img} not present" + (" -- would pull" if ctx.dry else ""))
            continue
        ctx.log(f"{name}: pulling {img}")
        r = rt.sh(["docker", "pull", "--quiet", img], timeout=1800)
        if r.returncode != 0:
            probs.append(f"pull {img} failed: {ctx.mask(r.stderr.strip()[-300:])}")
    if not probs and meta.get("validate"):
        e = rt.run_validate(name, meta, compose, rendered_files, ctx)
        if e:
            probs.append(e)
        else:
            ctx.log(f"{name}: validate ({meta['validate']['service']}, target image) OK")
    return probs
