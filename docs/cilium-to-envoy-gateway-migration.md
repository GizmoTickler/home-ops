# Cilium to Envoy Gateway Migration Plan

## Overview
This document outlines the migration strategy from Cilium Gateway API to Envoy Gateway for the home-ops Kubernetes cluster.

## Current State
- **Cilium Gateway**: Managing ingress traffic with Gateway API
  - Shared gateways: `internal` (192.168.120.102) and `external` (192.168.120.103) in `kube-system`
  - Per-app gateways using component template with individual IPs
- **DNS Management**:
  - Internal DNS: Unifi DNS
  - External DNS: Cloudflare DNS
- **TLS**: cert-manager with Google Trust Services (GTS) issuer

## Target State
- **Envoy Gateway**: Replace Cilium Gateway API with Envoy Gateway
  - Shared gateways: `envoy-internal` and `envoy-external` in `network` namespace
  - Better observability with native Envoy features
  - Advanced traffic management capabilities

## Pilot Test Results (it-tools)
Successfully deployed and tested Envoy Gateway with it-tools application:
- ✅ Envoy Gateway deployed in `network` namespace
- ✅ CRDs installed via separate kustomization for proper dependency management
- ✅ TLS certificate provisioned via cert-manager
- ✅ HTTPRoute working with Envoy Gateway
- ✅ DNS records created in Unifi DNS
- ✅ Coexistence with Cilium Gateway proven

## Migration Phases

### Phase 1: Preparation (Completed)
- [x] Deploy Envoy Gateway alongside Cilium Gateway
- [x] Configure separate DNS names to avoid conflicts
  - `envoy-internal.achva.casa` (192.168.123.140)
  - `envoy-external.achva.casa` (192.168.123.141)
- [x] Test with pilot application (it-tools)

### Phase 2: Application Migration
Migrate applications in order of complexity:

#### Low Risk Applications (Start Here)
1. **Observability Stack**
   - grafana-gateway → Envoy
   - kube-prometheus-gateway → Envoy

2. **Utility Applications**
   - echo → Envoy
   - webhook-gateway → Envoy

#### Medium Risk Applications
3. **Media Stack**
   - jellyseerr-gateway → Envoy
   - sonarr-gateway → Envoy
   - radarr-gateway → Envoy
   - bazarr-gateway → Envoy
   - prowlarr-gateway → Envoy

4. **Productivity Tools**
   - actual-gateway → Envoy
   - atuin-gateway → Envoy
   - fusion-gateway → Envoy

#### High Risk Applications
5. **Core Services**
   - ocis-gateway → Envoy
   - n8n-gateway → Envoy
   - kopia-gateway → Envoy

6. **Download Services**
   - qbittorrent-gateway → Envoy
   - nzbget-gateway → Envoy
   - autobrr-gateway → Envoy

### Phase 3: Gateway Consolidation
1. Update DNS to point to Envoy gateways
   - `internal.achva.casa` → 192.168.123.140 (envoy-internal)
   - `external.achva.casa` → 192.168.123.141 (envoy-external)
2. Remove per-app Cilium gateways
3. Keep Cilium shared gateways as fallback

### Phase 4: Cleanup
1. Remove Cilium Gateway API components
2. Update documentation
3. Clean up unused resources

## Migration Steps per Application

For each application migration:

1. **Update HelmRelease**
   ```yaml
   # Change from:
   gateway:
     app:
       className: cilium
       annotations:
         cert-manager.io/cluster-issuer: gts-production
       listeners:
         - name: https
           hostname: "${APP}.${SECRET_DOMAIN}"
           port: 443
           protocol: HTTPS

   # To:
   route:
     app:
       hostnames:
         - "{{ .Release.Name }}.${SECRET_DOMAIN}"
       parentRefs:
         - name: envoy-internal  # or envoy-external
           namespace: network
           sectionName: https
   ```

2. **Remove Gateway Component**
   ```yaml
   # Remove from kustomization.yaml:
   components:
     - ../../components/gateway
   ```

3. **Test Application**
   - Verify HTTPRoute is accepted
   - Check DNS resolution
   - Test application connectivity
   - Monitor for errors

