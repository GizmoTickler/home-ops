# Gateway Component with Simplified Variable Substitution

This gateway component template supports:
- Single hostname gateways using the app name as the subdomain
- Automatic certificate generation with Google Trust Services (GTS)
- Minimal variable overhead using creative reuse of existing variables

## Usage

### Basic Gateway Setup

For applications with standard hostname pattern (`${APP}.${SECRET_DOMAIN}`):

```yaml
spec:
  components:
    - ../../../../components/gateway
  postBuild:
    substitute:
      APP: *app
      NAMESPACE: *namespace
      GATEWAY_IP: "192.168.1.100"
    substituteFrom:
      - name: cluster-secrets
        kind: Secret
```

### Example App Configuration

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: &app prowlarr
  namespace: &namespace default
spec:
  components:
    - ../../../../components/gateway
  postBuild:
    substitute:
      APP: *app
      NAMESPACE: *namespace
      GATEWAY_IP: "192.168.120.107"
    substituteFrom:
      - name: cluster-secrets
        kind: Secret
```

This will create:
- Gateway: `prowlarr-gateway`
- Hostname: `prowlarr.${SECRET_DOMAIN}`
- Certificate: `prowlarr-tls`

### Custom Hostnames

For applications needing custom hostnames (not following the `${APP}.${SECRET_DOMAIN}` pattern), create dedicated gateway/certificate resources in the app directory instead of using this component.

## Component Template Variables

- `${APP}` - Used for gateway name, hostname subdomain, and TLS secret name
- `${NAMESPACE}` - Namespace for the Gateway and Certificate resources
- `${GATEWAY_IP}` - IP address for the Gateway listener
- `${SECRET_DOMAIN}` - Domain suffix (from cluster secrets)
      annotations:
        external-dns.alpha.kubernetes.io/hostname: 'myapp.${SECRET_DOMAIN}'
```

## Template Variables

- `APP`: Application name (used for resource naming)
- `NAMESPACE`: Target namespace
- `PRIMARY_HOSTNAME`: Primary hostname for both gateway listeners and certificate (e.g., "myapp.${SECRET_DOMAIN}")
- `ADDITIONAL_SANS`: Additional Subject Alternative Names for multi-hostname certificates (YAML list format)
- `GATEWAY_IP`: IP address for the gateway
- `SECRET_DOMAIN`: Domain from cluster secrets

## Migration Examples

### Backward Compatibility

Existing single-hostname applications can migrate by setting:
```yaml
PRIMARY_HOSTNAME: "${APP}.${SECRET_DOMAIN}"
ADDITIONAL_SANS: ""
```

### Jellyseerr Multi-Hostname Migration

To migrate Jellyseerr from shared gateway to per-app gateway:
```yaml
substitute:
  APP: jellyseerr
  NAMESPACE: default
  PRIMARY_HOSTNAME: "jellyseerr.${SECRET_DOMAIN}"
  ADDITIONAL_SANS: |
    - "requests.${SECRET_DOMAIN}"
  GATEWAY_IP: "192.168.120.105"
  # External DNS settings as needed
```

### Certificate Generation

The component automatically generates certificates with:
- **Single hostname**: Certificate with one SAN
- **Multi-hostname**: Certificate with multiple SANs
- **Issuer**: Google Trust Services (gts-production)
- **Algorithm**: ECDSA with 256-bit keys

**How it works:** When variables are empty, no external DNS annotations are added to the gateway. When populated, the annotations enable external access via Cloudflare tunnel.