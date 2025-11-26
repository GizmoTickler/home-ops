# VolSync Migration Checklist

When migrating apps with VolSync backups to new storage, follow these steps to prevent backup failures:

## Migration Steps

1. **Delete the app deployment** (keeps PVC alive for backup)
2. **Delete the ReplicationSource** (stops scheduled backups)
   ```bash
   kubectl delete replicationsource -n <namespace> <app>-src
   ```
3. **Delete the ReplicationDestination** (allows recreation with new config)
   ```bash
   kubectl delete replicationdestination -n <namespace> <app>-dst
   ```
4. **Delete the PVC** (will be recreated with new storage class)
   ```bash
   kubectl delete pvc -n <namespace> <app>
   ```
5. **Reconcile the Flux Kustomization** (recreates all resources)
   ```bash
   flux reconcile kustomization <app> -n <namespace>
   ```
6. **Verify ReplicationDestination has correct copyMethod**
   ```bash
   kubectl get replicationdestination -n <namespace> <app>-dst -o jsonpath='{.spec.kopia.copyMethod}'
   ```
7. **Trigger restore** (if needed)
   ```bash
   kubectl patch replicationdestination -n <namespace> <app>-dst --type merge -p '{"spec":{"trigger":{"manual":"restore-'$(date +%s)'"}}}'
   ```
8. **Wait for restore to complete** (check status)
   ```bash
   kubectl get replicationdestination -n <namespace> <app>-dst -o jsonpath='{.status.latestMoverStatus.result}'
   ```
9. **Apply Helm manifest** (if Flux SSA doesn't create deployment)
   ```bash
   helm get manifest <app> -n <namespace> | kubectl apply -f -
   ```
10. **Verify data restored** (check actual data size)
    ```bash
    kubectl exec -n <namespace> deployment/<app> -- du -sh /data
    ```

## Cleanup Stuck Backup Pods

If backup pods are stuck in Pending state after migration:

```bash
# List stuck volsync-src pods
kubectl get pod -A | grep volsync-src | grep Pending

# Force delete stuck pods
kubectl delete pod -n <namespace> <volsync-src-pod> --force --grace-period=0
```

The backup will automatically retry on the next scheduled run (hourly at :00).

## Why This Happens

With Direct copyMethod on iSCSI storage:
- Both app pod and backup pod must mount the same RWO PVC
- Both pods must run on the same node (due to RWO + iSCSI node affinity)
- Old backup pods from before migration reference deleted PVCs
- Pod scheduling fails due to missing PVC or node affinity mismatch

## Prevention

Always delete the ReplicationSource **before** deleting the PVC to ensure no backup pods are left running or scheduled.
