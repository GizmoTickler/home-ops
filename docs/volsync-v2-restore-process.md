# VolSync Data Restore Process for Longhorn v2 Data Engine

## Background
Longhorn v2 data engine does not support volume cloning from snapshots, which breaks VolSync's automatic restore process using `dataSourceRef`. This document outlines the manual restore process for migrating data from VolSync backup PVCs to application PVCs using Longhorn v2.

## Prerequisites
- VolSync ReplicationDestination jobs have completed successfully
- `volsync-{APP}-dst-dest` PVCs contain the restored data
- Application is currently commented out in kustomization.yaml
- `components/volsync/pvc.yaml` has `dataSourceRef` commented out

## Step-by-Step Restore Process

### 1. Prepare the PVC Component
Ensure the `dataSourceRef` is commented out in `components/volsync/pvc.yaml`:

```yaml
spec:
  accessModes:
    - "${VOLSYNC_ACCESSMODES:=ReadWriteOnce}"
  # dataSourceRef:
  #   kind: ReplicationDestination
  #   apiGroup: volsync.backube
  #   name: "${APP}-dst"
  resources:
    requests:
      storage: "${VOLSYNC_CAPACITY:=5Gi}"
  storageClassName: "${VOLSYNC_STORAGECLASS:=longhorn-v2-default}"
```

### 2. Enable the Application
Uncomment the application in the appropriate kustomization.yaml file:

```bash
# Edit kubernetes/apps/{namespace}/kustomization.yaml
# Change:
#   # - ./{app}/ks.yaml
# To:
#   - ./{app}/ks.yaml
```

### 3. Commit and Push Changes
```bash
git add -A
git commit -m "feat: enable {app} for v2 restore"
git push
```

### 4. Force Flux Reconciliation
```bash
flux reconcile source git flux-system -n flux-system
```

### 5. Verify PVC Creation
Wait for the application PVC to be created and bound:

```bash
kubectl get pvc -n {namespace} | grep {app}
# Should show:
# {app}                         Bound    pvc-xxx   {size}   RWO   longhorn-v2-default
# volsync-{app}-dst-dest        Bound    pvc-yyy   {size}   RWO   longhorn-snapshot
```

### 6. Scale Down the Application
Scale the application to 0 to release the PVC:

```bash
kubectl scale deployment {app} -n {namespace} --replicas=0
```

### 7. Create Data Copy Pod
Create a temporary pod to copy data from VolSync PVC to application PVC:

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: data-copy-{app}
  namespace: {namespace}
spec:
  containers:
  - name: copier
    image: busybox:latest
    command: ['sh', '-c', 'echo "Starting data copy..."; cp -av /source/* /dest/ 2>/dev/null || true; echo "Data copy complete"; sleep infinity']
    volumeMounts:
    - name: source
      mountPath: /source
    - name: dest
      mountPath: /dest
  volumes:
  - name: source
    persistentVolumeClaim:
      claimName: volsync-{app}-dst-dest
  - name: dest
    persistentVolumeClaim:
      claimName: {app}
EOF
```

### 8. Monitor Data Copy
Check the logs to ensure data is copied:

```bash
kubectl logs data-copy-{app} -n {namespace}
# Should show files being copied
```

### 9. Verify Data Transfer
Verify the data exists in the destination:

```bash
kubectl exec data-copy-{app} -n {namespace} -- ls -la /dest/
```

### 10. Cleanup and Scale Up
Delete the temporary pod and scale the application back up:

```bash
kubectl delete pod data-copy-{app} -n {namespace}
kubectl scale deployment {app} -n {namespace} --replicas=1
```

### 11. Verify Application is Running
```bash
kubectl get pod -n {namespace} | grep {app}
# Should show the pod as Running
```

## Batch Processing Multiple Apps

For restoring multiple applications, you can script the process:

```bash
#!/bin/bash
NAMESPACE="default"
APPS=("actual" "atuin" "autobrr" "bazarr" "cross-seed" "jellyseerr" "karakeep" "n8n" "ocis" "pinchflat" "prowlarr" "qbittorrent" "radarr" "recyclarr" "sabnzbd" "sonarr" "thelounge")

for APP in "${APPS[@]}"; do
    echo "Processing $APP..."
    
    # Scale down
    kubectl scale deployment $APP -n $NAMESPACE --replicas=0
    
    # Wait for pod to terminate
    kubectl wait --for=delete pod -l app.kubernetes.io/name=$APP -n $NAMESPACE --timeout=60s 2>/dev/null || true
    
    # Create copy pod
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: data-copy-$APP
  namespace: $NAMESPACE
spec:
  containers:
  - name: copier
    image: busybox:latest
    command: ['sh', '-c', 'cp -av /source/* /dest/ 2>/dev/null || true; sleep 5']
    volumeMounts:
    - name: source
      mountPath: /source
    - name: dest
      mountPath: /dest
  volumes:
  - name: source
    persistentVolumeClaim:
      claimName: volsync-$APP-dst-dest
  - name: dest
    persistentVolumeClaim:
      claimName: $APP
EOF
    
    # Wait for copy to complete
    kubectl wait --for=condition=ready pod/data-copy-$APP -n $NAMESPACE --timeout=60s 2>/dev/null || true
    sleep 10
    
    # Cleanup and scale up
    kubectl delete pod data-copy-$APP -n $NAMESPACE
    kubectl scale deployment $APP -n $NAMESPACE --replicas=1
    
    echo "$APP restored!"
done
```

## Important Notes

1. **Storage Class**: Ensure all PVCs use `longhorn-v2-default` for v2 data engine benefits
2. **Data Integrity**: Always verify data copy completion before scaling applications back up
3. **Cleanup**: Remove temporary pods after data copy to avoid resource waste
4. **VolSync Source Jobs**: The `volsync-src-{app}` jobs may remain pending if v1 engine is disabled - this is expected
5. **Cache PVCs**: Cache PVCs (like `jellyseerr-cache`, `radarr-cache`, `sonarr-cache`) don't need restore as they're temporary data

## Troubleshooting

### PVC Won't Bind
- Check if v1 data engine is disabled: `kubectl get settings.longhorn.io v1-data-engine -n longhorn-system -o yaml`
- Verify storage class exists: `kubectl get storageclass longhorn-v2-default`

### Copy Pod Stuck in ContainerCreating
- Check if application is still running and holding the PVC
- Verify both source and destination PVCs exist and are bound

### Data Not Copying
- Check VolSync ReplicationDestination has completed: `kubectl get replicationdestination -n {namespace}`
- Verify source PVC has data: `kubectl exec {any-pod-mounting-it} -- ls -la /path/`

## Reverting to v1 Engine (Emergency Only)
If you need to temporarily enable v1 engine:

```bash
kubectl patch settings.longhorn.io v1-data-engine -n longhorn-system --type='merge' -p '{"value":"true"}'
```

Remember to disable it after migration is complete to maintain v2 performance benefits.