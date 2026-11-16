#!/usr/bin/env python3
"""Lint truenas/apps/* against the nas01 app standard (truenas/README.md). Exit 1 on any violation.

Usage: lint.py [truenas/apps]   (CI: .github/workflows/truenas-apps.yaml)
"""
import ast, json, os, re, sys
import yaml

REF = re.compile(r"\{\{\s*op://")
LEAK = re.compile(r"\{\{\s*op://|\.[a-z0-9-]+\.casa\b", re.IGNORECASE)


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
    if "service" in ui and ui["service"] not in (c.get("services") or {}):
        e("app.yaml ui.service must name a service in compose.yaml")
    meta = meta or {}
    h = meta.get("health")
    if not h:
        e("app.yaml health probe is required")
    elif not isinstance(h, dict) or ("tcp" not in h and "http" not in h):
        e("app.yaml health must be a map with 'tcp' or 'http'")
    else:
        hkeys = {"tcp", "http", "netns", "insecure", "timeout", "expect", "min_ready"}
        for hk in sorted(set(h) - hkeys):
            e(f"app.yaml health: unknown key {hk!r}")
        if "netns" in h and h["netns"] not in (c.get("services") or {}):
            e("app.yaml health.netns must name a service in compose.yaml")
        for tk in ("timeout", "min_ready"):
            if tk in h and (not isinstance(h[tk], int) or h[tk] < 0):
                e(f"app.yaml health.{tk} must be a non-negative integer")
    known = {"dirs", "files", "ini", "health", "ui", "critical", "bootstrap", "order", "deploy_timeout", "validate",
             "dashboards"}
    for k in sorted(set(meta) - known):
        e(f"app.yaml: unknown key {k!r}")
    if "critical" in meta and not isinstance(meta["critical"], bool):
        e("app.yaml critical must be true/false")
    if "bootstrap" in meta and not isinstance(meta["bootstrap"], bool):
        e("app.yaml bootstrap must be true/false")
    if "order" in meta and (not isinstance(meta["order"], int) or not (meta.get("critical") or meta.get("bootstrap"))):
        e("app.yaml order must be an integer and only set on critical or bootstrap apps")
    if "deploy_timeout" in meta and not (isinstance(meta["deploy_timeout"], int) and 30 <= meta["deploy_timeout"] <= 3600):
        e("app.yaml deploy_timeout must be an integer between 30 and 3600 (seconds)")
    v = meta.get("validate")
    if v is not None:
        if not isinstance(v, dict) or v.get("service") not in (c.get("services") or {}):
            e("app.yaml validate.service must name a service in compose.yaml")
        elif not isinstance(v.get("args", []), list):
            e("app.yaml validate.args must be a list")
    ds_cfg = meta.get("dashboards")
    if ds_cfg is not None:
        if not isinstance(ds_cfg, dict) or not ds_cfg.get("dir"):
            e("app.yaml dashboards must be a map with dir")
        else:
            ddir = os.path.join(d, ds_cfg["dir"])
            folders = ds_cfg.get("folders") or {}
            if not isinstance(folders, dict):
                e("app.yaml dashboards.folders must be a map")
            if ds_cfg.get("default_folder") and ds_cfg["default_folder"] not in folders:
                e(f"app.yaml dashboards.default_folder {ds_cfg['default_folder']!r} not in folders")
            for fn, fk in (ds_cfg.get("folder_map") or {}).items():
                if fk not in folders:
                    e(f"app.yaml dashboards.folder_map[{fn!r}] -> {fk!r} not in folders")
            if not os.path.isdir(ddir):
                e(f"app.yaml dashboards.dir {ds_cfg['dir']!r} does not exist")
            else:
                seen_uids = {}
                for fn in sorted(os.listdir(ddir)):
                    if not fn.endswith(".json"):
                        continue
                    fpath = os.path.join(ddir, fn)
                    raw_d = open(fpath).read()
                    if LEAK.search(raw_d):
                        e(f"dashboards/{fn}: forbidden op:// ref or domain string")
                    try:
                        dj = json.loads(raw_d)
                    except Exception as ex:  # noqa: BLE001
                        e(f"dashboards/{fn}: invalid JSON ({ex})")
                        continue
                    uid, title = dj.get("uid"), dj.get("title")
                    if not isinstance(uid, str) or not uid:
                        e(f"dashboards/{fn}: missing string uid")
                    elif uid in seen_uids:
                        e(f"dashboards/{fn}: duplicate uid {uid!r} (also in {seen_uids[uid]})")
                    else:
                        seen_uids[uid] = fn
                    if not isinstance(title, str) or not title:
                        e(f"dashboards/{fn}: missing string title")
    seen_dirs = []
    for ds in meta.get("dirs") or []:
        if not isinstance(ds, dict) or not isinstance(ds.get("path"), str):
            e(f"app.yaml dirs entries must be maps with path ({ds})")
            continue
        dp = ds["path"].rstrip("/")
        if not dp.startswith("/mnt/") or ".." in dp.split("/"):
            e(f"app.yaml dir path must be an absolute /mnt/... path without '..' ({ds['path']})")
        if "owner" in ds and not re.match(r"^\d+:\d+$", str(ds["owner"])):
            e(f"app.yaml dir owner must be uid:gid ({ds['owner']})")
        if "mode" in ds:
            try:
                int(str(ds["mode"]), 8)
            except ValueError:
                e(f"app.yaml dir mode must be octal ({ds['mode']})")
        for prev in seen_dirs:
            if prev.startswith(dp + "/"):
                e(f"app.yaml dir parent {dp} must appear before child {prev}")
        seen_dirs.append(dp)
    return errs


