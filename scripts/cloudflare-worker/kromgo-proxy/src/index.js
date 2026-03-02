// src/index.js — Kromgo badge proxy
// Generates SVG badges directly, bypassing shields.io caching

const COLORS = {
  green: "#44cc11",
  brightgreen: "#44cc11",
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

// Verdana Bold 11px character widths — measured from shields.io reference renders
// These are per-character widths for uppercase text at 110 DPI
const CHAR_WIDTHS = {
  " ": 3.58, "!": 4.57, '"': 5.73, "#": 8.46, $: 6.86, "%": 9.56, "&": 8.09,
  "'": 3.07, "(": 4.57, ")": 4.57, "*": 6.86, "+": 8.46, ",": 3.58, "-": 4.57,
  ".": 3.58, "/": 4.86, 0: 6.86, 1: 6.86, 2: 6.86, 3: 6.86, 4: 6.86, 5: 6.86,
  6: 6.86, 7: 6.86, 8: 6.86, 9: 6.86, ":": 4.01, ";": 4.01, "<": 8.46,
  "=": 8.46, ">": 8.46, "?": 6.18, "@": 10.56, A: 7.73, B: 7.73, C: 7.37,
  D: 8.41, E: 7.01, F: 6.47, G: 8.41, H: 8.41, I: 5.07, J: 5.57, K: 7.73,
  L: 6.47, M: 9.56, N: 8.41, O: 8.41, P: 7.01, Q: 8.41, R: 7.73, S: 7.01,
  T: 6.47, U: 8.41, V: 7.73, W: 10.56, X: 7.37, Y: 6.86, Z: 7.01,
};

function measureText(text) {
  let width = 0;
  for (const ch of text) {
    width += CHAR_WIDTHS[ch] || CHAR_WIDTHS[ch.toUpperCase()] || 7.5;
  }
  return width;
}

function makeBadge(label, message, color) {
  label = (label || "").toUpperCase();
  message = (message || "").toUpperCase();
  const hex = resolveColor(color);

  const HEIGHT = 28;
  const HPAD = 9; // horizontal padding each side
  const FONT_SIZE = 11;
  const TEXT_Y = 17.5; // vertical center for text baseline

  const labelTextW = measureText(label);
  const messageTextW = measureText(message);

  const labelW = Math.round((labelTextW + HPAD * 2) * 10) / 10;
  const messageW = Math.round((messageTextW + HPAD * 2) * 10) / 10;
  const totalW = Math.round((labelW + messageW) * 10) / 10;

  const labelX = Math.round((labelW / 2) * 10) / 10;
  const messageX = Math.round((labelW + messageW / 2) * 10) / 10;

  // Drop shadow for readability (matches shields.io)
  return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${totalW}" height="${HEIGHT}" role="img" aria-label="${escapeXml(label)}: ${escapeXml(message)}">
  <title>${escapeXml(label)}: ${escapeXml(message)}</title>
  <g shape-rendering="crispEdges">
    <rect width="${labelW}" height="${HEIGHT}" fill="#555"/>
    <rect x="${labelW}" width="${messageW}" height="${HEIGHT}" fill="${hex}"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="${FONT_SIZE}">
    <text x="${labelX}" y="${TEXT_Y}" fill="#010101" fill-opacity=".3" font-weight="bold">${escapeXml(label)}</text>
    <text x="${labelX}" y="${TEXT_Y - 1}" font-weight="bold">${escapeXml(label)}</text>
    <text x="${messageX}" y="${TEXT_Y}" fill="#010101" fill-opacity=".3" font-weight="bold">${escapeXml(message)}</text>
    <text x="${messageX}" y="${TEXT_Y - 1}" font-weight="bold">${escapeXml(message)}</text>
  </g>
</svg>`;
}

function escapeXml(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
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

      const label = url.searchParams.get("label") || data.label || metricName;
      const color = url.searchParams.get("color") || data.color;
      return svgResponse(makeBadge(label, data.message, color), 200);
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
