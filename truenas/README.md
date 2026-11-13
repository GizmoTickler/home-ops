# nas01 (TrueNAS) apps — GitOps

Every app on nas01 is a **TrueNAS custom app** defined here and deployed by one reconciler. No catalog apps,
no `include:` of files on disk, no hand-run deploy scripts. Merging to `main` is deploying.

```
truenas/
├── apps/<app>/
│   ├── compose.yaml   # required: the full compose (what TrueNAS runs), secrets as {{ op://... }} refs
│   ├── app.yaml       # optional: dirs / templated files / ini pins / health probe
│   └── *.tmpl         # optional: config templates rendered to host paths (app.yaml `files:`)
└── reconciler/
    ├── reconcile.py   # runs on nas01 (root cron, every 10 min)
    └── export_app.py  # one-off: export a live app as a git-safe compose.yaml
```

## How it works
`reconcile.py` (on nas01, cron job "apps-gitops reconcile") per run:
1. `git fetch` this repo (public, sparse checkout of `truenas/`) into the encrypted dataset.
2. **CI gate:** only a commit whose `truenas/` changes passed the **Lint nas01 apps** check (this repo's
   `truenas-apps` workflow: lint + reconciler unit tests) is checked out. Commits that don't touch `truenas/` need no
   check; a failing/pending/missing check holds deploys on the last good commit (it never rewinds). If the GitHub
   API can't be reached it fails closed: it keeps reconciling the last good commit and raises an alert.
3. For each `apps/<app>/`: render `{{ op://Vault/Item/[Section/]field }}` references through the local
   1Password Connect server, then compare with the live app (`app.config`), templated files and ini pins.
   Live state changed outside git since the last deploy (TrueNAS UI edits) is logged as **DRIFT** (+ metric) and
   re-applied from git.
4. Only if something differs: **preflight** (nothing running is touched unless all pass): `docker compose config`
   on the rendered compose, static-IP / host-port conflicts against what is live on nas01 (other apps' containers,
   host addresses, LAN ping/ARP for `mgmt`/`iot` macvlan IPs), pull of new images, and the app's `validate`
   command (Envoy: `envoy --mode validate` with the target image on the fully rendered config; rendered files
   live in a root-only temp dir that is deleted right after). Then write files / patch ini, `app.update` (or
   `app.create`), wait while TrueNAS reports `DEPLOYING` (up to `deploy_timeout`; `CRASHED`/`STOPPED` = failure)
   and for the `health` probe -- **automatic rollback** re-applies the exact previous compose with `app.update`
   (and restores files/ini) and verifies health again.
5. **Critical apps** (`critical: true`: onepassword-connect, pocket-id, envoy) deploy after all others, one at a
   time in `order`; if one fails the run stops there.
6. Apps on nas01 that are not in git are reported, never deleted. Catalog apps are refused (migrate by hand).
7. Housekeeping: backup retention, state dirs 0700, `reconcile.log` rotation.
8. Metrics → node-exporter textfile (`truenas_apps_gitops_*`), alerts in `apps/vmalert/rules.yaml.tmpl`
   (group `truenas-apps-gitops.rules`).

Config: `/mnt/flashstor/secure-scripts/apps-gitops/config.json` (root 0600, not in git). Optional keys (defaults):
`deploy_timeout` (600), `backup_dir` (`<state_dir>/backup`), `backup_retention_days` (14), `backup_keep_newest` (5),
`log_max_bytes` (5000000), `log_keep` (5), `preflight` (`{"enabled": true, "pull_images": true, "probe_lan": true}`),
`ci_gate` (`{"enabled": true, "check_name": "Lint nas01 apps", "api_url": "https://api.github.com",
"history_depth": 50, "timeout": 15, "token_ref": null}`; `token_ref` = optional `op://` ref to a GitHub token if the
unauthenticated 60 requests/h limit is ever hit). **Emergency (CI or GitHub down, fix needed now):** set
`"ci_gate": {"enabled": false}` in config.json, or run by hand with `--no-ci-gate`.

Secrets never touch git or the reconciler's logs: compose diffs are logged as changed key paths only and
templated files as "N changed lines" (never values, old or new). File writes into app-owned directories are
symlink/FIFO-safe (random O_EXCL temp + fchown on the descriptor); backups go to the root-only state dir. The Connect token and
config live in `/mnt/flashstor/secure-scripts/apps-gitops/config.json` (root 0600, not in git).

## Rules
- **Public repo:** no domain, no secrets. Use `{{ op://Infrastructure/cluster-config/SECRET_DOMAIN }}` for the
  domain and `{{ op://Infrastructure/<item>/<field> }}` for credentials.
- **Pin images** as `tag@sha256:digest`. Renovate (docker-compose manager) opens the bumps; digest/patch
  auto-merge, minor/major need review (`.renovaterc.json5`), because a merge deploys within ~10 minutes.
- **onepassword-connect must not contain op refs** — it is what resolves them (its session stays in the on-disk
  `.env`). Its `health` probe makes a bad Connect deploy roll back.
