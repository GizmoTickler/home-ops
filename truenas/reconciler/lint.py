#!/usr/bin/env python3
"""Lint truenas/apps/* against the nas01 app standard (truenas/README.md). Exit 1 on any violation.

Usage: lint.py [truenas/apps]   (CI: .github/workflows/truenas-apps.yaml)
"""
import os, re, sys
import yaml

REF = re.compile(r"\{\{\s*op://")


def shared_networks(apps_root):
    p = os.path.join(apps_root, "..", "networks.yaml")
    if not os.path.exists(p):
        return set()
    return {n["name"] for n in (yaml.safe_load(open(p)) or {}).get("networks") or []}


def lint_app(d, shared=frozenset()):
    name, errs = os.path.basename(d), []
    e = lambda m: errs.append(f"{name}: {m}")
    path = os.path.join(d, "compose.yaml")
    raw = open(path).read()
    lines = raw.splitlines()
    if not lines or not lines[0].startswith("# "):
        e("compose.yaml must start with a '# <App> — <purpose>' header")
    if "Managed by truenas/reconciler" not in raw:
        e("header must say 'Managed by truenas/reconciler'")
    c = yaml.safe_load(raw) or {}
    if "include" in c:
        e("`include:` is not allowed — inline the compose")
    if name == "onepassword-connect" and REF.search(yaml.safe_dump(c)):  # values only, not comments
        e("onepassword-connect must not contain op:// refs (it resolves them)")
    for sname, s in (c.get("services") or {}).items():
        p = f"services.{sname}"
        img = s.get("image", "")
        if "@sha256:" not in img:
            e(f"{p}.image must be pinned as tag@sha256:digest ({img})")
        if img.split("@")[0].endswith(":latest") or ":" not in img.split("@")[0].rsplit("/", 1)[-1]:
            e(f"{p}.image needs an explicit version tag (not latest/untagged)")
        if s.get("restart") != "unless-stopped":
            e(f"{p}.restart must be unless-stopped")
        env = s.get("environment")
        if not isinstance(env, dict) or "TZ" not in env:
            e(f"{p}.environment must be a map containing TZ")
        lim = (((s.get("deploy") or {}).get("resources") or {}).get("limits") or {})
        if not ("cpus" in lim and "memory" in lim):
            e(f"{p}.deploy.resources.limits needs cpus and memory")
        if (s.get("logging") or {}).get("driver") != "json-file" or "max-size" not in ((s.get("logging") or {}).get("options") or {}):
            e(f"{p}.logging must be the standard json-file rotation (*logging)")
        for port in s.get("ports") or []:
            if not isinstance(port, dict):
                e(f"{p}.ports must use the long form with host_ip ({port})")
            elif port.get("host_ip") in (None, "", "0.0.0.0", "::"):
                e(f"{p}.ports must bind a specific host_ip ({port})")
        for v in s.get("volumes") or []:
            if not isinstance(v, str):
                e(f"{p}.volumes must use the short 'src:dst[:ro]' form")
        # TCP congestion control is per network namespace; pin BBR so it never silently falls back to cubic if the
        # host default changes. Services joining another netns (service:/container:) inherit it; host netns uses the host's.
        nm = str(s.get("network_mode") or "")
        if not (nm == "host" or nm.startswith(("service:", "container:"))):
            sc = s.get("sysctls") or {}
            if isinstance(sc, list):
                sc = dict(x.split("=", 1) for x in sc if "=" in x)
            if sc.get("net.ipv4.tcp_congestion_control") != "bbr":
                e(f"{p}.sysctls must set net.ipv4.tcp_congestion_control: bbr")
    for nk, nv in (c.get("networks") or {}).items():
        if (nv or {}).get("external") and (nv.get("name") or nk) not in shared:
            e(f"networks.{nk}: external network {(nv.get('name') or nk)!r} is not defined in truenas/networks.yaml")
    meta_p = os.path.join(d, "app.yaml")
    meta = yaml.safe_load(open(meta_p)) if os.path.exists(meta_p) else {}
    ui = (meta or {}).get("ui") or {}
    for k in ("title", "description", "icon"):
        if not ui.get(k):
            e(f"app.yaml ui.{k} is required")
    if ui.get("icon") and not str(ui["icon"]).startswith("https://"):
        e("app.yaml ui.icon must be an https URL")
    if not (meta or {}).get("health"):
        e("app.yaml health probe is required")
    return errs


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "apps")
    errs, shared = [], shared_networks(root)
    for a in sorted(os.listdir(root)):
        if os.path.isfile(os.path.join(root, a, "compose.yaml")):
            errs += lint_app(os.path.join(root, a), shared)
    for x in errs:
        print(f"::error::{x}" if os.environ.get("GITHUB_ACTIONS") else x)
    print(f"{'FAIL' if errs else 'OK'}: {len(errs)} violation(s)")
    return 1 if errs else 0


if __name__ == "__main__":
    sys.exit(main())
