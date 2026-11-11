#!/usr/bin/env python3
"""Export a live nas01 app's compose as a git-safe compose.yaml (run ON nas01 as root).

The domain (op://Infrastructure/cluster-config/SECRET_DOMAIN, resolved via Connect) is replaced with that op ref,
and values under secret-looking keys are replaced with a placeholder ref that must be filled in by hand.
Only the sanitized YAML is printed; nothing raw leaves the host.

Usage: export_app.py <app> | export_app.py --include <compose.yaml path> <app>
"""
import json, re, subprocess, sys, os
import yaml
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from reconcile import Ctx, DEFAULT_CONFIG  # noqa: E402

DOMAIN_REF = "op://Infrastructure/cluster-config/SECRET_DOMAIN"
SECRETISH = re.compile(r"(?i)(pass(word)?|secret|token|api[_-]?key|private[_-]?key|session|credential)")


def main():
    args = sys.argv[1:]
    inc = None
    if args and args[0] == "--include":
        inc, args = args[1], args[2:]
    app = args[0]
    cfg = json.load(open(DEFAULT_CONFIG))
    ctx = Ctx(cfg, dry=True)
    domain = ctx.resolve(DOMAIN_REF)
    if inc:
        conf = yaml.safe_load(open(inc))
    else:
        out = subprocess.run(["midclt", "call", "app.config", app], capture_output=True, text=True, check=True)
        conf = json.loads(out.stdout.strip().splitlines()[-1])
    flagged = []

    def walk(o, key=None, path=""):
        if isinstance(o, dict):
            return {k: walk(v, k, f"{path}.{k}") for k, v in o.items()}
        if isinstance(o, list):
            return [walk(v, key, f"{path}[]") for v in o]
        if isinstance(o, str):
            if key and SECRETISH.search(str(key)) and o and not o.startswith("${") and "/" not in o:
                flagged.append(path)
                return "{{ op://Infrastructure/" + app + "/" + str(key) + " }}"
            # KEY=VALUE list entries
            m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)=(.*)$", o)
            if m and SECRETISH.search(m.group(1)) and m.group(2):
                flagged.append(path + ":" + m.group(1))
                return m.group(1) + "={{ op://Infrastructure/" + app + "/" + m.group(1) + " }}"
            return o.replace(domain, "{{ " + DOMAIN_REF + " }}")
        return o

    clean = walk(conf)
    text = yaml.safe_dump(clean, sort_keys=False, default_flow_style=False, width=200)
    if domain in text:
        sys.exit("domain still present after sanitising -- refusing to print")
    if flagged:
        print("# FLAGGED secret-looking values replaced with placeholder refs (verify!): " + ", ".join(flagged))
    print(text, end="")


if __name__ == "__main__":
    main()
