<div align="center">
<img src="https://github.com/user-attachments/assets/4a3122ae-706d-4e21-8130-f5a8c9483710" align="center" width="175px" height="175px"/>

### <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f680/512.gif" alt="🚀" width="16" height="16"> Home Operations Repository <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f6a7/512.gif" alt="🚧" width="16" height="16">

_Kubernetes cluster on Talos Linux VMs with Rook Ceph distributed storage, managed via GitOps_ <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f916/512.gif" alt="🤖" width="16" height="16">

</div>

<div align="center">

[![Discord](https://img.shields.io/discord/673534664354430999?style=for-the-badge&label&logo=discord&logoColor=white&color=blue)](https://discord.gg/home-operations)&nbsp;&nbsp;
[![Talos](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Ftalos_version&style=for-the-badge&logo=talos&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://talos.dev)&nbsp;&nbsp;
[![Kubernetes](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fkubernetes_version&style=for-the-badge&logo=kubernetes&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://kubernetes.io)&nbsp;&nbsp;
[![Flux](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fflux_version&style=for-the-badge&logo=flux&logoColor=white&color=blue&label=%20&cacheSeconds=60)](https://fluxcd.io)&nbsp;&nbsp;
[![Renovate](https://img.shields.io/github/actions/workflow/status/GizmoTickler/home-ops/renovate.yaml?branch=main&label=&logo=renovatebot&style=for-the-badge&color=blue)](https://github.com/GizmoTickler/home-ops/actions/workflows/renovate.yaml)

</div>

<div align="center">

[![Age](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_age_days&style=for-the-badge&label=Age&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Uptime](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_uptime_days&style=for-the-badge&label=Uptime&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Nodes](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_node_count&style=for-the-badge&label=Nodes&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Pods](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_pod_count&style=for-the-badge&label=Pods&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Containers](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcontainer_count&style=for-the-badge&label=Containers&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![CPU](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_cpu_usage&style=for-the-badge&label=CPU&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Memory](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_memory_usage&style=for-the-badge&label=Memory&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Storage](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fceph_storage_used&style=for-the-badge&label=Storage&cacheSeconds=60)](https://github.com/kashalls/kromgo)

</div>

<div align="center">

[![HelmReleases](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fhelmrelease_count&style=for-the-badge&label=HelmReleases&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![PVCs](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fpvc_count&style=for-the-badge&label=PVCs&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Alerts](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcluster_alert_count&style=for-the-badge&label=Alerts&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Flux Errors](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fflux_failing_count&style=for-the-badge&label=Flux%20Errors&cacheSeconds=60)](https://github.com/kashalls/kromgo)&nbsp;&nbsp;
[![Cert Expiry](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.pixel-forge.workers.dev%2Fcert_expiry_days&style=for-the-badge&label=Cert%20Expiry&cacheSeconds=60)](https://github.com/kashalls/kromgo)

</div>

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4a1/512.gif" alt="💡" width="20" height="20"> Overview

This repository contains the configuration for my homelab Kubernetes cluster built for learning, experimentation, and running self-hosted applications. The setup emphasizes Infrastructure as Code (IaC) and GitOps practices using [Talos Linux](https://www.talos.dev/), [Kubernetes](https://kubernetes.io/), [Flux](https://github.com/fluxcd/flux2), [Renovate](https://github.com/renovatebot/renovate), and [GitHub Actions](https://github.com/features/actions).

**Architecture**: The cluster runs on VMware ESXi VMs with [Rook Ceph](https://rook.io/) providing distributed storage using SSDs passed as pRDMs (physical Raw Device Mappings) directly to each Talos VM. Additional storage is provided by [scale-csi](https://github.com/gizmotickler/scale-csi) connecting to TrueNAS via iSCSI, NVMe-oF, and NFS over 4x10Gbps link aggregation.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f331/512.gif" alt="🌱" width="20" height="20"> Kubernetes

The Kubernetes cluster is deployed using [Talos Linux](https://www.talos.dev) on VMware ESXi VMs with distributed storage provided by [Rook Ceph](https://rook.io/) running on SSDs passed directly to each VM as pRDMs (physical Raw Device Mappings). This setup provides a production-like Kubernetes environment with true distributed storage and fault tolerance.

### Infrastructure Details

- **Hypervisor**: VMware ESXi with advanced virtualization features
- **Primary Storage**: Rook Ceph distributed storage using dedicated SSDs passed as pRDMs to each Talos VM
- **Secondary Storage**: [scale-csi](https://github.com/gizmotickler/scale-csi) connecting to TrueNAS Scale via iSCSI, NVMe-oF, and NFS
- **Network Infrastructure**: Cisco switch with 4x10Gbps LACP between TrueNAS and ESXi
- **Kubernetes Distribution**: Talos Linux (immutable, minimal, secure)
- **VM Configuration**: 3 control plane nodes, each with 16 vCPUs and 48GB RAM
- **Storage Strategy**: Multiple storage tiers per VM:
  - **Boot Disk**: Virtual disk for Talos OS
  - **Ceph OSD**: Dedicated SSD passed as pRDM for Rook Ceph distributed storage
  - **Local Storage**: OpenEBS hostPath for high-performance local workloads
- **Networking**: Cilium CNI with eBPF, Gateway API, and L2/BGP announcements
- **Ingress**: Cilium Gateway API with per-application LoadBalancer services
- **DNS**: external-dns for both Cloudflare and Unifi local DNS management

### Core Components

- [actions-runner-controller](https://github.com/actions/actions-runner-controller): Self-hosted GitHub runners for CI/CD workflows.
- [cert-manager](https://github.com/cert-manager/cert-manager): Automated TLS certificate management with Google Trust Services.
- [cilium](https://github.com/cilium/cilium): eBPF-based networking, security, and Gateway API implementation with L2 announcements.
- [cloudflared](https://github.com/cloudflare/cloudflared): Secure tunnels to Cloudflare for external access via Cloudflare Tunnel.
- [external-dns](https://github.com/kubernetes-sigs/external-dns): Automated DNS record management with Cloudflare and Unifi API integration.
- [external-secrets](https://github.com/external-secrets/external-secrets): Kubernetes External Secrets Operator with 1Password Connect integration.
- [flux](https://github.com/fluxcd/flux2): GitOps continuous delivery for Kubernetes with SOPS decryption support.
- [openebs](https://github.com/openebs/openebs): Local persistent volume provisioner for hostPath storage.
- [rook-ceph](https://github.com/rook/rook): Primary distributed storage using Ceph on dedicated SSDs passed as pRDMs.
- [scale-csi](https://github.com/gizmotickler/scale-csi): TrueNAS Scale CSI driver for iSCSI, NVMe-oF, and NFS with metrics and Grafana dashboards.
- [sops](https://github.com/getsops/sops): Managed secrets for Kubernetes using age encryption, committed to Git.
- [spegel](https://github.com/spegel-org/spegel): Stateless cluster local OCI registry mirror for improved image pull performance.
- [system-upgrade-controller](https://github.com/rancher/system-upgrade-controller): Automated Kubernetes and Talos Linux upgrades.
- [volsync](https://github.com/backube/volsync): Backup and recovery of persistent volume claims with Kopia.

### GitOps

[Flux](https://github.com/fluxcd/flux2) provides GitOps continuous delivery, watching the [kubernetes](./kubernetes/) folder and applying changes based on Git repository state. The setup includes:

- **SOPS Integration**: Automatic decryption of secrets using age encryption
- **Dependency Management**: HelmReleases and Kustomizations with explicit dependencies
- **Multi-tenancy**: Namespace isolation with proper RBAC
- **Webhook Integration**: GitHub webhook receiver for immediate sync on push

The workflow recursively searches the `kubernetes/apps` folder for `kustomization.yaml` files, which typically contain namespace resources and Flux Kustomizations (`ks.yaml`). Each Kustomization manages HelmReleases or other Kubernetes resources for applications.

[Renovate](https://github.com/renovatebot/renovate) provides automated dependency management across the entire repository, creating pull requests for updates to:
- Container images with digest pinning
- Helm chart versions
- Kubernetes manifests
- GitHub Actions workflows

### Repository Structure

This Git repository is organized for GitOps workflows and infrastructure management:

```sh
📁 home-ops
├── 📁 bootstrap          # Initial cluster bootstrap resources
├── 📁 kubernetes
│   ├── 📁 apps          # Application deployments by namespace
│   │   ├── 📁 actions-runner-system # Self-hosted GitHub runners
│   │   ├── 📁 automation     # Workflow automation (n8n)
│   │   ├── 📁 cert-manager   # Certificate management
│   │   ├── 📁 downloads      # Media acquisition stack
│   │   ├── 📁 external-secrets # Secret management
│   │   ├── 📁 flux-system    # Flux controllers
│   │   ├── 📁 kube-system    # Core Kubernetes components
│   │   ├── 📁 media          # Media serving applications
│   │   ├── 📁 network        # Networking applications
│   │   ├── 📁 observability  # Monitoring and logging
│   │   ├── 📁 openebs-system # Local storage provisioner
│   │   ├── 📁 rook-ceph      # Distributed Ceph storage
│   │   ├── 📁 scale-csi      # TrueNAS Scale iSCSI/NVMe-oF/NFS storage
│   │   ├── 📁 self-hosted    # Productivity and tools
│   │   ├── 📁 system-upgrade # Automated upgrades
│   │   └── 📁 volsync-system # Volume backup and recovery
│   ├── 📁 components    # Reusable Kustomize components
│   │   ├── 📁 alerts         # AlertManager configurations
│   │   ├── 📁 cluster-secret # Cluster-wide secrets
│   │   ├── 📁 nfs-scaler     # NFS availability scaling
│   │   └── 📁 volsync-direct # Direct volume backup/restore
│   └── 📁 flux          # Flux system configuration
├── 📁 cmd               # HomeOps CLI source code
│   └── 📁 homeops-cli   # Go-based automation tool
├── 📁 scripts           # Automation and utility scripts
└── 📁 talos             # Talos Linux configuration templates
```

### Flux Workflow

This is a high-level look how Flux deploys my applications with dependencies. In most cases a `HelmRelease` will depend on other `HelmRelease`'s, in other cases a `Kustomization` will depend on other `Kustomization`'s, and in rare situations an app can depend on a `HelmRelease` and a `Kustomization`. The example below shows that applications with persistent storage depend on Rook Ceph being installed and healthy.

```mermaid
graph TD
    A>Kustomization: rook-ceph-cluster] -->|Creates| B[CephCluster: rook-ceph]
    C>Kustomization: volsync] -->|Creates| D[HelmRelease: volsync]
    E>Kustomization: atuin] -->|Creates| F(HelmRelease: atuin)
    F>HelmRelease: atuin] -->|Depends on| B>CephCluster: rook-ceph]
    F>HelmRelease: atuin] -->|Backed up by| D>HelmRelease: volsync]
```

### Automation & Tooling

The repository includes comprehensive automation for cluster management through a custom Go-based CLI:

#### HomeOps CLI (`cmd/homeops-cli`)

A purpose-built Go application that provides complete infrastructure automation:

**Core Capabilities:**
- **Bootstrap**: Complete cluster initialization with preflight checks and 1Password integration
- **Talos Operations**: Node configuration, VM deployment, ISO generation, and Kubernetes upgrades
- **VM Management**: ESXi VM creation with custom Talos ISOs and dedicated storage controllers
- **Volume Operations**: VolSync-based backup and restore with Kopia integration
- **Kubernetes Management**: Deployment restarts, PVC browsing, and maintenance operations

**Key Commands:**
```bash
# Bootstrap entire cluster
./homeops-cli bootstrap

# Talos node operations
./homeops-cli talos apply-node --ip 192.168.122.10
./homeops-cli talos deploy-vm --name test_node --generate-iso
./homeops-cli talos upgrade-k8s

# Volume backup/restore
./homeops-cli volsync snapshot --pvc data-pvc --namespace default
./homeops-cli volsync restore --pvc data-pvc --namespace default
```

**Supporting Tools:**
- **Template Rendering**: Embedded Jinja2 templates with [minijinja](https://github.com/mitsuhiko/minijinja)
- **Secret Injection**: [1Password CLI](https://developer.1password.com/docs/cli/) integration for secure secret management
- **Environment Management**: [mise](https://github.com/jdx/mise) for tool and environment variable management
- **Configuration Validation**: Pre-commit hooks with kubeconform and YAML linting
- **CI/CD**: GitHub Actions for automated testing, schema validation, and deployment

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f636_200d_1f32b_fe0f/512.gif" alt="😶" width="20" height="20"> Cloud Dependencies

While most infrastructure and workloads are self-hosted, I rely on cloud services for critical functions to avoid chicken/egg scenarios and ensure availability of essential services regardless of cluster state. This approach balances self-hosting benefits with operational reliability.

Alternative solutions would involve running a separate cloud-hosted Kubernetes cluster for critical services like [Vault](https://www.vaultproject.io/), [Vaultwarden](https://github.com/dani-garcia/vaultwarden), or [ntfy](https://ntfy.sh/), but the operational overhead and costs would likely exceed the current cloud service expenses.

| Service                                         | Use                                                               | Cost           |
|-------------------------------------------------|-------------------------------------------------------------------|----------------|
| [1Password](https://1password.com/)             | Secrets with [External Secrets](https://external-secrets.io/)     | ~$65/yr        |
| [Cloudflare](https://www.cloudflare.com/)       | Domain, DNS, and tunnel services                                 | ~$30/yr        |
| [Google Workspace](https://workspace.google.com/) | Email hosting and productivity suite                           | ~$72/yr        |
| [GitHub](https://github.com/)                   | Repository hosting and CI/CD with Actions                        | Free           |
| [iLert](https://www.ilert.com/)                 | Incident management and alerting                                  | Free (tier)    |
| [Pushover](https://pushover.net/)               | Mobile notifications for alerts                                   | $5 OTP         |
|                                                 |                                                                   | Total: ~$14/mo |

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f30e/512.gif" alt="🌎" width="20" height="20"> DNS & Networking

The cluster implements a sophisticated networking architecture using Cilium and Gateway API:

### External Access
- **Cloudflare Tunnel**: Secure external access via `cloudflared` without port forwarding
- **External DNS (Cloudflare)**: Automatic DNS record management in Cloudflare for public services
- **Gateway API**: Cilium-based ingress with dedicated LoadBalancer IPs per application

### Internal Resolution
- **External DNS (Unifi)**: Additional external-dns deployment leveraging Unifi local API for internal DNS record updates
- **CoreDNS**: Kubernetes cluster DNS with custom configurations
- **Cilium Announcements**: Cilium L2/BGP announcements for LoadBalancer IP allocation

### Network Architecture
- **CNI**: Cilium with eBPF datapath for high-performance networking
- **Load Balancing**: Maglev algorithm with DSR (Direct Server Return) mode
- **IP Management**: Kubernetes IPAM with native routing (10.42.0.0/16)
- **Gateway IPs**: Dedicated IP range (192.168.123.101-149) for application access

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4f1/512.gif" alt="📱" width="20" height="20"> Applications

The cluster hosts a variety of self-hosted applications organized by namespace and function:

### Productivity & Tools (self-hosted namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Atuin](https://github.com/atuinsh/atuin) | Shell history sync | `sh.${SECRET_DOMAIN}` |
| [OCIS](https://github.com/owncloud/ocis) | Personal file sync & sharing | `ocis.${SECRET_DOMAIN}` |
| [The Lounge](https://github.com/thelounge/thelounge) | Persistent IRC/web chat client | `thelounge.${SECRET_DOMAIN}` |

### Content & Finance (self-hosted namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Actual](https://github.com/actualbudget/actual) | Personal budgeting | `actual.${SECRET_DOMAIN}` |
| [FreshRSS](https://github.com/FreshRSS/FreshRSS) | RSS feed aggregator | `feeds.${SECRET_DOMAIN}` |
| [Karakeep](https://github.com/karakeep-app/karakeep) | Bookmarking & read-it-later capture | `karakeep.${SECRET_DOMAIN}` |

All self-hosted apps now share the `self-hosted` namespace so VolSync movers and Kopia ownership stay aligned (snapshots live under identities like `app@self-hosted:/data`).

### Media & Requests (media namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Jellyseerr](https://github.com/Fallenbagel/jellyseerr) | Media discovery & request management | `requests.${SECRET_DOMAIN}` |

Media workloads live in the `media` namespace so VolSync and Kopia identities follow `app@media:/data` for consistent restores.

### Downloads & Indexers (downloads namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Autobrr](https://github.com/autobrr/autobrr) | Real-time announce filtering & actions | `autobrr.${SECRET_DOMAIN}` |
| [Bazarr](https://github.com/morpheus65535/bazarr) | Subtitle management for Radarr/Sonarr libraries | `bazarr.${SECRET_DOMAIN}` |
| [Cross-Seed](https://github.com/cross-seed/cross-seed) | Torrent cross-seeding suggestions | Internal only |
| [NZBGet](https://github.com/nzbgetcom/nzbget) | Usenet downloader | `nzbget.${SECRET_DOMAIN}` |
| [Pinchflat](https://github.com/kieranjeglin/pinchflat) | Long-form video & podcast archiving | `pinchflat.${SECRET_DOMAIN}` |
| [Prowlarr](https://github.com/Prowlarr/Prowlarr) | Indexer proxy & search aggregator | `prowlarr.${SECRET_DOMAIN}` |
| [qBittorrent](https://github.com/qbittorrent/qBittorrent) | VPN-protected torrent client with VueTorrent UI | `qbittorrent.${SECRET_DOMAIN}` |
| [Qui](https://github.com/autobrr/qui) | Autobrr queue monitor & dashboard | `qui.${SECRET_DOMAIN}` |
| [Radarr](https://github.com/Radarr/Radarr) | Movie library automation | `radarr.${SECRET_DOMAIN}` |
| [Recyclarr](https://github.com/Recyclarr/Recyclarr) | Radarr/Sonarr config synchronisation | Internal only |
| [TQM](https://github.com/home-operations/tqm) | Automated qBittorrent retag/cleanup jobs | Internal only |
| [Sonarr](https://github.com/Sonarr/Sonarr) | TV library automation | `sonarr.${SECRET_DOMAIN}` |

The entire download stack now lives in the `downloads` namespace so VolSync movers and Kopia ownership stay aligned (`app@downloads:/data`) and restores remain consistent.

### Automation & Workflows (automation namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [n8n](https://github.com/n8n-io/n8n) | Workflow automation & integrations | `n8n.${SECRET_DOMAIN}` |

Automation workloads run in the `automation` namespace so VolSync restores and Kopia ownership continue to match `app@automation:/data`.

### Observability Stack (observability namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Grafana](https://github.com/grafana/grafana) | Metrics visualization and dashboards | `grafana.${SECRET_DOMAIN}` |
| [Victoria-Metrics](https://github.com/VictoriaMetrics/VictoriaMetrics) | Metrics collection and alerting | `metrics.${SECRET_DOMAIN}` |
| [Victoria-Metrics-Logs](https://github.com/VictoriaMetrics/VictoriaLogs) | Log aggregation and querying | `logs.${SECRET_DOMAIN}` |
| [Fluent-Bit](https://github.com/fluent/fluent-bit) | Telemetry data collection | Internal only |
| [Alertmanager](https://github.com/prometheus/alertmanager) | Alert routing and management | `alertmanager.${SECRET_DOMAIN}` |
| [Blackbox Exporter](https://github.com/prometheus/blackbox_exporter) | Endpoint monitoring | Internal only |
| [Node Exporter](https://github.com/prometheus/node_exporter) | System metrics collection | Internal only |
| [Kube State Metrics](https://github.com/kubernetes/kube-state-metrics) | Kubernetes metrics | Internal only |
| [KEDA](https://github.com/kedacore/keda) | Kubernetes event-driven autoscaling | Internal only |
| [Kromgo](https://github.com/kashalls/kromgo) | Kubernetes resource metrics API | Internal only |
| [Silence Operator](https://github.com/giantswarm/silence-operator) | Automated alert silencing | Internal only |

### Storage & Infrastructure

| Application | Purpose | Access |
|-------------|---------|--------|
| [Rook Ceph](https://github.com/rook/rook) | Primary distributed storage (block + filesystem) | Internal only |
| [scale-csi](https://github.com/gizmotickler/scale-csi) | TrueNAS Scale iSCSI/NVMe-oF/NFS storage | Internal only |
| [OpenEBS](https://github.com/openebs/openebs) | Local persistent volume provisioner | Internal only |

All applications use Cilium Gateway API for ingress with automatic TLS certificates from Google Trust Services via cert-manager.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4be/512.gif" alt="💾" width="20" height="20"> Storage Architecture

The cluster uses a multi-tier storage architecture with Rook Ceph as the primary distributed storage layer:

### Storage Tiers

| Tier | Provider | StorageClass | Use Case |
|------|----------|--------------|----------|
| **Distributed Block** | Rook Ceph | `ceph-block` (default) | Application PVCs with replication |
| **Distributed Filesystem** | Rook Ceph | `ceph-filesystem` | Shared storage across pods |
| **External Block** | scale-csi | `scale-iscsi`, `scale-nvmeof` | TrueNAS iSCSI/NVMe-oF volumes |
| **External NFS** | scale-csi | `scale-nfs` | Media files, shared data |
| **Local Storage** | OpenEBS | `openebs-hostpath` | High-performance local workloads |
| **Backup** | VolSync + Kopia | — | Automated PVC backup and restore |

### Rook Ceph Configuration

[Rook Ceph](https://rook.io/) provides the primary distributed storage using SSDs passed as pRDMs (physical Raw Device Mappings) directly to each Talos VM on ESXi:

- **Ceph Version**: v19.2.3 (Squid)
- **OSD Configuration**: Dedicated SSD per node passed as pRDM for direct hardware access
- **Replication**: 3-way replication across nodes for fault tolerance
- **Pools**:
  - `ceph-blockpool`: RBD block storage for application PVCs
  - `ceph-filesystem`: CephFS for shared filesystem access
- **Features**:
  - Automatic PG autoscaling
  - Disk failure prediction (local mode)
  - TRIM/discard support enabled
  - Integrated Prometheus metrics and Grafana dashboards

### scale-csi Configuration

The [scale-csi](https://github.com/gizmotickler/scale-csi) driver provides additional storage from TrueNAS Scale:

- **Protocols**: iSCSI, NVMe-oF, and NFS
- **Network**: 4x10Gbps LACP between TrueNAS and ESXi
- **StorageClasses**:
  - `scale-iscsi`: iSCSI block volumes
  - `scale-nvmeof`: NVMe over Fabrics for high-performance workloads
  - `scale-nfs`: NFSv4 shared volumes
- **Features**:
  - Volume snapshots via CSI snapshot controller
  - Metrics exporting with Grafana dashboards
  - Node-level metrics scraping for performance monitoring

### Backup Strategy

[VolSync](https://github.com/backube/volsync) with [Kopia](https://github.com/kopia/kopia) provides automated backup:

- **ReplicationSource**: Scheduled backups of PVCs to S3-compatible storage
- **ReplicationDestination**: Point-in-time recovery with dataSource references
- **Identity Alignment**: Namespace-based identity (`app@namespace:/data`) for consistent restores

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/2699_fe0f/512.gif" alt="⚙" width="20" height="20"> Hardware

### Physical Infrastructure

| Component                   | Specifications                                      | Function                          |
|-----------------------------|-----------------------------------------------------|-----------------------------------|
| **ESXi Host**               | VMware ESXi Hypervisor                             | VM compute & management           |
| ├─ **CPU**                  | 2x Intel Xeon E5-2640 v4 @ 2.40GHz (20 cores)     | VM compute resources              |
| ├─ **Memory**               | 256GB RAM                                           | VM memory allocation              |
| ├─ **Network**              | 4x 10GbE Intel X540 NICs (LACP to Cisco switch)   | High-speed VM networking          |
| └─ **Storage**              | 2x 500GB SATA SSD                                  | Boot and local datastore          |
| **Storage Server**          | TrueNAS Scale                                       | iSCSI & NFS storage provider      |
| ├─ **CPU**                  | 2x Intel Xeon E5-2630 v4 @ 2.20GHz (20 cores)     | Storage processing                |
| ├─ **Memory**               | 384GB RAM                                           | ARC cache and services            |
| ├─ **Network**              | 4x 10GbE Intel X540 NICs (LACP to Cisco switch)   | Storage network (40Gbps total)   |
| └─ **Protocols**            | iSCSI (block) + NFS 4.1 (file)                     | Kubernetes storage access         |
| **Network Switch**          | Cisco Switch                                        | Infrastructure interconnect       |
| └─ **Configuration**        | 4x10Gbps LACP between TrueNAS and ESXi            | High-bandwidth storage path       |

### Storage Architecture

| Storage Tier                | Hardware                                            | Purpose                           |
|-----------------------------|-----------------------------------------------------|-----------------------------------|
| **TrueNAS Primary Pool**    | 3x RAIDZ vdevs (4 disks each, 3.8TB SSDs)         | iSCSI volumes + NFS exports       |
| **SLOG (Intent Log)**       | 2x 800GB NVMe (mirrored)                           | Synchronous write acceleration    |
| **Special Metadata vdev**   | 2x 1.5TB NVMe (mirrored)                           | Metadata & small block storage    |

### Virtual Machine Configuration

| VM Role                     | Count | vCPU | Memory | Storage Layout                                              | OS            |
|-----------------------------|-------|------|--------|-------------------------------------------------------------|---------------|
| **Kubernetes Control Plane** | 3     | 16     | 48GB   | Boot vdisk + SSD pRDM (Ceph OSD) + Local vdisk (OpenEBS)   | Talos Linux   |

**Storage Details**:
- **Boot Disk**: Virtual disk for Talos OS
- **Ceph OSD**: Dedicated SSD passed as pRDM (physical Raw Device Mapping) for Rook Ceph distributed storage
- **Local Storage**: Virtual disk for OpenEBS hostPath high-performance workloads
- **External Storage**: scale-csi provides TrueNAS iSCSI/NVMe-oF/NFS for additional capacity

**Total VM Resources**: 48 vCPUs, 144GB RAM allocated from the 40-core, 256GB host system.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4da/512.gif" alt="📚" width="20" height="20"> Learning & Credits

This homelab serves as a continuous learning platform for cloud-native technologies, GitOps practices, and infrastructure automation. The setup provides hands-on experience with production-grade tools and practices in a controlled environment.

**Special thanks to [onedr0p](https://github.com/onedr0p)** and the [k8s-at-home](https://github.com/k8s-at-home) community. This repository was heavily inspired by onedr0p's [home-ops](https://github.com/onedr0p/home-ops) repository, which served as an excellent learning resource and foundation for understanding GitOps workflows and Kubernetes cluster management.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f64f/512.gif" alt="🙏" width="20" height="20"> Gratitude and Thanks

Thanks to all the people who donate their time to the [Home Operations](https://discord.gg/home-operations) Discord community. Be sure to check out [kubesearch.dev](https://kubesearch.dev/) for ideas on how to deploy applications or get ideas on what you could deploy.
