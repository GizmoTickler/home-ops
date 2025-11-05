# Kromgo Proxy - Quick Start Guide

Get your kromgo metrics exposed publicly in under 5 minutes without revealing your domain!

## Prerequisites

- Cloudflare account (free tier is fine)
- Your cluster must have kromgo running and accessible internally
- Node.js and npm installed (for Wrangler CLI)

## Step-by-Step Deployment

### 1. Install Wrangler

```bash
npm install -g wrangler
```

### 2. Login to Cloudflare

```bash
wrangler login
```

A browser window will open - authenticate with your Cloudflare account.

### 3. Navigate to the Worker Directory

```bash
cd scripts/cloudflare-worker/kromgo-proxy
```

### 4. Deploy the Worker

# Your actual domain needs to be configured as a secret (encrypted, never visible):

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

You'll see output like:

```
✨ Built successfully
🌎 Uploading...
✨ Uploaded kromgo-proxy (0.XX sec)
✨ Published kromgo-proxy
  https://kromgo-proxy.YOUR-USERNAME.workers.dev
```

**SAVE THIS URL!** You'll need it for badges.

### 5. Test the Deployment

Test that a metric endpoint works:

```bash
# Replace YOUR-USERNAME with your actual Cloudflare username
curl https://kromgo-proxy.YOUR-USERNAME.workers.dev/talos_version
```

Expected response:

```json
{
  "schemaVersion": 1,
  "label": "Talos",
  "message": "1.8.3",
  "color": "blue"
}
```

### 6. Run the Security Test

Verify no domain leakage:

```bash
./test-deployment.sh
```

Follow the prompts and ensure all tests pass.

### 7. Add Badges to Your README

1. Copy your worker URL: `kromgo-proxy.YOUR-USERNAME.workers.dev`
2. URL-encode it:
   - `:` becomes `%3A`
   - `/` becomes `%2F`
   - Result: `kromgo-proxy.YOUR-USERNAME.workers.dev` (no changes needed for this part)
   - Full encoded URL: `https%3A%2F%2Fkromgo-proxy.YOUR-USERNAME.workers.dev`

3. Use the badge examples from `badge-examples.md`

Example badge:

```markdown
[![Talos](https://img.shields.io/endpoint?url=https%3A%2F%2Fkromgo-proxy.YOUR-USERNAME.workers.dev%2Ftalos_version&style=for-the-badge&logo=talos&logoColor=white&color=blue&label=%20)](https://talos.dev)
```

## Troubleshooting

### "Error: No account ID found"

Run `wrangler login` again to re-authenticate.

### Worker returns 503

- Verify kromgo is running: `kubectl get pods -n observability -l app.kubernetes.io/name=kromgo`
- Check if SECRET_DOMAIN is set correctly
- View worker logs in Cloudflare dashboard: Workers & Pages → kromgo-proxy → Logs

### Domain is leaking

Run the test script and check the output:

```bash
./test-deployment.sh
```

If domain leaks are detected, ensure you're using the latest `src/index.js` code.

### Badges not showing in README

- Check that the URL is properly URL-encoded
- Verify the metric endpoint returns data: `curl https://kromgo-proxy.YOUR-USERNAME.workers.dev/talos_version`
- shields.io caches responses - wait a few minutes or add a cache-busting parameter

## Security Checklist

✅ **Worker deployed and returning metrics**
✅ **SECRET_DOMAIN configured as encrypted secret**
✅ **Test script passes all checks**
✅ **No domain leakage detected**
✅ **Only whitelisted metrics accessible**
✅ **Invalid paths return 404**

## Next Steps

- Add badges to your README (see `badge-examples.md`)
- Configure Cloudflare WAF rules for rate limiting (optional)
- Enable Workers KV for advanced rate limiting (optional)
- Monitor usage in Cloudflare dashboard

## Cost

**$0/month** with Cloudflare Workers free tier:
- 100,000 requests/day
- More than enough for README badges

## Support

Issues or questions? Check:
- [README.md](./README.md) - Full documentation
- [badge-examples.md](./badge-examples.md) - Badge customization
- Cloudflare Worker logs in dashboard
- Kromgo logs: `kubectl logs -n observability -l app.kubernetes.io/name=kromgo`

Enjoy your shiny new cluster metrics badges! 🎉