- Data lives on datasets under `/mnt/flashstor/apps/<app>-*` (Jellyfin still uses its former ix-volume paths).
- Changing an app: edit here, merge. Need it now? On nas01: `sudo python3
  /mnt/flashstor/secure-scripts/apps-gitops/home-ops/truenas/reconciler/reconcile.py --app <app>` (add `--dry-run` to see the diff).
  Merging to `main` also wakes the reconciler automatically once CI (`Lint nas01 apps`) passes: GitHub delivers
  signed `push`/`check_run` events through Cloudflare Tunnel → Envoy `:8443` (`gitops-webhook.<domain>/hooks/truenas-gitops` only)
  → `apps/webhook` (verifies `X-Hub-Signature-256` HMAC and writes 0-byte `/trigger/poke`) → host `systemd.path`
  (`POSTINIT`) running `reconcile.py --wait-lock 300`; the 10-minute cron catches drift and fallback runs.
- New app: write `apps/<app>/compose.yaml` (+ `app.yaml`), merge; the reconciler creates it.
  Adopting an existing app: `export_app.py <app>` on nas01, commit the output, dry-run must show "in sync".

## Compose standard (enforced by `truenas/reconciler/lint.py` in CI)
```yaml
# <App> — <one-line purpose>.
# Listens: <ip:port ...>        Data: <host paths>
# Managed by truenas/reconciler — edit here and merge (see truenas/README.md).
x-logging: &logging                       # same block in every file
  driver: json-file
  options: {max-size: 10m, max-file: "3"}

services:
  <svc>:
    image: repo/name:<version>@sha256:<digest>   # pinned; never latest/untagged
    restart: unless-stopped
    user: "uid:gid"                              # when the image supports non-root
    environment:                                 # map form, TZ always
      TZ: America/New_York
    ports:                                       # long form, always bound to a specific host_ip
      - {host_ip: 192.168.123.x, target: 80, published: 80, protocol: tcp}
    volumes:                                     # short form
      - /mnt/flashstor/apps/<app>-config:/config
    deploy:
      resources:
        limits: {cpus: "2", memory: 2048M}       # always both
    logging: *logging

x-portals:                                       # "Web UI" button in the TrueNAS Apps page
  - {name: Web UI, scheme: http, host: 192.168.123.x, port: 80, path: /}
```
Every `app.yaml` has a `health` probe and a `ui` block (`title`, `description`, `icon` https URL, `home`). TrueNAS
custom apps have no icon/title support of their own, so the reconciler writes `ui` (and the image tag as the app
version) into the app's TrueNAS metadata after each deploy. Icons: homarr-labs dashboard-icons via jsDelivr.

## Shared networks and ingress
`truenas/networks.yaml` defines two shared Docker networks, created by the reconciler before any app:
- `apps` (bridge 172.31.200.0/24): every web app joins it as `apps: {external: true}` and publishes no port;
  **Envoy** (`apps/envoy`, 192.168.123.156 on the LAN) terminates TLS with its wildcard cert and proxies to the
  app by service name (service names must be unique across apps). Envoy carries a network alias for each hostname
  it serves, so apps calling `https://<host>.<domain>` stay on the host. Pocket ID login for apps without their own
  OIDC: Envoy's native `envoy.filters.http.oauth2` HTTP filter enabled per route in `envoy.yaml.tmpl`.
- `mgmt` (macvlan on br0, 192.168.120.0/22): for apps that need their own LAN IP (Envoy, qBittorrent's gluetun,
  Home Assistant). Only one macvlan per subnet is possible, hence shared. Each IP needs an SC01 TrustSec sgt-map.
Public hostnames: `apps/cloudflared` (Cloudflare tunnel) -> Envoy's external listener (`:8443`, apps network only).

## app.yaml
```yaml
dirs:   [{path: /mnt/flashstor/apps/x-config, owner: "1000:1000", mode: "0755"}]
files:  [{template: x.conf.tmpl, dest: /mnt/flashstor/apps/x-config/x.conf, owner: "1000:1000", mode: "0600"}]
ini:    [{path: /mnt/flashstor/apps/x-config/x.conf, set: {"Key": "value"}}]   # existing keys only (verbatim, section-blind); a missing key fails the app
health: {tcp: "192.168.123.154:6789"}        # or {http: https://..., insecure: true}
                                             # macvlan apps: {tcp: "127.0.0.1:8080", netns: <service>} (host can't reach its macvlan IPs)
ui:     {title: NZBGet, description: "...", icon: https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/nzbget.png, home: https://nzbget.com}
deploy_timeout: 600                          # optional: s the app may stay DEPLOYING after a deploy
critical: true                               # optional: deploy last, one at a time, abort the run on failure
order: 20                                    # critical apps only, lower first (onepassword-connect = 0)
validate: {service: envoy, entrypoint: envoy, args: ["--mode", "validate", "-c", "/etc/envoy/envoy.yaml"]}
                                             # optional preflight: run the target image (no network, read-only)
                                             # with the service's volumes, rendered files swapped in
```
Tests: `python3 -m pytest truenas/reconciler/tests` (needs pyyaml, pytest, prometheus-client; CI runs them).
