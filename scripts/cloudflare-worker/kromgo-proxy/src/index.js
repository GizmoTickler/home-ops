// src/index.js — Kromgo badge proxy
// Generates SVG badges matching shields.io "for-the-badge" style

const COLORS = {
  green: "#97ca00",
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

// Verdana Bold 10px character widths (shields.io for-the-badge reference)
// Measured from shields.io outputs at font-size 100 with scale(.1)
const WIDTHS = {
  " ": 33, "!": 39, "%": 86, ".": 33, "/": 46, "-": 40,
  0: 66, 1: 66, 2: 66, 3: 66, 4: 66, 5: 66, 6: 66, 7: 66, 8: 66, 9: 66,
  A: 73, B: 73, C: 70, D: 80, E: 66, F: 61, G: 80, H: 80, I: 46, J: 53,
  K: 73, L: 61, M: 93, N: 80, O: 80, P: 66, Q: 80, R: 73, S: 66, T: 61,
  U: 80, V: 73, W: 100, X: 70, Y: 66, Z: 66,
};

// Per-character extra spacing for "for-the-badge" style (in 10x units)
const LETTER_SPACING = 15;

function textWidth10x(text) {
  let w = 0;
  for (let i = 0; i < text.length; i++) {
    w += WIDTHS[text[i]] || WIDTHS[text[i].toUpperCase()] || 73;
    if (i < text.length - 1) w += LETTER_SPACING;
  }
  return w;
}

// Embedded SVG logos (Simple Icons, white fill, 24x24 viewBox)
const LOGOS = {
  kubernetes: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTEwLjIwNCAxNC4zNWwuMDA3LjAxLS45OTkgMi40MTNhNS4xNzEgNS4xNzEgMCAwIDEtMi4wNzUtMi41OTdsMi41NzgtLjQzNy4wMDQuMDA1YS40NC40NCAwIDAgMSAuNDg0LjYwNnptLS44MzMtMi4xMjlhLjQ0LjQ0IDAgMCAwIC4xNzMtLjc1NmwuMDAyLS4wMTFMNy41ODUgOS43YTUuMTQzIDUuMTQzIDAgMCAwLS43MyAzLjI1NWwyLjUxNC0uNzI1LjAwMi0uMDA5em0xLjE0NS0xLjk4YS40NC40NCAwIDAgMCAuNjk5LS4zMzdsLjAxLS4wMDUuMTUtMi42MmE1LjE0NCA1LjE0NCAwIDAgMC0zLjAxIDEuNDQybDIuMTQ3IDEuNTIzLjAwNC0uMDAzem0uNzYgMi43NWwuNzIzLjM0OS43MjItLjM0Ny4xOC0uNzgtLjUtLjYyM2gtLjgwNGwtLjUuNjIzLjE3OS43Nzl6bTEuNS0zLjA5NWEuNDQuNDQgMCAwIDAgLjcuMzM2bC4wMDguMDAzIDIuMTM0LTEuNTEzYTUuMTg4IDUuMTg4IDAgMCAwLTIuOTkyLTEuNDQybC4xNDggMi42MTUuMDAyLjAwMXoiLz48L3N2Zz4=",
  talos: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTkuNjc4IDExLjk4YzAtMi42NjQtMS4xMy02Ljg5Ni0yLjg2Ny0xMC44MDRhMTIgMTIgMCAwIDAtMS41ODUuOTE3YzEuNjA4IDMuNjY4IDIuNjQ3IDcuNTUzIDIuNjQ3IDkuODg2IDAgMi4yNTQtMS4wOCA2LjE0NS0yLjczNSA5Ljg2NWExMiAxMiAwIDAgMCAxLjU3Ni45M2MxLjc5LTMuOTc2IDIuOTY0LTguMjI5IDIuOTY0LTEwLjc5NW02LjQ0MiAwYzAtMi4zMzYgMS4wNDItNi4yMiAyLjY0Ni05Ljg5YTEyIDEyIDAgMCAwLTEuNjA4LS45MjJjLTEuNzU2IDMuOTU3LTIuODQzIDguMTY2LTIuODQzIDEwLjgxNiAwIDIuNTY0IDEuMTc3IDYuODE5IDIuOTY1IDEwLjc5N2ExMiAxMiAwIDAgMCAxLjU3NS0uOTMxYy0xLjY1NS0zLjcyMy0yLjczNS03LjYxNi0yLjczNS05Ljg3bTUuNDUgNi41MjUuMzEuMzA3YTEyIDEyIDAgMCAwIC45MzYtMS42MTJjLTEuODY2LTEuODkzLTMuNDU3LTMuOTM4LTMuNDctNS4yMzMtLjAxMi0xLjI2NCAxLjU3LTMuMzA4IDMuNDQ2LTUuMjIyYTEyIDEyIDAgMCAwLS45NDUtMS42MDNsLS4yNTkuMjU4Yy0yLjczOSAyLjc2Ni00LjA2MyA0LjkyLTQuMDQ3IDYuNTgzLjAxNiAxLjY2MiAxLjMzMiAzLjgxIDQuMDI4IDYuNTIyTTIuNDExIDUuNDA1bC0uMjYtLjI1OWExMiAxMiAwIDAgMC0uOTQ2IDEuNjA4YzMuMTIzIDMuMTczIDMuNDUyIDQuNzA0IDMuNDQ4IDUuMjE3LS4wMTIgMS4zLTEuNjAzIDMuMzQtMy40NyA1LjIyOWExMiAxMiAwIDAgMCAuOTM5IDEuNjA4Yy4xMDYtLjEwNi4yMDctLjIwNC4zMS0uMzA4IDIuNjk0LTIuNzExIDQuMDEtNC44NDIgNC4wMjYtNi41MTZzLTEuMzA4LTMuODA5LTQuMDQ3LTYuNThNMTIuMDAyIDI0Yy4zMDMgMCAuNjAyLS4wMTYuODk4LS4wMzdWLjAzN0ExMiAxMiAwIDAgMCAxMiAwYy0uMzA0IDAtLjYwNS4wMTUtLjkwNS4wMzd2MjMuOTI1cS40NDguMDM1LjkwMy4wMzh6Ii8+PC9zdmc+",
  flux: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTExLjQwMiAyMy43NDdjLjE1NC4wNzUuMzA2LjE1NC40NTQuMjM4LjE4MS4wMzguMzcuMDA0LjUyNS0uMDk3bC4zODYtLjI1MWMtMS4yNDItLjgzMS0yLjYyMi0xLjI1MS0zLjk5OC0xLjYwMmwyLjYzMyAxLjcxMlptLTcuNDk1LTUuNzgzYTguMDg4IDguMDg4IDAgMCAxLS4yMjItLjIzNi42OTYuNjk2IDAgMCAwIC4xMTIgMS4wNzVsMi4zMDQgMS40OThjMS4wMTkuNDIyIDIuMDg1LjY4NiAzLjEzNC45NDQgMS42MzYuNDAzIDMuMi43OSA0LjU1NCAxLjcyOGwuNjk3LS40NTNjLTEuNTQxLTEuMTU4LTMuMzI3LTEuNjAyLTUuMDY1LTIuMDMtMi4wMzktLjUwMy0zLjk2NS0uOTc3LTUuNTE0LTIuNTI2Wm0xLjQxNC0xLjMyMi0uNjY1LjQzMmMuMDIzLjAyNC4wNDQuMDQ5LjA2OC4wNzMgMS43MDIgMS43MDIgMy44MjUgMi4yMjUgNS44NzcgMi43MzEgMS43NzguNDM4IDMuNDY5Ljg1NiA0LjkgMS45ODJsLjY4Mi0uNDQ0Yy0xLjYxMi0xLjM1Ny0zLjUzMi0xLjgzNC01LjM5NS0yLjI5My0yLjAxOS0uNDk3LTMuOTI2LS45NjktNS40NjctMi40ODFabTcuNTAyIDIuMDg0YzEuNTk2LjQxMiAzLjA5Ni45MDQgNC4zNjcgMi4wMzZsLjY3LS40MzZjLTEuNDg0LTEuMzk2LTMuMjY2LTEuOTUzLTUuMDM3LTIuNDAzdi44MDNabS42OTgtMi4zMzdhNjQuNjk1IDY0LjY5NSAwIDAgMS0uNjk4LS4xNzR2LjgwMmwuNTEyLjEyN2MyLjAzOS41MDMgMy45NjUuOTc4IDUuNTE0IDIuNTI2bC4wMDcuMDA5LjY2My0uNDMxYy0uMDQxLS4wNDItLjA3OS0uMDg2LS4xMjEtLjEyOC0xLjcwMi0xLjcwMS0zLjgyNC0yLjIyNS01Ljg3Ny0yLjczMVptLS42OTgtMS45Mjh2LjgxNmMuNjI0LjE5IDEuMjU1LjM0NyAxLjg3OS41MDEgMi4wMzkuNTAyIDMuOTY1Ljk3NyA1LjUxMyAyLjUyNi4wNzcuMDc3LjE1My4xNTcuMjI2LjIzOWEuNzA0LjcwNCAwIDAgMC0uMjM4LS45MTFsLTMuMDY0LTEuOTkyYy0uNzQ0LS4yNDUtMS41MDItLjQzMy0yLjI1MS0uNjE4YTMxLjQzNiAzMS40MzYgMCAwIDEtMi4wNjUtLjU2MVptLTEuNjQ2IDMuMDQ5Yy0xLjUyNi0uNC0yLjk2LS44ODgtNC4xODUtMS45NTVsLS42NzQuNDM5YzEuNDM5IDEuMzI2IDMuMTUxIDEuODggNC44NTkgMi4zMTl2LS44MDNabTAtMS43NzJhOC41NDMgOC41NDMgMCAwIDEtMi40OTItMS4yODNsLS42ODYuNDQ2Yy45NzUuODA0IDIuMDYxIDEuMjkzIDMuMTc4IDEuNjU1di0uODE4Wm0wLTEuOTQ2YTcuNTkgNy41OSAwIDAgMS0uNzc2LS40NTNsLS43MDEuNDU2Yy40NjIuMzM3Ljk1Ny42MjcgMS40NzcuODY1di0uODY4Wm0zLjUzMy4yNjktMS44ODctMS4yMjZ2LjU4MWMuNjE0LjI1NyAxLjI0NC40NzMgMS44ODcuNjQ1Wm01LjQ5My04Ljg2M0wxMi4zODEuMTEyYS43MDUuNzA1IDAgMCAwLS43NjIgMEwzLjc5NyA1LjE5OGEuNjk4LjY5OCAwIDAgMCAwIDEuMTcxbDcuMzggNC43OTdWNy42NzhhLjQxNC40MTQgMCAwIDAtLjQxMi0uNDEyaC0uNTQzYS40MTMuNDEzIDAgMCAxLS4zNTYtLjYxN2wxLjc3Ny0zLjA3OWEuNDEyLjQxMiAwIDAgMSAuNzE0IDBsMS43NzcgMy4wNzlhLjQxMy40MTMgMCAwIDEtLjM1Ni42MTdoLS41NDNhLjQxNC40MTQgMCAwIDAtLjQxMi40MTJ2My40ODhsNy4zOC00Ljc5N2EuNy43IDAgMCAwIDAtMS4xNzFaIi8+PC9zdmc+",
};

function makeBadge(label, message, color, logoName) {
  label = (label || "").toUpperCase();
  message = (message || "").toUpperCase();
  const hex = resolveColor(color);

  const PADDING = 120; // 12px in 10x units
  const HEIGHT = 28;
  const LOGO_WIDTH = 140; // 14px logo
  const LOGO_PAD_LEFT = 90; // 9px from edge
  const LOGO_PAD_RIGHT = 30; // 3px gap after logo

  const hasLogo = logoName && LOGOS[logoName];
  const logoSpace = hasLogo ? LOGO_PAD_LEFT + LOGO_WIDTH + LOGO_PAD_RIGHT : 0;

  const labelTextW = textWidth10x(label);
  const messageTextW = textWidth10x(message);

  // Label section: padding + optional logo + text + padding
  const labelW10x = hasLogo
    ? logoSpace + labelTextW + PADDING
    : PADDING + labelTextW + PADDING;
  const messageW10x = PADDING + messageTextW + PADDING;

  const labelW = Math.round(labelW10x) / 10;
  const messageW = Math.round(messageW10x) / 10;
  const totalW = Math.round(labelW * 10 + messageW * 10) / 10;

  // Text center positions (in 10x space)
  const labelTextX = hasLogo
    ? logoSpace + labelTextW / 2 + PADDING / 2
    : labelW10x / 2;
  const messageTextX = labelW10x + messageW10x / 2;

  const logoSvg = hasLogo
    ? `<image x="9" y="7" width="14" height="14" href="data:image/svg+xml;base64,${LOGOS[logoName]}"/>`
    : "";

  return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${totalW}" height="${HEIGHT}" role="img" aria-label="${escapeXml(label)}: ${escapeXml(message)}"><title>${escapeXml(label)}: ${escapeXml(message)}</title><g shape-rendering="crispEdges"><rect width="${labelW}" height="${HEIGHT}" fill="#555"/><rect x="${labelW}" width="${messageW}" height="${HEIGHT}" fill="${hex}"/></g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="100">${logoSvg}<text transform="scale(.1)" x="${labelTextX}" y="175" textLength="${labelTextW}" fill="#fff">${escapeXml(label)}</text><text transform="scale(.1)" x="${messageTextX}" y="175" textLength="${messageTextW}" fill="#fff" font-weight="bold">${escapeXml(message)}</text></g></svg>`;
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
      const logo = url.searchParams.get("logo") || null;
      return svgResponse(makeBadge(label, data.message, color, logo), 200);
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
