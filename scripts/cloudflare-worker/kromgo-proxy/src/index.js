/**
 * Cloudflare Worker: Kromgo Metrics Proxy
 *
 * Securely exposes Kubernetes cluster metrics from internal Kromgo instance
 * without revealing the actual domain. Metrics are used with shields.io badges.
 *
 * Security features:
 * - Whitelist of allowed metrics only (no path traversal)
 * - Strips all upstream headers (prevents domain leakage)
 * - Generic error messages (no internal details exposed)
 * - CORS headers for shields.io compatibility
 * - 5-minute cache to reduce load
 */

export default {
  async fetch(request, env) {
    // Handle CORS preflight requests
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        headers: {
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'GET',
          'Access-Control-Max-Age': '86400',
        }
      });
    }

    // Only allow GET requests
    if (request.method !== 'GET') {
      return new Response('Method not allowed', { status: 405 });
    }

    const url = new URL(request.url);

    // Whitelist of allowed metric endpoints (from config.yaml)
    const allowedMetrics = [
      'talos_version',
      'kubernetes_version',
      'flux_version',
      'cluster_node_count',
      'cluster_pod_count',
      'cluster_cpu_usage',
      'cluster_memory_usage',
      'cluster_age_days',
      'cluster_uptime_days',
      'cluster_alert_count'
    ];

    // Extract metric name from path (remove leading /)
    const metricName = url.pathname.substring(1);

    // Return 404 for any non-whitelisted metrics
    if (!metricName || !allowedMetrics.includes(metricName)) {
      return new Response(JSON.stringify({
        schemaVersion: 1,
        label: 'error',
        message: 'not found',
        color: 'red'
      }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    try {
      // Fetch from internal kromgo instance
      // SECRET_DOMAIN is set as a Cloudflare Worker secret (never exposed)
      const kromgoUrl = `https://kromgo.${env.SECRET_DOMAIN}/${metricName}`;

      const response = await fetch(kromgoUrl, {
        headers: {
          'User-Agent': 'CloudflareWorker-KromgoProxy/1.0',
        },
        // Timeout after 5 seconds
        signal: AbortSignal.timeout(5000)
      });

      if (!response.ok) {
        // Return a valid shields.io endpoint format on error
        return new Response(JSON.stringify({
          schemaVersion: 1,
          label: 'error',
          message: 'unavailable',
          color: 'lightgrey'
        }), {
          status: 503,
          headers: {
            'Content-Type': 'application/json',
            'Cache-Control': 'no-cache'
          }
        });
      }

      // Parse the kromgo response
      const data = await response.json();

      // Return clean response with ONLY the data
      // Strip all headers from upstream to prevent domain leakage
      return new Response(JSON.stringify(data), {
        status: 200,
        headers: {
          'Content-Type': 'application/json',
          'Access-Control-Allow-Origin': '*',
          'Cache-Control': 'public, max-age=300', // Cache for 5 minutes
          'X-Robots-Tag': 'noindex', // Don't index these endpoints
          'Referrer-Policy': 'no-referrer', // Don't leak referrer
        }
      });

    } catch (error) {
      // Log error to Cloudflare dashboard (not visible to users)
      console.error('Kromgo fetch failed:', error.message);

      // Return generic error in shields.io format
      return new Response(JSON.stringify({
        schemaVersion: 1,
        label: 'error',
        message: 'timeout',
        color: 'lightgrey'
      }), {
        status: 503,
        headers: {
          'Content-Type': 'application/json',
          'Cache-Control': 'no-cache'
        }
      });
    }
  }
}
