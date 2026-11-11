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
2. For each `apps/<app>/`: render `{{ op://Vault/Item/[Section/]field }}` references through the local
   1Password Connect server, then compare with the live app (`app.config`), templated files and ini pins.
3. Only if something differs: write files / patch ini, `app.update` (or `app.create`), wait for `RUNNING` and the
   optional `health` probe — **automatic rollback** to the previous compose/files if either fails.
4. Apps on nas01 that are not in git are reported, never deleted. Catalog apps are refused (migrate by hand).
5. Metrics → node-exporter textfile (`truenas_apps_gitops_*`), alerts in
   `kubernetes/apps/observability/victoria-metrics/app/vmrule-truenas-apps.yaml`.

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
  /mnt/flashstor/secure-scripts/apps-gitops/bin/reconcile.py --app <app>` (add `--dry-run` to see the diff).
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

## app.yaml
```yaml
dirs:   [{path: /mnt/flashstor/apps/x-config, owner: "1000:1000", mode: "0755"}]
files:  [{template: x.conf.tmpl, dest: /mnt/flashstor/apps/x-config/x.conf, owner: "1000:1000", mode: "0600"}]
ini:    [{path: /mnt/flashstor/apps/x-config/x.conf, set: {'Section\Key': "value"}}]   # existing keys only
health: {tcp: "192.168.123.154:6789"}        # or {http: https://..., insecure: true}
ui:     {title: NZBGet, description: "...", icon: https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/nzbget.png, home: https://nzbget.com}
```
