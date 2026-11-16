"""Declarative Grafana folder & dashboard sync inside the container network namespace."""

import json, os, sys

from . import runtime as rt


def _env_lookup(svc_cfg, key):
    env = (svc_cfg or {}).get("environment")
    if isinstance(env, dict):
        return env.get(key)
    if isinstance(env, list):
        prefix = f"{key}="
        for entry in env:
            if isinstance(entry, str) and entry.startswith(prefix):
                return entry[len(prefix):]
    return None


def _grafana_sync(url, user, password, folders, items, pid, dry):
    """Sync Grafana folders and dashboards via nsenter inside the container netns using stdin JSON."""
    script = r"""
import base64, json, sys, urllib.error, urllib.request
req = json.load(sys.stdin)
base = req["url"].rstrip("/")
auth = base64.b64encode((req["user"] + ":" + req["password"]).encode()).decode()
def api(path, method="GET", body=None):
    hdr = {"Authorization": "Basic " + auth, "Accept": "application/json"}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        hdr["Content-Type"] = "application/json"
    r = urllib.request.Request(base + path, data=data, method=method, headers=hdr)
    with urllib.request.urlopen(r, timeout=15) as resp:
        return json.loads(resp.read().decode())

live_folders = {f["uid"]: f for f in api("/api/folders?limit=200")}
for title, uid in req["folders"].items():
    if uid not in live_folders and not req["dry"]:
        api("/api/folders", "POST", {"uid": uid, "title": title})

out = []
for item in req["dashboards"]:
    uid, fuid, want = item["uid"], item["folderUid"], item["dashboard"]
    try:
        cur = api("/api/dashboards/uid/" + uid)
        live = dict(cur.get("dashboard") or {})
        live.pop("id", None)
        live.pop("version", None)
        live_fuid = (cur.get("meta") or {}).get("folderUid", "")
        if live == want and live_fuid == fuid:
            out.append({"uid": uid, "file": item["file"], "status": "ok"})
            continue
        rec = {"uid": uid, "file": item["file"], "status": "changed" if req["dry"] else "updated",
               "live_fuid": live_fuid, "live_dashboard": live}
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
        rec = {"uid": uid, "file": item["file"], "status": "missing" if req["dry"] else "created"}
    if not req["dry"]:
        api("/api/dashboards/db", "POST", {"dashboard": {**want, "id": None}, "folderUid": fuid, "overwrite": True})
    out.append(rec)
print(json.dumps(out))
"""
    payload = {"url": url, "user": user, "password": password, "folders": folders, "dashboards": items, "dry": bool(dry)}
    r = rt.sh(["nsenter", f"--net=/proc/{pid}/ns/net", sys.executable, "-c", script],
              timeout=120, input=json.dumps(payload))
    if r.returncode != 0:
        raise RuntimeError((r.stderr.strip() or r.stdout.strip())[-400:] or f"exit {r.returncode}")
    return json.loads(r.stdout.strip() or "[]")


def reconcile_dashboards(appdir, name, meta, desired, applied_entry, ctx):
    """Ensure declared Grafana dashboards and folders match git; return (ok, dash_fingerprints_or_None)."""
    dspec = meta.get("dashboards")
    if not dspec:
        return True, None
    ddir = os.path.join(appdir, dspec["dir"])
    if not os.path.isdir(ddir):
        ctx.log(f"{name}: dashboards dir {dspec['dir']} not found")
        return False, None
    folders = dict(dspec.get("folders") or {})
    default_folder = dspec.get("default_folder", "infrastructure")
    fmap = dspec.get("folder_map") or {}
    items, want_fp = [], {}
    for fn in sorted(os.listdir(ddir)):
        if not fn.endswith(".json"):
            continue
        with open(os.path.join(ddir, fn)) as f:
            dash = json.load(f)
        dash.pop("id", None)
        dash.pop("version", None)
        uid = dash.get("uid")
        if not uid:
            ctx.log(f"{name}: dashboard {fn} missing uid")
            return False, None
        fname = fmap.get(fn, default_folder)
        fuid = folders.get(fname, "")
        want_fp[uid] = rt.fingerprint(ctx, {"folderUid": fuid, "dashboard": dash})
        items.append({"uid": uid, "file": fn, "folderUid": fuid, "dashboard": dash})

    svc = dspec.get("service", name)
    svc_cfg = (desired.get("services") or {}).get(svc)
    user = _env_lookup(svc_cfg, dspec.get("user_env", "GF_SECURITY_ADMIN_USER"))
    password = _env_lookup(svc_cfg, dspec.get("pass_env", "GF_SECURITY_ADMIN_PASSWORD"))
    if not user or not password:
        ctx.log(f"{name}: dashboard sync missing credentials env on service {svc}")
        return False, None

    try:
        pid = rt.container_pid(name, svc)
    except Exception as e:  # noqa: BLE001
        if ctx.dry:
            ctx.log(f"{name}: dashboards JSON validated ({len(items)} files; container not running in dry-run)")
            return True, None
        ctx.log(f"{name}: dashboards sync failed locating container: {ctx.mask(str(e))}")
        return False, None

    prev_dash = (applied_entry or {}).get("dashboards") or {}
    try:
        res = rt._grafana_sync(dspec.get("url", "http://127.0.0.1:3000"), user, password, folders, items, pid, ctx.dry)
    except Exception as e:  # noqa: BLE001
        ctx.log(f"{name}: dashboards sync FAILED: {ctx.mask(str(e))}")
        return False, None

    for row in res:
        st = row.get("status")
        uid, fn = row["uid"], row["file"]
        if st == "ok":
            continue
        if "live_dashboard" in row:
            lfp = rt.fingerprint(ctx, {"folderUid": row.get("live_fuid", ""), "dashboard": row["live_dashboard"]})
            if uid in prev_dash and prev_dash[uid] != lfp:
                ctx.drift[name] = True
                ctx.event("drift", name)
                ctx.log(f"{name}: DRIFT -- dashboard {uid} ({fn}) changed outside git"
                        + (" (will be re-imported)" if ctx.dry else " (re-imported from git)"))
                continue
        ctx.log(f"{name}: dashboard {uid} ({fn}) {'would be ' + st if ctx.dry else st}")
    return True, want_fp
