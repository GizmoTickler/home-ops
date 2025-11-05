# Kromgo Proxy - Cloudflare Worker

This Cloudflare Worker acts as a secure proxy for your internal Kromgo metrics, allowing you to use shields.io badges in your README without exposing your actual domain name.

## Features

- ✅ **Domain Protection**: Your actual domain is never exposed in responses
- ✅ **Security Whitelist**: Only allowed metrics can be accessed
- ✅ **Error Sanitization**: No internal error details leaked
- ✅ **Rate Limiting Ready**: Designed to add KV-based rate limiting
- ✅ **Caching**: 5-minute cache to reduce load on internal service
- ✅ **shields.io Compatible**: Returns proper endpoint badge format

## Prerequisites

- Cloudflare account (free tier works fine)
- Node.js and npm installed
- Access to your internal Kromgo instance

## Installation

### 1. Install Wrangler CLI

```bash
npm install -g wrangler
```

### 2. Login to Cloudflare

```bash
wrangler login
```

This will open a browser window to authenticate with Cloudflare.

### 3. Deploy the Worker

From this directory (`scripts/cloudflare-worker/kromgo-proxy`):

```bash
wrangler secret put SECRET_DOMAIN
```
# When prompted, enter your domain (e.g., `example.com`)
```bash
  wrangler secret put CF_CLIENT_ID
```
  # Paste the FULL Client ID with .access
```bash
   wrangler secret put CF_CLIENT_SECRET
```
  # Paste the FULL Client Secret
```bash
# Deploy to Cloudflare
wrangler deploy
```

### 4. Note Your Worker URL

After deployment, Wrangler will output your worker URL:

```
https://kromgo-proxy.<your-cloudflare-username>.workers.dev
```

Save this URL - you'll use it in your README badges!

## Testing

Test that your worker is functioning correctly:

```bash
# Test a metric endpoint
curl https://kromgo-proxy.<your-username>.workers.dev/talos_version

# Expected response format (shields.io endpoint):
{
  "schemaVersion": 1,
  "label": "Talos",
  "message": "1.8.3",
  "color": "blue"
}
```

### Verify No Domain Leakage

```bash
# Check for your domain in all responses
for metric in talos_version kubernetes_version flux_version; do
  echo "Testing $metric..."
  curl -v https://kromgo-proxy.<your-username>.workers.dev/$metric 2>&1 | grep -i "your-domain.com"
done

# Should return NOTHING - if it finds your domain, there's a leak!
```

## Usage in README Badges

Use the shields.io dynamic endpoint badge format:

```markdown
[![Talos](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.<your-username>.workers.dev%2Ftalos_version&style=for-the-badge&logo=talos&logoColor=white&color=blue&label=%20)](https://talos.dev)
```

**Note**: URL must be URL-encoded for shields.io. Use this format:
- Replace `/` with `%2F`
- Replace `:` with `%3A`

Example: `https://kromgo-proxy.user.workers.dev/talos_version` becomes:
```
https%3A%2F%2Fkromgo-proxy.user.workers.dev%2Ftalos_version
```

## Available Metrics

The worker exposes these metrics (defined in your kromgo config):

- `talos_version` - Talos Linux version
- `kubernetes_version` - Kubernetes version
- `flux_version` - Flux version
- `cluster_node_count` - Number of nodes
- `cluster_pod_count` - Number of running pods
- `cluster_cpu_usage` - CPU usage percentage
- `cluster_memory_usage` - Memory usage percentage
- `cluster_age_days` - Cluster age in days
- `cluster_uptime_days` - Cluster uptime in days
- `cluster_alert_count` - Active alert count

Any other paths will return a 404 error.

## Security Considerations

### What's Protected

✅ **Your domain name**: Never appears in responses or headers
✅ **Internal URLs**: Error messages are generic
✅ **Path traversal**: Only whitelisted metrics allowed
✅ **Method restriction**: Only GET requests accepted

### What's Public

⚠️ **Metric values**: Anyone can see your cluster metrics (CPU, memory, versions)
⚠️ **Worker URL**: Your Cloudflare username is in the worker URL

This is expected - the goal is to share metrics publicly while hiding your domain.

### Recommended Additional Security

1. **Enable Rate Limiting** (optional, requires Workers KV):

```bash
# Create KV namespace
wrangler kv:namespace create "RATE_LIMIT"

# Update wrangler.toml with the namespace ID
# Then uncomment the rate limiting code in src/index.js
```

2. **Cloudflare WAF Rules** (in Cloudflare Dashboard):

Go to Workers & Pages → kromgo-proxy → Settings → Add WAF rules:

```
# Rate limit: 100 requests per minute per IP
rate.requests.count > 100

# Block known bad bots
http.user_agent contains "scanner"
```

## Updating the Worker

When you modify kromgo metrics or need to update the worker:

```bash
# Make changes to src/index.js
# Then redeploy
wrangler deploy
```

Changes take effect immediately (within seconds).

## Troubleshooting

### Worker returns 503 "unavailable"

- Check that your kromgo service is running: `kubectl get pods -n observability`
- Verify SECRET_DOMAIN is set correctly: Re-run `wrangler secret put SECRET_DOMAIN`
- Check Cloudflare Worker logs in the dashboard

### Domain is leaking in responses

- Run the domain leakage test above
- Check that you're using the latest version of `src/index.js`
- Ensure no custom headers are being forwarded

### Badges not updating

- Shields.io caches responses - add `?dummy=123` to URLs to bypass cache
- Check worker cache (5 min default) - wait and retry
- Verify metric name matches exactly (case-sensitive)

## Cost

Cloudflare Workers free tier includes:
- **100,000 requests per day**
- **10ms CPU time per request**

For README badges, you'll use maybe 1000-2000 requests/day (assuming moderate traffic).

**Cost: $0/month** ✨

## Support

If you encounter issues:

1. Check Cloudflare Worker logs in the dashboard
2. Review kromgo logs: `kubectl logs -n observability -l app.kubernetes.io/name=kromgo`
3. Test kromgo directly (from inside cluster): `kubectl run -it --rm debug --image=curlimages/curl --restart=Never -- curl kromgo.observability.svc.cluster.local/talos_version`

## License

MIT - Feel free to use and modify for your own homelab!
