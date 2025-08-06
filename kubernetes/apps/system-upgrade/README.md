# System Upgrade Controller Configuration

This directory contains the configuration for the Kubernetes System Upgrade Controller, which manages automated upgrades of Talos Linux and Kubernetes versions.

## Architecture

The system upgrade setup consists of three main components:

1. **system-upgrade-controller**: The main controller that manages upgrade plans
2. **system-upgrade-controller-plans**: The upgrade plans for Kubernetes and Talos
3. **versions**: A ConfigMap containing version variables used by the upgrade plans

## Variable Substitution Fix

### Problem
The system upgrade controller plans require specific version variables (`KUBERNETES_VERSION` and `TALOS_VERSION`) for substitution. However, the parent `cluster-apps` kustomization was applying a patch that added `cluster-config-secret` as a substitution source to all child kustomizations, causing conflicts.

### Root Cause
The `cluster-config-secret` doesn't contain the required version variables, causing envsubst to fail with:
```
post build failed for 'kubernetes': envsubst error: YAMLToJSON: yaml: line 23: mapping values are not allowed in this context
```

### Solution
Added exclusion labels to prevent system-upgrade kustomizations from receiving the cluster-config-secret substitution:

1. **Added exclusion labels** to all system-upgrade kustomizations:
   ```yaml
   metadata:
     labels:
       substitution.flux.home.arpa/disabled: "true"
   ```

2. **Updated cluster-apps patch selector** to exclude labeled kustomizations:
   ```yaml
   target:
     group: kustomize.toolkit.fluxcd.io
     kind: Kustomization
     labelSelector: "substitution.flux.home.arpa/disabled!=true"
   ```

## Configuration Approaches

### Current Approach: Separate Versions Kustomization
The current implementation uses a dedicated `versions/ks.yaml` kustomization that creates the ConfigMap:

```yaml
# versions/ks.yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: versions
  namespace: system-upgrade
  labels:
    substitution.flux.home.arpa/disabled: "true"
spec:
  path: ./kubernetes/apps/system-upgrade/versions
```

**Pros:**
- Works with Flux CD's kustomization processing
- Clear separation of concerns
- Explicit dependency management

**Cons:**
- Additional kustomization resource
- More complex than the original intent

### Alternative Approach: kustomizeconfig.yaml (Not Compatible)
The original intent was to use `kustomizeconfig.yaml` with `configMapGenerator`:

```yaml
# kustomization.yaml
configMapGenerator:
  - name: versions
    envs:
      - versions.env
configurations:
  - kustomizeconfig.yaml
```

**Why this doesn't work with Flux:**
- Flux processes individual `ks.yaml` files, not the parent `kustomization.yaml`
- The ConfigMap generation never happens because Flux doesn't process the parent kustomization
- The `kustomizeconfig.yaml` is never applied

## Version Management

Versions are defined in `versions.env`:
```bash
# renovate: datasource=docker depName=ghcr.io/siderolabs/kubelet
KUBERNETES_VERSION=v1.33.3
# renovate: datasource=docker depName=ghcr.io/siderolabs/installer
TALOS_VERSION=v1.10.6
```

These versions are automatically updated by Renovate bot based on the datasource annotations.

## Future Use Cases

For similar scenarios where you need to exclude kustomizations from parent patches:

1. **Add the exclusion label** to the kustomization metadata:
   ```yaml
   metadata:
     labels:
       substitution.flux.home.arpa/disabled: "true"
   ```

2. **Ensure the parent patch uses the correct selector**:
   ```yaml
   target:
     labelSelector: "substitution.flux.home.arpa/disabled!=true"
   ```

This pattern can be used for any kustomization that needs custom substitution sources or should be excluded from global patches.

## Files Structure

```
kubernetes/apps/system-upgrade/
├── README.md                           # This documentation
├── kustomization.yaml                  # Parent kustomization (not processed by Flux)
├── kustomizeconfig.yaml               # Original config (not used with current approach)
├── versions.env                       # Version definitions
├── versions/
│   ├── ks.yaml                       # Versions kustomization (creates ConfigMap)
│   ├── kustomization.yaml            # Versions kustomize config
│   └── versions.env                  # Version definitions (duplicate)
└── system-upgrade-controller/
    ├── ks.yaml                       # Controller and plans kustomizations
    ├── app/                          # Controller application
    └── plans/                        # Upgrade plans (kubernetes.yaml, talos.yaml)
```

## Verification

To verify the setup is working:

1. Check kustomization status:
   ```bash
   kubectl get kustomizations -n system-upgrade
   ```

2. Verify ConfigMap exists:
   ```bash
   kubectl get configmap versions -n system-upgrade
   ```

3. Check upgrade plans:
   ```bash
   kubectl get plans -n system-upgrade
   ```

All should show the correct versions and be in a ready state.