def lint_truenas_meta(apps_root, app_names):
    errs = []
    troot = os.path.dirname(os.path.abspath(apps_root))
    retired_p = os.path.join(troot, "retired.yaml")
    if os.path.exists(retired_p):
        doc = yaml.safe_load(open(retired_p)) or {}
        ret = doc.get("retired")
        if not isinstance(ret, list) or not all(isinstance(x, str) and x for x in ret):
            errs.append("retired.yaml: 'retired' must be a list of non-empty app names")
        else:
            for r in ret:
                if r in app_names:
                    errs.append(f"retired.yaml: {r!r} still exists in truenas/apps/")
    host_p = os.path.join(troot, "host", "host.yaml")
    if os.path.exists(host_p):
        hdoc = yaml.safe_load(open(host_p)) or {}
        files = hdoc.get("files")
        if not isinstance(files, list):
            errs.append("host/host.yaml: 'files' must be a list")
        else:
            for item in files:
                if not isinstance(item, dict) or not item.get("src") or not item.get("dest"):
                    errs.append(f"host/host.yaml: entry missing src/dest ({item})")
                    continue
                src_p = os.path.join(troot, "host", item["src"])
                if not os.path.isfile(src_p):
                    errs.append(f"host/host.yaml: missing src {item['src']!r}")
                elif src_p.endswith(".py"):
                    try:
                        ast.parse(open(src_p).read(), filename=src_p)
                    except SyntaxError as ex:
                        errs.append(f"host/{item['src']}: python syntax error: {ex}")
                if "owner" in item and not re.match(r"^\d+:\d+$", str(item["owner"])):
                    errs.append(f"host/host.yaml: owner must be uid:gid ({item['owner']})")
                if "mode" in item:
                    try:
                        int(str(item["mode"]), 8)
                    except ValueError:
                        errs.append(f"host/host.yaml: mode must be octal ({item['mode']})")
    return errs


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "apps")
    errs, shared = [], shared_networks(root)
    apps = [a for a in sorted(os.listdir(root)) if os.path.isfile(os.path.join(root, a, "compose.yaml"))]
    for a in apps:
        errs += lint_app(os.path.join(root, a), shared)
    errs += lint_truenas_meta(root, set(apps))
    for x in errs:
        print(f"::error::{x}" if os.environ.get("GITHUB_ACTIONS") else x)
    print(f"{'FAIL' if errs else 'OK'}: {len(errs)} violation(s)")
    return 1 if errs else 0


if __name__ == "__main__":
    sys.exit(main())
