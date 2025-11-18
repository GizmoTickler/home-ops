# Contributing to Home Ops

## Adding a New Application

This repository uses a "Copy-Paste Pattern" for adding new applications. This ensures consistency across the cluster and leverages existing patterns for Flux and Kustomize.

### 1. Directory Structure

Applications are organized by namespace in `kubernetes/apps/`.

```text
kubernetes/apps/
└── <namespace>/          # e.g., media, observability
    ├── <app-name>/       # e.g., jellyseerr
    │   ├── app/          # Application manifests
    │   │   ├── helmrelease.yaml
    │   │   └── kustomization.yaml
    │   └── ks.yaml       # Flux Kustomization
    ├── kustomization.yaml # Namespace aggregation
    └── namespace.yaml     # Namespace definition
```

### 2. The Pattern

To add a new application, follow these steps:

1.  **Choose a Namespace**: Identify the appropriate namespace (e.g., `media`, `downloads`, `observability`). If a new namespace is needed, create a new directory and a `namespace.yaml`.
2.  **Create App Directory**: Create a directory for your app: `kubernetes/apps/<namespace>/<app-name>`.
3.  **Create `ks.yaml`**: This tells Flux how to deploy your app. Copy an existing `ks.yaml` (e.g., from `jellyseerr`) and update:
    - `metadata.name`: Your app name.
    - `spec.path`: Path to your app's `app` directory (`./kubernetes/apps/<namespace>/<app-name>/app`).
    - `spec.postBuild.substitute`: Update app-specific variables.

    **Template `ks.yaml`**:
    ```yaml
    ---
    # yaml-language-server: $schema=https://kubernetes-schema.pages.dev/kustomize.toolkit.fluxcd.io/kustomization_v1.json
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: my-app
    spec:
      interval: 1h
      path: ./kubernetes/apps/<namespace>/my-app/app
      prune: true
      sourceRef:
        kind: GitRepository
        name: flux-system
        namespace: flux-system
      targetNamespace: <namespace>
      wait: false
      # Optional: Dependencies
      # dependsOn:
      #   - name: rook-ceph-cluster
      #     namespace: rook-ceph
    ```

4.  **Create `app` Directory**: Create `kubernetes/apps/<namespace>/<app-name>/app`.
5.  **Create `app/kustomization.yaml`**: This is a standard Kustomize file.

    **Template `app/kustomization.yaml`**:
    ```yaml
    ---
    # yaml-language-server: $schema=https://json.schemastore.org/kustomization
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    resources:
      - ./helmrelease.yaml
      # - ./secret.yaml
    ```

6.  **Create `app/helmrelease.yaml`**: This defines the Helm chart deployment.

    **Template `app/helmrelease.yaml`**:
    ```yaml
    ---
    # yaml-language-server: $schema=https://raw.githubusercontent.com/bjw-s-labs/helm-charts/main/charts/other/app-template/schemas/helmrelease-helm-v2.schema.json
    apiVersion: helm.toolkit.fluxcd.io/v2
    kind: HelmRelease
    metadata:
      name: my-app
    spec:
      interval: 1h
      chartRef:
        kind: OCIRepository
        name: app-template # Or specific chart name
        namespace: flux-system
      values:
        controllers:
          my-app:
            containers:
              app:
                image:
                  repository: ghcr.io/my-org/my-app
                  tag: latest
    ```

7.  **Register the App**: Add your new app directory to the namespace-level `kustomization.yaml` (`kubernetes/apps/<namespace>/kustomization.yaml`).

    ```yaml
    resources:
      - ./namespace.yaml
      - ./existing-app/ks.yaml
      - ./my-app/ks.yaml  # <--- Add this line
    ```

### 3. Commit and Push

Commit your changes. Flux will detect the changes in the git repository and reconcile the new application.
