# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a comprehensive home infrastructure repository containing:
- **Kubernetes cluster configuration** using GitOps with Flux
- **Fedora CoreOS + kubeadm** cluster management (nodes k8s-0/1/2 run FCOS 44 since 2026-09-26; the Flatcar provider remains selectable and a legacy Talos provider is retained for reference)
- **HomeOps CLI** (Go-based) for infrastructure automation
- **Kubernetes applications** deployed via Helm and Kustomize

## IMPORTANT: Git Workflow

**ALWAYS commit and push changes to git when making configuration changes:**
- Any changes to YAML files in `kubernetes/` directory must be committed and pushed
- Flux GitOps requires changes to be in the git repository to reconcile
- Running `flux reconcile` alone is NOT sufficient - changes must be in git first

```bash
# Standard workflow for configuration changes:
git add -A
git commit -m "feat/fix: descriptive message"
git push
flux reconcile source git flux-system -n flux-system
```

## Essential Commands

### Go CLI Development (cmd/homeops-cli/)

The repository contains a custom CLI tool written in Go. Always work from the `cmd/homeops-cli/` directory:

```bash
cd cmd/homeops-cli/

# Build and test (essential during development)
make build
make test
make check  # Run all checks (fmt, vet, lint, test)

# Development workflow
make dev    # Quick cycle: fmt, build, test
make fmt    # Format code
make lint   # Run linter (requires golangci-lint)

# Dependencies
make deps         # Download and tidy dependencies
make deps-update  # Update all dependencies
```

### Environment Setup

The CLI relies on several environment variables. The following must be set globally:
- `KUBECONFIG` - Path to your Kubernetes config file
- `SOPS_AGE_KEY_FILE` - Path to your SOPS age key file
- `TALOSCONFIG` - Path to your Talos config file (legacy Talos provider only; not used by the FCOS/Flatcar kubeadm path)

The following is automatically set by main.go if not already defined:
- `MINIJINJA_CONFIG_FILE=./.minijinja.toml`

Required environment variables:
- `KUBERNETES_VERSION` - Current Kubernetes version (kubeadm clusters resolve this from the kubeadm System Upgrade Controller Plan)
- `TALOS_VERSION` - Talos version (legacy Talos provider only)

### Kubernetes Management

**IMPORTANT:** KUBECONFIG is set as a global environment variable. Always use kubectl and flux commands directly without specifying kubeconfig path.

```bash
# Correct way - KUBECONFIG is globally set
kubectl <command>
flux <command>

# NEVER use these patterns since KUBECONFIG is global:
# kubectl --kubeconfig=./kubeconfig <command>  # WRONG
# flux --kubeconfig=./kubeconfig <command>     # WRONG
```

### CLI Usage Examples

```bash
# Bootstrap entire cluster (defaults to the `flatcar` kubeadm provider, which also drives FCOS nodes; the OS comes from cluster.os in homeops.yaml)
./homeops-cli bootstrap

# Fedora CoreOS operations (current cluster OS)
./homeops-cli fcos deploy-vm --nodes k8s-test
./homeops-cli fcos os-status
./homeops-cli cluster replace-node --node k8s-2 --plan
# Lifecycle verbs shared with Flatcar live under `flatcar` (kubeconfig, save-pki, reboot-node, reset-node, ...)
# (the kubeadm-upgrade SUC Plan is Flatcar-only; see kubernetes/apps/system-upgrade/README.md for FCOS)

# Legacy Talos operations (retained provider; not the current cluster OS)
./homeops-cli bootstrap --provider talos
./homeops-cli talos apply-node --ip 192.168.122.10
./homeops-cli talos deploy-vm --name test_node --generate-iso

# Kubernetes operations
./homeops-cli k8s browse-pvc --namespace default
./homeops-cli k8s sync --type kustomization --namespace flux-system

# Legacy VolSync commands still exist but VolSync is no longer installed;
# backups are kopiur (kopiur.home-operations.com) — inspect with kubectl:
kubectl get snapshotschedules,snapshots -A
```
**Always run format, type-check, and test before completing any task.**

## Architecture Overview

### CLI Architecture (cmd/homeops-cli/)