4. **Rollback if Needed**
   - Revert HelmRelease changes
   - Re-add gateway component
   - Reconcile kustomization

## Configuration Templates

### Envoy Gateway Configuration
Located in: `kubernetes/apps/network/envoy-gateway/`

Key files:
- `app/envoy.yaml` - Gateway definitions and policies
- `app/certificate.yaml` - TLS certificate
- `app/observability.yaml` - Monitoring configuration

### Application Route Template
```yaml
route:
  app:
    hostnames:
      - "{{ .Release.Name }}.${SECRET_DOMAIN}"
    parentRefs:
      - name: envoy-internal  # Use envoy-external for public services
        namespace: network
        sectionName: https
```

## Monitoring During Migration

### Key Metrics to Watch
1. **Gateway Health**
   - Pod status: `kubectl get pods -n network | grep envoy`
   - Gateway status: `kubectl get gateways -n network`
   - Service IPs: `kubectl get svc -n network | grep envoy`

2. **HTTPRoute Status**
   ```bash
   kubectl get httproutes -A -o custom-columns=\
   NAMESPACE:.metadata.namespace,\
   NAME:.metadata.name,\
   HOSTNAMES:.spec.hostnames,\
   PARENT:.spec.parentRefs[0].name
   ```

3. **Application Connectivity**
   - Test each application after migration
   - Check logs for errors
   - Verify certificate status

### Troubleshooting Commands
```bash
# Check gateway status
kubectl describe gateway envoy-internal -n network

# Check HTTPRoute acceptance
kubectl get httproute <app-name> -n <namespace> -o jsonpath='{.status.parents[*]}'

# View Envoy Gateway logs
kubectl logs -n network deployment/envoy-gateway

# Check DNS records
nslookup <app>.achva.casa <unifi-dns-ip>
```

## Rollback Procedures

### Application Level Rollback
1. Revert HelmRelease to use Cilium gateway
2. Re-add gateway component to kustomization
3. Commit and push changes
4. Reconcile: `flux reconcile kustomization <app-name> -n <namespace>`

### Full Rollback
1. Restore all applications to Cilium gateways
2. Update DNS records back to Cilium IPs
3. Scale down Envoy Gateway: `kubectl scale deployment envoy-gateway -n network --replicas=0`
4. Remove Envoy Gateway kustomization if needed

## Benefits of Migration

1. **Better Observability**
   - Native Envoy metrics and tracing
   - Detailed access logs
   - Integration with existing observability stack

2. **Advanced Features**
   - Rate limiting
   - Circuit breaking
   - Request/response transformation
   - Advanced load balancing algorithms

3. **Performance**
   - Optimized for L7 proxy workloads
   - Better resource utilization
   - Lower latency for HTTP/HTTPS traffic

4. **Ecosystem**
   - Wide adoption in cloud-native environments
   - Extensive documentation and community support
   - Regular updates and security patches

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Service disruption during migration | Migrate one app at a time, test thoroughly |
| DNS propagation delays | Use internal DNS first, update external DNS after validation |
| Certificate issues | Pre-create certificates in target namespace |
| IP address conflicts | Use separate IP ranges for Envoy gateways |
| Rollback complexity | Document each change, use Git for version control |

## Success Criteria

- [ ] All applications migrated to Envoy Gateway
- [ ] No service disruptions reported
- [ ] DNS records updated and resolving correctly
- [ ] Monitoring and alerting functional
- [ ] Documentation updated
- [ ] Team trained on Envoy Gateway operations

## Timeline Estimate

- Phase 1: ✅ Completed
- Phase 2: 2-3 weeks (1-2 apps per day with testing)
- Phase 3: 1 week (DNS updates and validation)
- Phase 4: 1 week (cleanup and documentation)

**Total: 4-5 weeks for complete migration**

## Next Steps

1. Review and approve migration plan
2. Schedule maintenance windows for critical services
3. Begin Phase 2 with low-risk applications
4. Monitor and adjust plan based on learnings

---
*Last Updated: September 18, 2025*
*Author: Migration Planning Team*