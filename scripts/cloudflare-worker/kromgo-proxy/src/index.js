// src/index.js — Kromgo badge proxy
// Generates SVG badges directly, bypassing shields.io caching

// Color name → hex mapping (shields.io compatible)
const COLORS = {
  green: "#4c1",
  brightgreen: "#4c1",
  yellowgreen: "#a4a61d",
  yellow: "#dfb317",
  orange: "#fe7d37",
  red: "#e05d44",
  blue: "#007ec6",
  lightgrey: "#9f9f9f",
  gray: "#9f9f9f",
  grey: "#9f9f9f",
  critical: "#e05d44",
};

function resolveColor(color) {
  if (!color) return COLORS.lightgrey;
  if (color.startsWith("#")) return color;
  return COLORS[color.toLowerCase()] || COLORS.lightgrey;
}

// Approximate character width for Verdana 11px (for-the-badge uses uppercase)
function textWidth(text) {
  // Average character width for Verdana bold 11px uppercase
  const AVG_CHAR_WIDTH = 7.5;
  let width = 0;
  for (const ch of text) {
    if (ch === " ") width += 3.5;
    else if ("MWØÆ".includes(ch)) width += 10;
    else if ("mw%".includes(ch.toLowerCase())) width += 9.5;
    else if ("NDGQOUHAB".includes(ch)) width += 8.5;
    else if ("Il1i!|.:".includes(ch)) width += 4;
    else width += AVG_CHAR_WIDTH;
  }
  return width;
}

function makeBadge(label, message, color) {
  label = (label || "").toUpperCase();
  message = (message || "").toUpperCase();
  const hex = resolveColor(color);

  const PADDING = 18;
  const HEIGHT = 28;
  const FONT_SIZE = 11;

  const labelW = textWidth(label) + PADDING;
  const messageW = textWidth(message) + PADDING;
  const totalW = labelW + messageW;

  const labelX = labelW / 2;
  const messageX = labelW + messageW / 2;

  return `<svg xmlns="http://www.w3.org/2000/svg" width="${totalW}" height="${HEIGHT}" role="img">
  <title>${label}: ${message}</title>
  <g shape-rendering="crispEdges">
    <rect width="${labelW}" height="${HEIGHT}" fill="#555"/>
    <rect x="${labelW}" width="${messageW}" height="${HEIGHT}" fill="${hex}"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="${FONT_SIZE}">
    <text x="${labelX}" y="${HEIGHT / 2 + 4}" font-weight="bold" textLength="${labelW - PADDING}" lengthAdjust="">${escapeXml(label)}</text>
    <text x="${messageX}" y="${HEIGHT / 2 + 4}" font-weight="bold" textLength="${messageW - PADDING}" lengthAdjust="">${escapeXml(message)}</text>
  </g>
</svg>`;
}

function escapeXml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

const ALLOWED_METRICS = new Set([
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
  "container_count",
  "wan_primary",
  "wan_cellular1",
  "wan_cellular2",
]);

var index_default = {
  async fetch(request, env) {
    if (request.method === "OPTIONS") {
      return new Response(null, {
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Access-Control-Allow-Methods": "GET",
          "Access-Control-Max-Age": "86400",
        },
      });
    }

    if (request.method !== "GET") {
      return new Response("Method not allowed", { status: 405 });
    }

    if (!env.CF_CLIENT_ID || !env.CF_CLIENT_SECRET || !env.SECRET_DOMAIN) {
      return svgResponse(makeBadge("error", "misconfigured", "critical"), 500);
    }

    const url = new URL(request.url);
    const metricName = url.pathname.substring(1);

    if (!metricName || !ALLOWED_METRICS.has(metricName)) {
      return svgResponse(makeBadge("error", "not found", "red"), 404);
    }

    // Check if caller wants JSON (shields.io endpoint format) via query param
    const wantJson = url.searchParams.has("json");

    try {
      const kromgoUrl = `https://kromgo.${env.SECRET_DOMAIN}/${metricName}`;

      const response = await fetch(kromgoUrl, {
        headers: {
          "CF-Access-Client-Id": env.CF_CLIENT_ID,
          "CF-Access-Client-Secret": env.CF_CLIENT_SECRET,
        },
        signal: AbortSignal.timeout(5000),
      });

      const contentType = response.headers.get("content-type") || "";

      if (contentType.includes("text/html")) {
        return wantJson
          ? jsonResponse({ schemaVersion: 1, label: "kromgo", message: "auth failed", color: "critical" }, 503)
          : svgResponse(makeBadge("kromgo", "auth failed", "critical"), 503);
      }

      if (!response.ok) {
        return wantJson
          ? jsonResponse({ schemaVersion: 1, label: "error", message: "unavailable", color: "lightgrey" }, 503)
          : svgResponse(makeBadge("error", "unavailable", "lightgrey"), 503);
      }

      const data = await response.json();

      if (wantJson) {
        return jsonResponse(data, 200);
      }

      // Use label override from query param, or Kromgo's label field
      const label = url.searchParams.get("label") || data.label || metricName;
      return svgResponse(makeBadge(label, data.message, data.color), 200);
    } catch (error) {
      return wantJson
        ? jsonResponse({ schemaVersion: 1, label: "error", message: "timeout", color: "lightgrey" }, 503)
        : svgResponse(makeBadge("error", "timeout", "lightgrey"), 503);
    }
  },
};

function svgResponse(svg, status) {
  return new Response(svg, {
    status,
    headers: {
      "Content-Type": "image/svg+xml",
      "Access-Control-Allow-Origin": "*",
      "Cache-Control": "no-cache, max-age=0",
      "X-Robots-Tag": "noindex",
      "Referrer-Policy": "no-referrer",
      "X-Content-Type-Options": "nosniff",
    },
  });
}

function jsonResponse(data, status) {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      "Content-Type": "application/json",
      "Access-Control-Allow-Origin": "*",
      "Cache-Control": "no-cache, max-age=0",
      "X-Robots-Tag": "noindex",
    },
  });
}

export { index_default as default };