The HomeOps CLI is structured as a Cobra-based application with the following key components:

**Main Commands:**
- `bootstrap/` - Complete cluster bootstrap with preflight checks (defaults to the `flatcar` kubeadm provider, which also drives FCOS nodes; `--provider talos` for the legacy path)
- `flatcar/` - kubeadm node and VM management: the `fcos` command group (current cluster OS) and the `flatcar` group, sharing deployers and lifecycle verbs
- `cluster/` - `rehearse-node` drills and `replace-node` in-place rebuilds
- `talos/` - Talos Linux node and VM management (legacy; retained for reference/rollback)
- `kubernetes/` - Kubernetes cluster operations
- `volsync/` - Legacy VolSync backup/restore operations (VolSync is no longer installed)
- `workstation/` - Local development environment setup
- `completion/` - Shell completion support

**Internal Packages:**
- `internal/templates/` - Embedded templates: `fcos/` (FCOS Butane overlay, current), `flatcar/` (Butane/Ignition + kubeadm configs, shared files reused by FCOS) and `talos/` (legacy Talos machine config)
- `internal/flatcar/` - Ignition rendering for the Flatcar/kubeadm provider
- `internal/fcos/` - Fedora CoreOS Ignition rendering and stream-metadata image resolution
- `internal/talos/` - Talos factory API integration for custom ISOs (legacy)
- `internal/truenas/` - TrueNAS API client for VM management
- `internal/yaml/` - YAML processing and merging utilities
- `internal/ssh/` - SSH client for remote operations
- `internal/iso/` - ISO download and management

**Template System:**
- Templates are embedded in the binary via go:embed
- FCOS/kubeadm: Butane (`internal/templates/fcos/butane/controlplane.bu`, overlaid on the Flatcar files) → Ignition + kubeadm init/join configs in `internal/templates/flatcar/`
- Legacy Talos machine-config templates in `internal/templates/talos/`
- Bootstrap templates for initial cluster resources
- 1Password integration for secret injection during template rendering

**VM Deployment Flow (FCOS/kubeadm, current; Flatcar is the same apart from the image and fw_cfg key):**
1. Render the Butane → Ignition config (with 1Password secret injection)
2. Upload the Ignition config to the Proxmox snippets store over SSH
3. Create the VM referencing the Ignition config (`--name`, no dashes in ZVol/VM names)
4. kubeadm init/join runs on first boot; Cilium CNI is then installed

Legacy Talos flow: generate a custom Talos ISO via the factory API + schematic.yaml, download to storage, create the VM, then apply Talos machine config.

### Kubernetes GitOps Structure

**Flux Workflow:**
- `kubernetes/flux/` - Flux system configuration
- `kubernetes/apps/` - Applications organized by namespace
- `kubernetes/components/` - Reusable Kustomize components

**Application Structure Pattern:**
```
kubernetes/apps/<namespace>/<app>/
├── app/
│   ├── helmrelease.yaml      # Helm deployment
│   ├── kustomization.yaml    # Kustomize configuration
│   └── helm/values.yaml      # Helm values
└── ks.yaml                   # Flux Kustomization
```

**Key Namespaces:**
- `flux-system` - Flux controllers
- `kube-system` - Core Kubernetes components (Cilium, CoreDNS)
- `cert-manager` - Certificate management
- `external-secrets` - 1Password integration
- `observability` - VictoriaMetrics, VictoriaLogs, Grafana stack
- `downloads` - Media acquisition apps (Radarr, Sonarr, qBittorrent, etc.)
- `media` - Media serving apps
- `self-hosted` - Self-hosted utilities and tools
- `automation` - Automation tools (Home Assistant, n8n)
- `network` - Networking applications
- `scale-csi` - TrueNAS CSI driver (primary storage, `scale-nvmeof` default StorageClass)
- `openebs-system` - Local storage
- `kopiur-system` - Backup orchestration (kopiur + Kopia UI)
- `system-upgrade` - system-upgrade-controller Plans + kured

## Kubernetes Manifest Patterns

### ks.yaml (Flux Kustomization) Pattern

The `ks.yaml` files define Flux Kustomization resources that reconcile applications from git.

