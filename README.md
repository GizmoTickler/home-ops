<div align="center">
<img src="https://github.com/user-attachments/assets/4a3122ae-706d-4e21-8130-f5a8c9483710" align="center" width="195px" height="195px"/>

### <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f680/512.gif" alt="🚀" width="16" height="16"> Home Operations Repository <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f6a7/512.gif" alt="🚧" width="16" height="16">

_Kubernetes cluster running on TrueNAS Scale VMs, managed with Talos, Flux, and GitOps_ <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f916/512.gif" alt="🤖" width="16" height="16">

</div>

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4a1/512.gif" alt="💡" width="20" height="20"> Overview

This repository contains the configuration for my homelab Kubernetes cluster built for learning, experimentation, and running self-hosted applications. The setup emphasizes Infrastructure as Code (IaC) and GitOps practices using [Talos Linux](https://www.talos.dev/), [Kubernetes](https://kubernetes.io/), [Flux](https://github.com/fluxcd/flux2), [Renovate](https://github.com/renovatebot/renovate), and [GitHub Actions](https://github.com/features/actions).

**Architecture**: The cluster runs on libvirt VMs hosted on TrueNAS Scale, providing a flexible virtualized environment that balances learning opportunities with production-like operations.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f331/512.gif" alt="🌱" width="20" height="20"> Kubernetes

The Kubernetes cluster is deployed using [Talos Linux](https://www.talos.dev) on libvirt VMs running on TrueNAS Scale. This setup provides a production-like Kubernetes environment while maintaining the flexibility to experiment and learn. The cluster features a hyper-converged architecture where compute and storage are co-located on the same nodes.

### Infrastructure Details

- **Host OS**: TrueNAS Scale with libvirt/QEMU virtualization
- **Kubernetes Distribution**: Talos Linux v1.10.6 (immutable, minimal, secure)
- **Kubernetes Version**: v1.33.3
- **VM Configuration**: 3 control plane nodes, each with 8 vCPUs and 32GB RAM
- **Storage Strategy**: Multi-tier approach using local NVMe for performance and Rook Ceph for distributed resilience
- **Networking**: Cilium v1.18.0 CNI with eBPF, Gateway API, and L2 announcements
- **Ingress**: Cilium Gateway API with per-application LoadBalancer services
- **DNS**: k8s-gateway for internal resolution, external-dns for Cloudflare integration

### Core Components

- [actions-runner-controller](https://github.com/actions/actions-runner-controller): Self-hosted GitHub runners for CI/CD workflows.
- [cert-manager](https://github.com/cert-manager/cert-manager): Automated TLS certificate management with Google Trust Services.
- [cilium](https://github.com/cilium/cilium): eBPF-based networking, security, and Gateway API implementation with L2 announcements.
- [cloudflared](https://github.com/cloudflare/cloudflared): Secure tunnels to Cloudflare for external access via Cloudflare Tunnel.
- [external-dns](https://github.com/kubernetes-sigs/external-dns): Automated DNS record management with Cloudflare integration.
- [external-secrets](https://github.com/external-secrets/external-secrets): Kubernetes External Secrets Operator with 1Password Connect integration.
- [flux](https://github.com/fluxcd/flux2): GitOps continuous delivery for Kubernetes with SOPS decryption support.
- [k8s-gateway](https://github.com/ori-edge/k8s_gateway): Internal DNS resolution for cluster services and HTTPRoutes.
- [openebs](https://github.com/openebs/openebs): Local persistent volume provisioner for hostPath storage.
- [rook-ceph](https://github.com/rook/rook): Distributed block storage with Ceph for persistent storage and data resilience.
- [sops](https://github.com/getsops/sops): Managed secrets for Kubernetes using age encryption, committed to Git.
- [spegel](https://github.com/spegel-org/spegel): Stateless cluster local OCI registry mirror for improved image pull performance.
- [system-upgrade-controller](https://github.com/rancher/system-upgrade-controller): Automated Kubernetes and Talos Linux upgrades.
- [volsync](https://github.com/backube/volsync): Backup and recovery of persistent volume claims with Restic.

### GitOps

[Flux](https://github.com/fluxcd/flux2) v2.6.4 provides GitOps continuous delivery, watching the [kubernetes](./kubernetes/) folder and applying changes based on Git repository state. The setup includes:

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
│   │   ├── 📁 default   # Media stack and productivity apps
│   │   ├── 📁 external-secrets # Secret management
│   │   ├── 📁 flux-system      # Flux controllers
│   │   ├── 📁 kube-system      # Core Kubernetes components
│   │   ├── 📁 network          # Networking applications
│   │   ├── 📁 observability    # Monitoring and logging
│   │   ├── 📁 openebs-system   # Local storage provisioner
│   │   ├── 📁 rook-ceph        # Distributed storage
│   │   └── 📁 system-upgrade   # Automated upgrades
│   ├── 📁 components    # Reusable Kustomize components
│   │   ├── 📁 common    # Shared configurations
│   │   ├── 📁 gateway   # Gateway API templates
│   │   ├── 📁 keda      # Autoscaling components
│   │   └── 📁 volsync   # Backup and recovery
│   └── 📁 flux          # Flux system configuration
├── 📁 scripts           # Automation and utility scripts
├── 📁 talos             # Talos Linux configuration templates
└── 📁 .taskfiles        # Task automation definitions
```

### Flux Workflow

This is a high-level look how Flux deploys my applications with dependencies. In most cases a `HelmRelease` will depend on other `HelmRelease`'s, in other cases a `Kustomization` will depend on other `Kustomization`'s, and in rare situations an app can depend on a `HelmRelease` and a `Kustomization`. The example below shows that `atuin` won't be deployed or upgrade until the `rook-ceph-cluster` Helm release is installed or in a healthy state.

```mermaid
graph TD
    A>Kustomization: rook-ceph] -->|Creates| B[HelmRelease: rook-ceph]
    A>Kustomization: rook-ceph] -->|Creates| C[HelmRelease: rook-ceph-cluster]
    C>HelmRelease: rook-ceph-cluster] -->|Depends on| B>HelmRelease: rook-ceph]
    D>Kustomization: atuin] -->|Creates| E(HelmRelease: atuin)
    E>HelmRelease: atuin] -->|Depends on| C>HelmRelease: rook-ceph-cluster]
```

### Automation & Tooling

The repository includes comprehensive automation for cluster management:

- **Task Automation**: [Task](https://taskfile.dev/) for common operations (bootstrap, upgrades, maintenance)
- **Template Rendering**: [minijinja-cli](https://github.com/mitsuhiko/minijinja) for Jinja2 template processing
- **Secret Injection**: [1Password CLI](https://developer.1password.com/docs/cli/) for secure secret injection
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
- **External DNS**: Automatic DNS record management in Cloudflare for public services
- **Gateway API**: Cilium-based ingress with dedicated LoadBalancer IPs per application

### Internal Resolution
- **k8s-gateway**: Internal DNS resolution for cluster services and HTTPRoutes
- **CoreDNS**: Kubernetes cluster DNS with custom configurations
- **L2 Announcements**: Cilium L2 announcements for LoadBalancer IP allocation

### Network Architecture
- **CNI**: Cilium with eBPF datapath for high-performance networking
- **Load Balancing**: Maglev algorithm with DSR (Direct Server Return) mode
- **IP Management**: Kubernetes IPAM with native routing (10.42.0.0/16)
- **Gateway IPs**: Dedicated IP range (192.168.120.101-116) for application access

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4f1/512.gif" alt="📱" width="20" height="20"> Applications

The cluster hosts a variety of self-hosted applications organized by namespace and function:

### Productivity & Tools (default namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Atuin](https://github.com/atuinsh/atuin) | Shell history sync | `sh.${SECRET_DOMAIN}` |
| [Fusion](https://github.com/0x2E/fusion) | RSS feed aggregator | `feeds.${SECRET_DOMAIN}` |

### Observability Stack (observability namespace)

| Application | Purpose | Access |
|-------------|---------|--------|
| [Grafana](https://github.com/grafana/grafana) | Metrics visualization and dashboards | `grafana.${SECRET_DOMAIN}` |
| [Prometheus](https://github.com/prometheus/prometheus) | Metrics collection and alerting | `prometheus.${SECRET_DOMAIN}` |
| [Loki](https://github.com/grafana/loki) | Log aggregation and querying | Internal only |
| [Alloy](https://github.com/grafana/alloy) | Telemetry data collection | Internal only |
| [Alertmanager](https://github.com/prometheus/alertmanager) | Alert routing and management | Internal only |
| [Blackbox Exporter](https://github.com/prometheus/blackbox_exporter) | Endpoint monitoring | Internal only |
| [Node Exporter](https://github.com/prometheus/node_exporter) | System metrics collection | Internal only |
| [Kube State Metrics](https://github.com/kubernetes/kube-state-metrics) | Kubernetes metrics | Internal only |
| [KEDA](https://github.com/kedacore/keda) | Kubernetes event-driven autoscaling | Internal only |
| [Kromgo](https://github.com/kashalls/kromgo) | Kubernetes resource metrics API | Internal only |
| [Silence Operator](https://github.com/giantswarm/silence-operator) | Automated alert silencing | Internal only |

### Storage & Infrastructure

| Application | Purpose | Access |
|-------------|---------|--------|
| [Rook Ceph](https://github.com/rook/rook) | Distributed storage cluster | `rook-ceph-cluster.${SECRET_DOMAIN}` |
| [OpenEBS](https://github.com/openebs/openebs) | Local persistent volume provisioner | Internal only |

All applications use Cilium Gateway API for ingress with automatic TLS certificates from Google Trust Services via cert-manager.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/2699_fe0f/512.gif" alt="⚙" width="20" height="20"> Hardware

### Physical Infrastructure

| Component                   | Specifications                                      | Function                          |
|-----------------------------|-----------------------------------------------------|-----------------------------------|
| **Host Server**             | Intel R2000 Server                                 | Virtualization Host               |
| ├─ **CPU**                  | 2x Intel Xeon E5-2630 v4 @ 2.20GHz (20 cores)     | VM compute resources              |
| ├─ **Memory**               | 384GB RAM                                           | VM memory allocation              |
| ├─ **Network**              | 2x 10GbE bonded NICs                               | High-speed networking             |
| └─ **Operating System**     | TrueNAS Scale                                       | VM host & storage management      |

### Storage Architecture

| Storage Tier                | Hardware                                            | Purpose                           |
|-----------------------------|-----------------------------------------------------|-----------------------------------|
| **Primary Pool**            | 12x SSD (8x 4TB + 4x 1TB) in 3x RAIDZ vdevs       | VM storage & datasets             |
| **Hot Spare**               | 1x 4TB SSD                                         | Automatic replacement             |
| **SLOG (Intent Log)**       | 2x 800GB Intel NVMe (mirrored)                     | Synchronous write acceleration    |
| **Special Metadata vdev**   | 2x 1.6TB Hitachi NVMe (mirrored)                   | Metadata & small block storage    |

### Virtual Machine Configuration

| VM Role                     | Count | vCPU | Memory | Storage Layout                    | OS            |
|-----------------------------|-------|------|--------|-----------------------------------|---------------|
| **Kubernetes Control Plane** | 3     | 8    | 32GB   | 250GB (OS) + 1TB (local) + 800GB (ceph) | Talos Linux   |

**Total VM Resources**: 24 vCPUs, 96GB RAM allocated from the 40-core, 384GB host system.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f4da/512.gif" alt="📚" width="20" height="20"> Learning & Credits

This homelab serves as a continuous learning platform for cloud-native technologies, GitOps practices, and infrastructure automation. The setup provides hands-on experience with enterprise-grade tools and practices in a controlled environment.

**Special thanks to [onedr0p](https://github.com/onedr0p)** and the [k8s-at-home](https://github.com/k8s-at-home) community. This repository was heavily inspired by onedr0p's [home-ops](https://github.com/onedr0p/home-ops) repository, which served as an excellent learning resource and foundation for understanding GitOps workflows and Kubernetes cluster management.

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f31f/512.gif" alt="🌟" width="20" height="20"> Stargazers

<div align="center">

<a href="https://star-history.com/#varuntirumala1/home-ops&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=varuntirumala1/home-ops&type=Date&theme=dark" />
    <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=varuntirumala1/home-ops&type=Date" />
    <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=varuntirumala1/home-ops&type=Date" />
  </picture>
</a>

</div>

---

## <img src="https://fonts.gstatic.com/s/e/notoemoji/latest/1f64f/512.gif" alt="🙏" width="20" height="20"> Gratitude and Thanks

Thanks to all the people who donate their time to the [Home Operations](https://discord.gg/home-operations) Discord community. Be sure to check out [kubesearch.dev](https://kubesearch.dev/) for ideas on how to deploy applications or get ideas on what you could deploy.
