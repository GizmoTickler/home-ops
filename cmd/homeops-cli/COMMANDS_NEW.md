# New Commands Documentation

## Kubernetes Management Commands

### 1. `prune-pods` - Clean Up Pods

**Purpose**: Removes pods in specified phases (Failed, Succeeded, Pending).

```bash
homeops-cli k8s prune-pods [flags]
```

**Flags**:
- `--phase string`: Comma-separated list of pod phases to prune (default: `Failed,Succeeded,Pending`)
- `--namespace string`: Limit to specific namespace (default: all namespaces)
- `--dry-run`: Show what would be deleted without making changes

**Examples**:
```bash
# Clean up all failed and succeeded pods
homeops-cli k8s prune-pods

# Clean up only failed pods in specific namespace
homeops-cli k8s prune-pods --phase Failed --namespace default

# Dry run to see what would be deleted
homeops-cli k8s prune-pods --dry-run
```

---

### 2. `view-secret` - View Decoded Secrets

**Purpose**: Retrieves and decodes Kubernetes secret data.

```bash
homeops-cli k8s view-secret <secret-name> [flags]
```

**Flags**:
- `-n, --namespace string`: Kubernetes namespace (default: `default`)
- `-o, --format string`: Output format (table|json|yaml) (default: `table`)
- `-k, --key string`: Specific key to view (optional)

**Examples**:
```bash
# View secret in table format
homeops-cli k8s view-secret my-secret -n default

# View secret as JSON
homeops-cli k8s view-secret my-secret -n default -o json

# View specific key from secret
homeops-cli k8s view-secret my-secret -n default -k password
```

---

### 3. `sync` - Bulk Flux Resource Sync

**Purpose**: Reconcile multiple Flux resources of a specific type.

```bash
homeops-cli k8s sync [flags]
```

**Flags**:
- `-t, --type string`: Resource type (gitrepo|helmrelease|kustomization|ocirepository) (required)
- `-n, --namespace string`: Limit to specific namespace (default: all namespaces)
- `--parallel`: Run reconciliations in parallel (experimental)

**Examples**:
```bash
# Sync all HelmReleases
homeops-cli k8s sync --type helmrelease

# Sync all Kustomizations in specific namespace
homeops-cli k8s sync --type kustomization --namespace flux-system

# Sync all GitRepositories with parallel execution
homeops-cli k8s sync --type gitrepo --parallel
```

---

### 4. `force-sync-externalsecret` - Force ExternalSecret Sync

**Purpose**: Triggers immediate synchronization of ExternalSecrets.

```bash
homeops-cli k8s force-sync-externalsecret [name] [flags]
```

**Flags**:
- `-n, --namespace string`: Kubernetes namespace (default: `default`)
- `--all`: Sync all ExternalSecrets in namespace
- `--timeout int`: Timeout in seconds to wait for sync (default: `60`)

**Examples**:
```bash
# Force sync specific ExternalSecret
homeops-cli k8s force-sync-externalsecret my-secret -n default

# Force sync all ExternalSecrets in namespace
homeops-cli k8s force-sync-externalsecret --all -n default
```

---

### 5. Flux-Local Kustomization Commands

#### 5.1 `render-ks` - Render Kustomization Locally

**Purpose**: Builds and renders a Kustomization locally without applying to cluster.

```bash
homeops-cli k8s render-ks <dir> <ks-name> [flags]
```

**Flags**:
- `-o, --output string`: Write output to file instead of stdout

**Examples**:
```bash
# Render Kustomization to stdout
homeops-cli k8s render-ks ./kubernetes/apps/default/app app-ks

# Render and save to file
homeops-cli k8s render-ks ./kubernetes/apps/default/app app-ks -o rendered.yaml
```

#### 5.2 `apply-ks` - Apply Rendered Kustomization

**Purpose**: Renders a Kustomization locally and applies it to the cluster.

```bash
homeops-cli k8s apply-ks <dir> <ks-name> [flags]
```

**Flags**:
- `--dry-run`: Perform a dry-run without applying

**Examples**:
```bash
# Apply Kustomization
homeops-cli k8s apply-ks ./kubernetes/apps/default/app app-ks

# Dry-run to see what would be applied
homeops-cli k8s apply-ks ./kubernetes/apps/default/app app-ks --dry-run
```

#### 5.3 `delete-ks` - Delete Kustomization Resources

**Purpose**: Renders a Kustomization locally and deletes its resources from the cluster.

```bash
homeops-cli k8s delete-ks <dir> <ks-name> [flags]
```

**Flags**:
- `--force`: Force deletion without confirmation

**Examples**:
```bash
# Delete Kustomization resources (with confirmation)
homeops-cli k8s delete-ks ./kubernetes/apps/default/app app-ks

# Force delete without confirmation
homeops-cli k8s delete-ks ./kubernetes/apps/default/app app-ks --force
```

---

## VolSync Management Commands

### 1. `suspend` - Suspend VolSync Resources

**Purpose**: Suspends ReplicationSource or ReplicationDestination resources.

```bash
homeops-cli volsync suspend [name] [flags]
```

**Flags**:
- `-n, --namespace string`: Kubernetes namespace (required)
- `--all`: Suspend all ReplicationSources and ReplicationDestinations in namespace

**Examples**:
```bash
# Suspend specific ReplicationSource
homeops-cli volsync suspend my-app-source -n default

# Suspend all VolSync resources in namespace
homeops-cli volsync suspend --all -n default
```

---

### 2. `resume` - Resume VolSync Resources

**Purpose**: Resumes suspended ReplicationSource or ReplicationDestination resources.

```bash
homeops-cli volsync resume [name] [flags]
```

**Flags**:
- `-n, --namespace string`: Kubernetes namespace (required)
- `--all`: Resume all ReplicationSources and ReplicationDestinations in namespace

**Examples**:
```bash
# Resume specific ReplicationSource
homeops-cli volsync resume my-app-source -n default

# Resume all VolSync resources in namespace
homeops-cli volsync resume --all -n default
```

---