**Standard Pattern:**
```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: <app-name>
spec:
  interval: 1h                    # Reconciliation interval (1h standard, 12h for CRDs)
  path: ./kubernetes/apps/<namespace>/<app>/app
  prune: true                     # Delete resources removed from git (false for CRDs)
  sourceRef:
    kind: GitRepository
    name: flux-system
    namespace: flux-system
  targetNamespace: <namespace>
  wait: false                     # Wait for resources to be ready (true for infrastructure)
```

**Component Injection (Storage-aware apps):**
```yaml
spec:
  components:
    - ../../../../components/nfs-scaler    # KEDA auto-scaling for NFS availability
    - ../../../../components/kopiur/backup    # Backup/restore with kopiur (Kopia)
```

**Dependencies:**
```yaml
spec:
  dependsOn:
    - name: keda
      namespace: observability
```

**Variable Substitution:**
```yaml
spec:
  postBuild:
    substitute:
      APP: atuin
      KOPIUR_CAPACITY: 15Gi
```

**Health Checks (Critical infrastructure):**
```yaml
spec:
  healthChecks:
    - apiVersion: cert-manager.io/v1
      kind: ClusterIssuer
      name: gts-production
  healthCheckExprs:
    - apiVersion: cert-manager.io/v1
      kind: ClusterIssuer
      failed: status.conditions.filter(e, e.type == 'Ready').all(e, e.status == 'False')
      current: status.conditions.filter(e, e.type == 'Ready').all(e, e.status == 'True')
```

**Multi-Part Applications (e.g., Grafana):**
Multiple Kustomization resources in one file with dependencies:
```yaml
---
# Part 1: Deploy operator
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana
spec:
  wait: true  # Wait for operator to be ready
---
# Part 2: Deploy instance
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana-instance
spec:
  dependsOn:
    - name: grafana
  wait: false
```

**Root Flux Kustomization (`flux/cluster/ks.yaml`):**
Applies patches to all child Kustomizations:
- SOPS decryption configuration
- HelmRelease defaults (CRD strategies, retry behavior)
- cluster-config-secret substitution

### HelmRelease Pattern

**Standard Structure:**
```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: <app-name>
spec:
  chartRef:
    kind: OCIRepository
    name: <app-name>
  interval: 1h
  values:
    # Inline Helm values (no separate values.yaml files)
```

**Two Chart Types:**
1. **app-template** (bjw-s-labs v5.2.1): Used for custom apps (Radarr, Sonarr, qBittorrent, etc.)
2. **Native charts**: Used for infrastructure (Cilium, Grafana, Victoria Metrics, scale-csi, kopiur)

### app-template HelmRelease Pattern

**Core Structure:**
```yaml
values:
  controllers:
    <controller-name>:
      annotations:
        reloader.stakater.com/auto: "true"  # Auto-reload on config changes
      containers:
        app:
          image:
            repository: ghcr.io/...
            tag: x.y.z@sha256:...  # Always pinned with SHA256 digest
          env:
            PORT: &port 80
          envFrom:
            - secretRef:
                name: <app>-secret
          probes:
            liveness: &probes
              enabled: true
              custom: true
              spec:
                httpGet:
                  path: /ping
                  port: *port
                initialDelaySeconds: 20
                periodSeconds: 10
                timeoutSeconds: 1
                failureThreshold: 3
            readiness: *probes
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: {drop: ["ALL"]}
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: 1000m
              memory: 2Gi
```

**Security Context (Standard for all apps):**
```yaml
defaultPodOptions:
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    fsGroupChangePolicy: OnRootMismatch
```

**Service Definition:**
```yaml
service:
  app:
    ports:
      http:
        port: *port
```

**Gateway API Routes (HTTPRoute):**
```yaml
route:
  app:
    hostnames:
      - "{{ .Release.Name }}.${SECRET_DOMAIN}"
    parentRefs:
      - name: envoy-internal    # or envoy-external
        namespace: network
        sectionName: https
```

**Persistence Patterns:**

