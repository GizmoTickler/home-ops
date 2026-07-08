# README Badge Examples

> **Note:** The current README uses the worker's native **stat-tile panels**
> (`/stack_panel`, `/health_panel`, `/usage_panel`, `/gitops_panel`,
> `/network_status`) — uniform 832px rows of GitHub-Primer-styled tiles with
> light/dark theming via `prefers-color-scheme`. The shields.io examples below
> are a legacy alternative that consumes the worker's `?json` endpoints.

After deploying your Cloudflare Worker, use these badge examples in your README.md.

**Replace `<WORKER_URL>` with your actual worker URL** (e.g., `kromgo-proxy.username.workers.dev`)

## Top Row - Large Badges (for-the-badge style)

```markdown
<div align="center">

[![Discord](https://img.shields.io/discord/673534664354430999?style=for-the-badge&label&logo=discord&logoColor=white&color=blue)](https://discord.gg/home-operations)&nbsp;&nbsp;
[![Flatcar](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Ftalos_version&style=for-the-badge&logo=flatcar&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://flatcar.org)&nbsp;&nbsp;
[![Kubernetes](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fkubernetes_version&style=for-the-badge&logo=kubernetes&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://kubernetes.io)&nbsp;&nbsp;
[![Flux](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fflux_version&style=for-the-badge&logo=flux&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://fluxcd.io)&nbsp;&nbsp;
[![Renovate](https://img.shields.io/github/actions/workflow/status/GizmoTickler/home-ops/renovate.yaml?branch=main&label=&logo=renovatebot&style=for-the-badge&color=blue)](https://github.com/GizmoTickler/home-ops/actions/workflows/renovate.yaml)

</div>
```

## Bottom Row - Small Badges (flat-square style)

```markdown
<div align="center">

[![Age-Days](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_age_days&style=flat-square&label=Age&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Uptime-Days](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_uptime_days&style=flat-square&label=Uptime&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Node-Count](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_node_count&style=flat-square&label=Nodes&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Pod-Count](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_pod_count&style=flat-square&label=Pods&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![CPU-Usage](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_cpu_usage&style=flat-square&label=CPU&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Memory-Usage](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_memory_usage&style=flat-square&label=Memory&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Alerts](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_alert_count&style=flat-square&label=Alerts&cacheSeconds=60)](https://github.com/kashalls/kromgo)

</div>
```

## URL Encoding Reference

Shields.io requires URLs to be URL-encoded. Here's the pattern:

**Original URL:**
```
https://kromgo-proxy.username.workers.dev/talos_version
```

**URL-encoded version:**
```
https%3A%2F%2Fkromgo-proxy.username.workers.dev%2Ftalos_version
```

**Encoding rules:**
- `:` → `%3A`
- `/` → `%2F`
- `.` → `.` (no encoding needed)
- `-` → `-` (no encoding needed)

## Quick URL Encoder

Use this bash command to encode your worker URL:

```bash
WORKER_URL="kromgo-proxy.username.workers.dev"
echo "https%3A%2F%2F${WORKER_URL}%2Ftalos_version"
```

Or use an online tool: https://www.urlencoder.org/

## Testing Your Badges

Before adding to README, test each badge URL:

```bash
# Test that kromgo returns data
curl https://kromgo-proxy.username.workers.dev/talos_version

# Test shields.io rendering (paste in browser)
https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.username.workers.dev%2Ftalos_version
```

## Badge Customization

### Colors

Change the `color` parameter:
- `blue` - Default blue
- `green` - Success/healthy
- `red` - Error/alert
- `orange` - Warning
- `yellow` - Caution
- `brightgreen` - Very positive
- `lightgrey` - Neutral

### Styles

Change the `style` parameter:
- `flat-square` - Flat with square corners (compact)
- `for-the-badge` - Large bold badges
- `flat` - Flat with rounded corners
- `plastic` - Shiny plastic look
- `social` - Social media style

### Logos

Add logo with `logo` parameter:
- `flatcar` - Flatcar Container Linux (current cluster OS)
- `talos` - Talos Linux (legacy)
- `kubernetes` - Kubernetes
- `flux` - Flux
- `prometheus` - Prometheus
- `grafana` - Grafana
- Full list: https://simpleicons.org/

## Example Variations

### With custom color based on metric:

```markdown
[![CPU](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_cpu_usage&style=flat-square&label=CPU&cacheSeconds=60)](https://github.com/kashalls/kromgo)
```

The color will be automatically set by kromgo based on your config.yaml color rules.

### With Prometheus logo:

```markdown
[![Alerts](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_alert_count&style=for-the-badge&logo=prometheus&logoColor=white&label=Alerts&cacheSeconds=60)](https://github.com/kashalls/kromgo)
```

## Full Example for README.md

Here's the complete section you can add after deploying:

```markdown
<div align="center">
<img src="https://github.com/user-attachments/assets/4a3122ae-706d-4e21-8130-f5a8c9483710" align="center" width="195px" height="195px"/>

### 🚀 Home Operations Repository 🚧

_Kubernetes cluster running on Proxmox VMs, managed with Flatcar Container Linux + kubeadm, Flux, and GitOps_ 🤖

<!-- Note: the version badge endpoint is still served at /talos_version (kromgo metric name retained), but renders the Flatcar version + logo. -->


</div>

<div align="center">

[![Flatcar](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Ftalos_version&style=for-the-badge&logo=flatcar&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://www.flatcar.org/)&nbsp;&nbsp;
[![Kubernetes](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fkubernetes_version&style=for-the-badge&logo=kubernetes&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://kubernetes.io/)&nbsp;&nbsp;
[![Flux](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fflux_version&style=for-the-badge&logo=flux&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://fluxcd.io/)&nbsp;&nbsp;
[![Renovate](https://img.shields.io/badge/Renovate-enabled-blue?style=for-the-badge&logo=renovatebot&logoColor=white)](https://renovatebot.com/)

</div>

<div align="center">

[![Age-Days](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_age_days&style=flat-square&label=Age&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Uptime-Days](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_uptime_days&style=flat-square&label=Uptime&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Node-Count](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_node_count&style=flat-square&label=Nodes&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Pod-Count](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_pod_count&style=flat-square&label=Pods&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![CPU-Usage](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_cpu_usage&style=flat-square&label=CPU&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Memory-Usage](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_memory_usage&style=flat-square&label=Memory&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Alerts](https://img.shields.io/endpoint?url=https%3A%2F%2F<WORKER_URL>%2Fcluster_alert_count&style=flat-square&label=Alerts&cacheSeconds=60)](https://github.com/kashalls/kromgo)

</div>
```

Remember to replace `<WORKER_URL>` with your actual worker URL!
