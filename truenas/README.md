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

## app.yaml
```yaml
dirs:   [{path: /mnt/flashstor/apps/x-config, owner: "1000:1000", mode: "0755"}]
files:  [{template: x.conf.tmpl, dest: /mnt/flashstor/apps/x-config/x.conf, owner: "1000:1000", mode: "0600"}]
ini:    [{path: /mnt/flashstor/apps/x-config/x.conf, set: {'Section\Key': "value"}}]   # existing keys only
health: {tcp: "192.168.123.154:6789"}        # or {http: https://..., insecure: true}
```