```yaml
persistence:
  # Existing PVC
  config:
    existingClaim: "{{ .Release.Name }}"

  # NFS Mount
  media:
    type: nfs
    server: nas01.${SECRET_DOMAIN}
    path: /mnt/flashstor/data
    globalMounts:
      - path: /media

  # EmptyDir
  tmp:
    type: emptyDir

  # ConfigMap
  config-file:
    type: configMap
    name: <configmap-name>
    globalMounts:
      - path: /config/file.conf
        subPath: file.conf

  # Secret with advanced mounts
  auth:
    type: secret
    name: <secret-name>
    advancedMounts:
      <controller-name>:
        <container-name>:
          - path: /path/to/file
            subPath: file

  # Image-based (sidecar pattern)
  tool:
    type: image
    image: ghcr.io/org/tool:tag@sha256:...
```

**Multi-Container Pattern:**
```yaml
controllers:
  <app>:
    initContainers:
      sidecar:
        image: {...}
        restartPolicy: Always  # Long-running sidecar
    containers:
      app: {...}
      helper: {...}
```

### Kustomization Pattern

**Namespace-Level (`kubernetes/apps/<namespace>/kustomization.yaml`):**
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: <namespace>
components:
  - ../../components/alerts         # AlertManager + GitHub status
  - ../../components/cluster-secret # Cluster-config substitution
resources:
  - ./namespace.yaml
  - ./app1/ks.yaml
  - ./app2/ks.yaml
```

**App-Level (`kubernetes/apps/<namespace>/<app>/app/kustomization.yaml`):**
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./ocirepository.yaml
  - ./externalsecret.yaml
  - ./pvc.yaml
  - ./helmrelease.yaml
  - ./grafanadashboard.yaml
  - ./vmrule.yaml
  - ./servicemonitor.yaml
```

### Component Pattern

Components are reusable Kustomize configurations injected into multiple apps.

**Component Structure:**
```yaml
apiVersion: kustomize.config.k8s.io/v1alpha1  # Note: v1alpha1, not v1beta1
kind: Component
resources:
  - ./resource1.yaml
  - ./resource2.yaml
```

**Available Components:**

1. **kopiur/backup** (`components/kopiur/backup/`):
   - PVC with a kopiur `Restore` as `dataSourceRef` (restore-on-create)
   - `SnapshotPolicy` + hourly `SnapshotSchedule` to the `nas-s3` ClusterRepository
   - Variables: `${APP}`, `${KOPIUR_CAPACITY}`, `${KOPIUR_STORAGECLASS}`, `${KOPIUR_SNAPSHOTCLASS}`

2. **nfs-scaler** (`components/nfs-scaler/`):
   - ScaledObject (KEDA) that scales to 0 when NFS unavailable
   - Prometheus query: `probe_success{instance=~".+:2049"}`

3. **alerts** (`components/alerts/`):
   - AlertManager configuration
   - GitHub status notifications

4. **cluster-secret** (`components/cluster-secret/`):
   - ExternalSecret for cluster-wide configuration
   - Provides: `${SECRET_DOMAIN}`, etc.

**Usage:**
```yaml
# In ks.yaml:
spec:
  components:
    - ../../../../components/kopiur/backup

# In namespace kustomization.yaml:
components:
  - ../../components/alerts
```

### OCIRepository Pattern

**Standard Pattern:**
```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: <app-name>
spec:
  interval: 15m
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
    operation: copy
  ref:
    tag: <version>
  url: oci://<registry>/<path>
```

**Common Registries:**
- `oci://ghcr.io/bjw-s-labs/helm/app-template` - app-template v5.2.1
- `oci://quay.io/cilium/charts/cilium` - Cilium (upstream OCI chart; the home-operations mirror stopped at 1.18.6)
- `oci://ghcr.io/grafana/helm-charts/grafana-operator` - Official Grafana
- `oci://ghcr.io/home-operations/charts/kopiur` - kopiur backup operator

### Variable Substitution Flow

1. **cluster-config-secret** (from 1Password via ExternalSecret)
2. **Flux root patches** inject into all Kustomizations
3. **App-level postBuild.substitute** (APP=atuin, KOPIUR_CAPACITY=15Gi)
4. **Component templates** use variables (${APP}, ${KOPIUR_CAPACITY})
5. **Helm values** use templates ({{ .Release.Name }}, ${SECRET_DOMAIN})

