// src/index.js
var index_default = {
  async fetch(request, env) {
    // Handle CORS preflight
    if (request.method === "OPTIONS") {
      return new Response(null, {
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Access-Control-Allow-Methods": "GET",
          "Access-Control-Max-Age": "86400"
        }
      });
    }

    // Only allow GET requests
    if (request.method !== "GET") {
      return new Response("Method not allowed", { status: 405 });
    }

    // Validate environment variables
    if (!env.CF_CLIENT_ID || !env.CF_CLIENT_SECRET || !env.SECRET_DOMAIN) {
      return new Response(JSON.stringify({
        schemaVersion: 1,
        label: "error",
        message: "misconfigured",
        color: "critical"
      }), {
        status: 500,
        headers: { "Content-Type": "application/json" }
      });
    }

    const url = new URL(request.url);

    // Whitelist of allowed metrics
    const allowedMetrics = [
      "talos_version",
      "kubernetes_version",
      "flux_version",
      "cluster_node_count",
      "cluster_pod_count",
      "cluster_cpu_usage",
      "cluster_memory_usage",
      "cluster_age_days",
      "cluster_uptime_days",
      "cluster_alert_count",
      "ceph_storage_used",
      "ceph_health",
      "cert_expiry_days",
      "flux_failing_count",
      "helmrelease_count",
      "pvc_count",
      "container_count"
    ];

    const metricName = url.pathname.substring(1);

    // Validate metric name
    if (!metricName || !allowedMetrics.includes(metricName)) {
      return new Response(JSON.stringify({
        schemaVersion: 1,
        label: "error",
        message: "not found",
        color: "red"
      }), {
        status: 404,
        headers: { "Content-Type": "application/json" }
      });
    }

    try {
      const kromgoUrl = `https://kromgo.${env.SECRET_DOMAIN}/${metricName}`;

      const response = await fetch(kromgoUrl, {
        headers: {
          "CF-Access-Client-Id": env.CF_CLIENT_ID,
          "CF-Access-Client-Secret": env.CF_CLIENT_SECRET
        },
        signal: AbortSignal.timeout(5000)
      });

      const contentType = response.headers.get("content-type") || "";

      // Detect auth failure (HTML response instead of JSON)
      if (contentType.includes("text/html")) {
        return new Response(JSON.stringify({
          schemaVersion: 1,
          label: "kromgo",
          message: "auth failed",
          color: "critical"
        }), {
          status: 503,
          headers: {
            "Content-Type": "application/json",
            "Cache-Control": "no-cache"
          }
        });
      }

      if (!response.ok) {
        return new Response(JSON.stringify({
          schemaVersion: 1,
          label: "error",
          message: "unavailable",
          color: "lightgrey"
        }), {
          status: 503,
          headers: {
            "Content-Type": "application/json",
            "Cache-Control": "no-cache"
          }
        });
      }

      const data = await response.json();

      return new Response(JSON.stringify(data), {
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "Access-Control-Allow-Origin": "*",
          "Cache-Control": "public, max-age=60",
          "X-Robots-Tag": "noindex",
          "Referrer-Policy": "no-referrer",
          "X-Content-Type-Options": "nosniff"
        }
      });
    } catch (error) {
      return new Response(JSON.stringify({
        schemaVersion: 1,
        label: "error",
        message: "timeout",
        color: "lightgrey"
      }), {
        status: 503,
        headers: {
          "Content-Type": "application/json",
          "Cache-Control": "no-cache"
        }
      });
    }
  }
};

export {
  index_default as default
};