### Observability Pattern

Most applications include:
```yaml
# In helmrelease.yaml:
serviceMonitor:
  app:
    endpoints:
      - port: http

# Additional files in app/:
- vmrule.yaml           # Victoria Metrics alerting rules
- servicemonitor.yaml   # Prometheus scraping config
- grafanadashboard.yaml # Grafana dashboard definitions
```

### Best Practices

1. **Security**: All apps run non-root with read-only root filesystem
2. **Images**: Always pin SHA256 digests
3. **Secrets**: Use External Secrets Operator with 1Password
4. **Storage**: scale-csi (TrueNAS NVMe-oF) for app PVCs, OpenEBS for local scratch, kopiur for backups
5. **Networking**: Gateway API (HTTPRoute) instead of Ingress
6. **GitOps**: All configuration in git, Flux reconciles automatically
7. **Dependencies**: Explicitly declare with dependsOn
8. **Schema Validation**: Always include yaml-language-server schema hints

### Infrastructure Components

**Storage:**
- scale-csi (TrueNAS SCALE NVMe-oF/TCP) for application volumes
- OpenEBS for local persistent volumes
- kopiur (Kopia) for backups

**Networking:**
- Cilium CNI with eBPF datapath
- Gateway API for ingress
- Cloudflare Tunnel for external access
- external-dns (PowerDNS RFC2136 + Cloudflare) for DNS records

**Security:**
- External Secrets Operator with 1Password
- SOPS with age encryption for Git-stored secrets
- cert-manager with Google Trust Services

## Development Guidelines

### Template Development

When working with node configuration templates:
- FCOS/kubeadm templates (current): `cmd/homeops-cli/internal/templates/fcos/` (overlaid on the shared `cmd/homeops-cli/internal/templates/flatcar/` files)
- Legacy Talos templates: `cmd/homeops-cli/internal/templates/talos/`
- Use Jinja2 syntax with environment variable substitution
- Test template rendering with `make dev` before deployment
- 1Password references use format: `op://vault/item/field`

### Error Handling Patterns

The CLI uses structured error handling:
- Wrap errors with context using fmt.Errorf
- Use the common.ColorLogger for consistent output
- Implement retry logic for network operations
- Validate inputs early and provide clear error messages

### Testing

When adding new functionality:
- Add unit tests for internal packages
- Use the embedded test framework in `internal/testing/`
- Test CLI commands with dry-run flags where available
- Validate template rendering in isolation

### 1Password Integration

The CLI integrates with 1Password for secret management:
- Secrets are resolved during template rendering
- Bootstrap process validates 1Password connectivity
- Use 1Password references in YAML templates, not Go code
- Test authentication with preflight checks

## Data Restore Process

Application data restores are kopiur `Restore` resources: each app PVC created by the `components/kopiur/backup` component restores from its latest snapshot on (re)creation. List snapshots with `kubectl get snapshots.kopiur.home-operations.com -n <namespace>`.

## Common Issues and Solutions

**Bootstrap Failures:**
- Ensure 1Password CLI is authenticated: `op signin`
- Verify all required tools are installed: `kubectl`, `kustomize`, `op`, `helmfile` (plus `talosctl` only for the legacy `--provider talos` path)
- Check that the kubeadm System Upgrade Controller Plan provides the target Kubernetes version
- Run with `--skip-preflight` only for debugging

**Template Rendering Issues:**
- Verify environment variables are set correctly
- Check template syntax with embedded validation
- Use dry-run mode to test template output
- Ensure 1Password references are accessible

**VM Deployment Problems:**
- FCOS/Flatcar: ensure the Ignition config uploads to the Proxmox snippets store (SSH reachable) before VM creation; FCOS first boot layers greenboot and reboots once before kubeadm is available
- Legacy Talos: use the `--generate-iso` flag for custom Talos ISOs and ensure the ISO is downloaded before VM creation
- Verify hypervisor (Proxmox/TrueNAS) credentials are configured
- Check ZVol naming conventions (no dashes in VM names)

This CLI tool is designed for infrastructure automation and requires careful attention to the order of operations, especially during bootstrap and VM deployment processes.
