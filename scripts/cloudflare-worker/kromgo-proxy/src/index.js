// src/index.js — Kromgo badge proxy
// Generates pixel-perfect SVG badges with edge caching, stale-while-revalidate,
// textLength-pinned text, viewBox scaling, and integer-coordinate rendering.

// --- Rate limiter (per-isolate, sliding window) ---
const RATE_LIMIT = 30;
const RATE_WINDOW_MS = 60_000;
const DAILY_LIMIT = 50_000;
const rateMap = new Map();
let dailyCount = 0;
let dailyResetAt = 0;

function isRateLimited(ip) {
  const now = Date.now();
  if (now > dailyResetAt) { dailyCount = 0; dailyResetAt = now + 86_400_000; }
  dailyCount++;
  if (dailyCount > DAILY_LIMIT) return true;
  let entry = rateMap.get(ip);
  if (!entry || now > entry.resetAt) {
    entry = { count: 0, resetAt: now + RATE_WINDOW_MS };
    rateMap.set(ip, entry);
  }
  entry.count++;
  if (rateMap.size > 1024) {
    for (const [key, val] of rateMap) {
      if (now > val.resetAt) rateMap.delete(key);
    }
  }
  return entry.count > RATE_LIMIT;
}

// --- Edge cache with stale-while-revalidate ---
// Fresh for 30s, serve stale up to 5min while revalidating in the background.
// On origin failure, always serve stale rather than error badges.
const CACHE_FRESH_S = 30;
const CACHE_STALE_S = 300;

async function withEdgeCache(request, renderFn, ctx) {
  const cache = caches.default;
  const cacheReq = new Request(request.url, { method: "GET" });

  const cached = await cache.match(cacheReq);
  if (cached) {
    const ts = parseInt(cached.headers.get("x-fetch-time") || "0");
    const age = (Date.now() - ts) / 1000;

    if (age < CACHE_STALE_S) {
      if (age > CACHE_FRESH_S) {
        // Stale — return immediately, revalidate in background
        ctx.waitUntil(revalidateCache(cache, cacheReq, renderFn));
      }
      return toClientResponse(cached);
    }
  }

  // Cache miss or expired — fetch synchronously
  try {
    const resp = await renderFn();
    if (resp.status === 200) {
      const toReturn = resp.clone();
      ctx.waitUntil(putInCache(cache, cacheReq, resp));
      return toReturn;
    }
    // Non-200: prefer stale cache over error badge
    if (cached) return toClientResponse(cached);
    return resp;
  } catch (e) {
    if (cached) return toClientResponse(cached);
    return svgResponse(makeBadge("error", "timeout", "lightgrey"), 503);
  }
}

function toClientResponse(cached) {
  const ct = cached.headers.get("Content-Type") || "image/svg+xml";
  return new Response(cached.body, { status: 200, headers: { ...SECURITY_HEADERS, "Content-Type": ct } });
}

async function putInCache(cache, req, resp) {
  const body = await resp.arrayBuffer();
  const h = new Headers();
  h.set("Content-Type", resp.headers.get("Content-Type") || "image/svg+xml");
  h.set("x-fetch-time", Date.now().toString());
  h.set("Cache-Control", "public, s-maxage=300");
  await cache.put(req, new Response(body, { status: 200, headers: h }));
}

async function revalidateCache(cache, cacheReq, renderFn) {
  try {
    const resp = await renderFn();
    if (resp.status === 200) await putInCache(cache, cacheReq, resp);
  } catch { /* stale cache continues serving */ }
}

// --- Colors (GitHub Primer dark-mode palette) ---
const COLORS = {
  green: "#3fb950",
  brightgreen: "#56d364",
  yellowgreen: "#7ee787",
  yellow: "#d29922",
  orange: "#db6d28",
  red: "#f85149",
  blue: "#58a6ff",
  purple: "#bc8cff",
  cyan: "#76e3ea",
  lightgrey: "#8b949e",
  gray: "#8b949e",
  grey: "#8b949e",
  critical: "#f85149",
};

function resolveColor(color) {
  if (!color) return COLORS.lightgrey;
  if (color.startsWith("#")) return color;
  return COLORS[color.toLowerCase()] || COLORS.lightgrey;
}

// --- Text width calculation ---
// Helvetica Bold character widths at 11px (in 10x units for sub-pixel precision)
// Derived from Helvetica Bold AFM metrics (1000 units/em → scaled to 11px * 10x)
const W = {
  " ": 31, "%": 92, ".": 31, "/": 31, "-": 37, ":": 31, "(": 37, ")": 37,
  0: 61, 1: 61, 2: 61, 3: 61, 4: 61, 5: 61, 6: 61, 7: 61, 8: 61, 9: 61,
  A: 79, B: 79, C: 79, D: 79, E: 73, F: 67, G: 86, H: 79, I: 31, J: 61,
  K: 79, L: 67, M: 92, N: 79, O: 86, P: 73, Q: 86, R: 79, S: 73, T: 67,
  U: 79, V: 73, W: 104, X: 73, Y: 73, Z: 67,
};
const LETTER_SP = 13;

function tw(text) {
  let w = 0;
  for (let i = 0; i < text.length; i++) {
    w += W[text[i]] || W[text[i].toUpperCase()] || 73;
    if (i < text.length - 1) w += LETTER_SP;
  }
  return w;
}

// --- SVG logos (Simple Icons, white fill, 24x24 viewBox) — base64 ---
const LOGOS = {
  kubernetes: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTEwLjIwNCAxNC4zNWwuMDA3LjAxLS45OTkgMi40MTNhNS4xNzEgNS4xNzEgMCAwIDEtMi4wNzUtMi41OTdsMi41NzgtLjQzNy4wMDQuMDA1YS40NC40NCAwIDAgMSAuNDg0LjYwNnptLS44MzMtMi4xMjlhLjQ0LjQ0IDAgMCAwIC4xNzMtLjc1NmwuMDAyLS4wMTFMNy41ODUgOS43YTUuMTQzIDUuMTQzIDAgMCAwLS43MyAzLjI1NWwyLjUxNC0uNzI1LjAwMi0uMDA5em0xLjE0NS0xLjk4YS40NC40NCAwIDAgMCAuNjk5LS4zMzdsLjAxLS4wMDUuMTUtMi42MmE1LjE0NCA1LjE0NCAwIDAgMC0zLjAxIDEuNDQybDIuMTQ3IDEuNTIzLjAwNC0uMDAyem0uNzYgMi43NWwuNzIzLjM0OS43MjItLjM0Ny4xOC0uNzgtLjUtLjYyM2gtLjgwNGwtLjUuNjIzLjE3OS43Nzl6bTEuNS0zLjA5NWEuNDQuNDQgMCAwIDAgLjcuMzM2bC4wMDguMDAzIDIuMTM0LTEuNTEzYTUuMTg4IDUuMTg4IDAgMCAwLTIuOTkyLTEuNDQybC4xNDggMi42MTUuMDAyLjAwMXptMTAuODc2IDUuOTdsLTUuNzczIDcuMTgxYTEuNiAxLjYgMCAwIDEtMS4yNDguNTk0bC05LjI2MS4wMDNhMS42IDEuNiAwIDAgMS0xLjI0Ny0uNTk2bC01Ljc3Ni03LjE4YTEuNTgzIDEuNTgzIDAgMCAxLS4zMDctMS4zNEwyLjEgNS41NzNjLjEwOC0uNDcuNDI1LS44NjQuODYzLTEuMDczTDExLjMwNS41MTNhMS42MDYgMS42MDYgMCAwIDEgMS4zODUgMGw4LjM0NSAzLjk4NWMuNDM4LjIwOS43NTUuNjA0Ljg2MyAxLjA3M2wyLjA2MiA4Ljk1NWMuMTA4LjQ3LS4wMDUuOTYzLS4zMDggMS4zNHptLTMuMjg5LTIuMDU3Yy0uMDQyLS4wMS0uMTAzLS4wMjYtLjE0NS0uMDM0LS4xNzQtLjAzMy0uMzE1LS4wMjUtLjQ3OS0uMDM4LS4zNS0uMDM3LS42MzgtLjA2Ny0uODk1LS4xNDgtLjEwNS0uMDQtLjE4LS4xNjUtLjIxNi0uMjE2bC0uMjAxLS4wNTlhNi40NSA2LjQ1IDAgMCAwLS4xMDUtMi4zMzIgNi40NjUgNi40NjUgMCAwIDAtLjkzNi0yLjE2M2MuMDUyLS4wNDcuMTUtLjEzMy4xNzctLjE1OS4wMDgtLjA5LjAwMS0uMTgzLjA5NC0uMjgyLjE5Ny0uMTg1LjQ0NC0uMzM4Ljc0My0uNTIyLjE0Mi0uMDg0LjI3My0uMTM3LjQxNS0uMjQyLjAzMi0uMDI0LjA3Ni0uMDYyLjExLS4wODkuMjQtLjE5MS4yOTUtLjUyLjEyMy0uNzM2LS4xNzItLjIxNi0uNTA2LS4yMzYtLjc0NS0uMDQ1LS4wMzQuMDI3LS4wOC4wNjItLjExMS4wODgtLjEzNC4xMTYtLjIxNy4yMy0uMzMuMzUtLjI0Ni4yNS0uNDUuNDU4LS42NzMuNjA5LS4wOTcuMDU2LS4yMzkuMDM3LS4zMDMuMDMzbC0uMTkuMTM1YTYuNTQ1IDYuNTQ1IDAgMCAwLTQuMTQ2LTIuMDAzbC0uMDEyLS4yMjNjLS4wNjUtLjA2Mi0uMTQzLS4xMTUtLjE2My0uMjUtLjAyMi0uMjY4LjAxNS0uNTU3LjA1Ny0uOTA1LjAyMy0uMTYzLjA2MS0uMjk4LjA2OC0uNDc1LjAwMS0uMDQtLjAwMS0uMDk5LS4wMDEtLjE0MiAwLS4zMDYtLjIyNC0uNTU1LS41LS41NTUtLjI3NSAwLS40OTkuMjQ5LS40OTkuNTU1bC4wMDEuMDE0YzAgLjA0MS0uMDAyLjA5MiAwIC4xMjguMDA2LjE3Ny4wNDQuMzEyLjA2Ny40NzUuMDQyLjM0OC4wNzguNjM3LjA1Ni45MDZhLjU0NS41NDUgMCAwIDEtLjE2Mi4yNThsLS4wMTIuMjExYTYuNDI0IDYuNDI0IDAgMCAwLTQuMTY2IDIuMDAzIDguMzczIDguMzczIDAgMCAxLS4xOC0uMTI4Yy0uMDkuMDEyLS4xOC4wNC0uMjk3LS4wMjktLjIyMy0uMTUtLjQyNy0uMzU4LS42NzMtLjYwOC0uMTEzLS4xMi0uMTk1LS4yMzQtLjMyOS0uMzQ5LS4wMy0uMDI2LS4wNzctLjA2Mi0uMTExLS4wODhhLjU5NC41OTQgMCAwIDAtLjM0OC0uMTMyLjQ4MS40ODEgMCAwIDAtLjM5OC4xNzZjLS4xNzIuMjE2LS4xMTcuNTQ2LjEyMy43MzdsLjAwNy4wMDUuMTA0LjA4M2MuMTQyLjEwNS4yNzIuMTU5LjQxNC4yNDIuMjk5LjE4NS41NDYuMzM4Ljc0My41MjIuMDc2LjA4Mi4wOS4yMjYuMS4yODhsLjE2LjE0M2E2LjQ2MiA2LjQ2MiAwIDAgMC0xLjAyIDQuNTA2bC0uMjA4LjA2Yy0uMDU1LjA3Mi0uMTMzLjE4NC0uMjE1LjIxNy0uMjU3LjA4MS0uNTQ2LjExLS44OTUuMTQ3LS4xNjQuMDE0LS4zMDUuMDA2LS40OC4wMzktLjAzNy4wMDctLjA5LjAyLS4xMzMuMDNsLS4wMDQuMDAyLS4wMDcuMDAyYy0uMjk1LjA3MS0uNDg0LjM0Mi0uNDIzLjYwOC4wNjEuMjY3LjM0OS40MjkuNjQ1LjM2NWwuMDA3LS4wMDEuMDEtLjAwMy4xMjktLjAyOWMuMTctLjA0Ni4yOTQtLjExMy40NDgtLjE3Mi4zMy0uMTE4LjYwNC0uMjE3Ljg3LS4yNTYuMTEyLS4wMDkuMjMuMDY5LjI4OC4xMDFsLjIxNy0uMDM3YTYuNSA2LjUgMCAwIDAgMi44OCAzLjU5NmwtLjA5LjIxOGMuMDMzLjA4NC4wNjkuMTk5LjA0NC4yODItLjA5Ny4yNTItLjI2My41MTctLjQ1Mi44MTMtLjA5MS4xMzYtLjE4NS4yNDItLjI2OC4zOTktLjAyLjAzNy0uMDQ1LjA5NS0uMDY0LjEzNC0uMTI4LjI3NS0uMDM0LjU5MS4yMTMuNzEuMjQ4LjEyLjU1Ni0uMDA3LjY5LS4yODJ2LS4wMDJjLjAyLS4wMzkuMDQ2LS4wOS4wNjItLjEyNy4wNy0uMTYyLjA5NC0uMzAxLjE0NC0uNDU4LjEzMi0uMzMyLjIwNS0uNjguMzg3LS44OTcuMDUtLjA2LjEzLS4wODIuMjE1LS4xMDVsLjExMy0uMjA1YTYuNDUzIDYuNDUzIDAgMCAwIDQuNjA5LjAxMmwuMTA2LjE5MmMuMDg2LjAyOC4xOC4wNDIuMjU2LjE1NS4xMzYuMjMyLjIyOS41MDcuMzQyLjg0LjA1LjE1Ni4wNzQuMjk1LjE0NS40NTcuMDE2LjAzNy4wNDMuMDkuMDYyLjEyOS4xMzMuMjc2LjQ0Mi40MDIuNjkuMjgyLjI0Ny0uMTE4LjM0MS0uNDM1LjIxMy0uNzEtLjAyLS4wMzktLjA0NS0uMDk2LS4wNjUtLjEzNC0uMDgzLS4xNTYtLjE3Ny0uMjYxLS4yNjgtLjM5OC0uMTktLjI5Ni0uMzQ2LS41NDEtLjQ0My0uNzkzLS4wNC0uMTMuMDA3LS4yMS4wMzgtLjI5NC0uMDE4LS4wMjItLjA1OS0uMTQ0LS4wODMtLjIwMmE2LjQ5OSA2LjQ5OSAwIDAgMCAyLjg4LTMuNjIyYy4wNjQuMDEuMTc2LjAzLjIxMy4wMzguMDc1LS4wNS4xNDQtLjExNC4yOC0uMTA0LjI2Ni4wMzkuNTQuMTM4Ljg3LjI1Ni4xNTQuMDYuMjc3LjEyOC40NDguMTczLjAzNi4wMS4wODguMDE5LjEzLjAyOGwuMDA5LjAwMy4wMDcuMDAxYy4yOTcuMDY0LjU4NC0uMDk4LjY0NS0uMzY1LjA2LS4yNjYtLjEyOC0uNTM3LS40MjMtLjYwOHpNMTYuNCA5LjcwMWwtMS45NSAxLjc0NnYuMDA1YS40NC40NCAwIDAgMCAuMTczLjc1N2wuMDAzLjAxIDIuNTI2LjcyOGE1LjE5OSA1LjE5OSAwIDAgMC0uMTA4LTEuNjc0QTUuMjA4IDUuMjA4IDAgMCAwIDE2LjQgOS43em0tNC4wMTMgNS4zMjVhLjQzNy40MzcgMCAwIDAtLjQwNC0uMjMyLjQ0LjQ0IDAgMCAwLS4zNzIuMjMzaC0uMDAybC0xLjI2OCAyLjI5MmE1LjE2NCA1LjE2NCAwIDAgMCAzLjMyNi4wMDNsLTEuMjctMi4yOTZoLS4wMXptMS44ODgtMS4yOTNhLjQ0LjQ0IDAgMCAwLS4yNy4wMzYuNDQuNDQgMCAwIDAtLjIxNC41NzJsLS4wMDMuMDA0IDEuMDEgMi40MzhhNS4xNSA1LjE1IDAgMCAwIDIuMDgxLTIuNjE1bC0yLjYtLjQ0LS4wMDQuMDA1eiIvPjwvc3ZnPg==",
  talos: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTkuNjc4IDExLjk4YzAtMi42NjQtMS4xMy02Ljg5Ni0yLjg2Ny0xMC44MDRhMTIgMTIgMCAwIDAtMS41ODUuOTE3YzEuNjA4IDMuNjY4IDIuNjQ3IDcuNTUzIDIuNjQ3IDkuODg2IDAgMi4yNTQtMS4wOCA2LjE0NS0yLjczNSA5Ljg2NWExMiAxMiAwIDAgMCAxLjU3Ni45M2MxLjc5LTMuOTc2IDIuOTY0LTguMjI5IDIuOTY0LTEwLjc5NW02LjQ0MiAwYzAtMi4zMzYgMS4wNDItNi4yMiAyLjY0Ni05Ljg5YTEyIDEyIDAgMCAwLTEuNjA4LS45MjJjLTEuNzU2IDMuOTU3LTIuODQzIDguMTY2LTIuODQzIDEwLjgxNiAwIDIuNTY0IDEuMTc3IDYuODE5IDIuOTY1IDEwLjc5N2ExMiAxMiAwIDAgMCAxLjU3NS0uOTMxYy0xLjY1NS0zLjcyMy0yLjczNS03LjYxNi0yLjczNS05Ljg3bTUuNDUgNi41MjUuMzEuMzA3YTEyIDEyIDAgMCAwIC45MzYtMS42MTJjLTEuODY2LTEuODkzLTMuNDU3LTMuOTM4LTMuNDctNS4yMzMtLjAxMi0xLjI2NCAxLjU3LTMuMzA4IDMuNDQ2LTUuMjIyYTEyIDEyIDAgMCAwLS45NDUtMS42MDNsLS4yNTkuMjU4Yy0yLjczOSAyLjc2Ni00LjA2MyA0LjkyLTQuMDQ3IDYuNTgzLjAxNiAxLjY2MiAxLjMzMiAzLjgxIDQuMDI4IDYuNTIyTTIuNDExIDUuNDA1bC0uMjYtLjI1OWExMiAxMiAwIDAgMC0uOTQ2IDEuNjA4YzMuMTIzIDMuMTczIDMuNDUyIDQuNzA0IDMuNDQ4IDUuMjE3LS4wMTIgMS4zLTEuNjAzIDMuMzQtMy40NyA1LjIyOWExMiAxMiAwIDAgMCAuOTM5IDEuNjA4Yy4xMDYtLjEwNi4yMDctLjIwNC4zMS0uMzA4IDIuNjk0LTIuNzExIDQuMDEtNC44NDIgNC4wMjYtNi41MTZzLTEuMzA4LTMuODA5LTQuMDQ3LTYuNThNMTIuMDAyIDI0Yy4zMDMgMCAuNjAyLS4wMTYuODk4LS4wMzdWLjAzN0ExMiAxMiAwIDAgMCAxMiAwYy0uMzA0IDAtLjYwNS4wMTUtLjkwNS4wMzd2MjMuOTI1cS40NDguMDM1LjkwMy4wMzh6Ii8+PC9zdmc+",
  flux: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTExLjQwMiAyMy43NDdjLjE1NC4wNzUuMzA2LjE1NC40NTQuMjM4LjE4MS4wMzguMzcuMDA0LjUyNS0uMDk3bC4zODYtLjI1MWMtMS4yNDItLjgzMS0yLjYyMi0xLjI1MS0zLjk5OC0xLjYwMmwyLjYzMyAxLjcxMlptLTcuNDk1LTUuNzgzYTguMDg4IDguMDg4IDAgMCAxLS4yMjItLjIzNi42OTYuNjk2IDAgMCAwIC4xMTIgMS4wNzVsMi4zMDQgMS40OThjMS4wMTkuNDIyIDIuMDg1LjY4NiAzLjEzNC45NDQgMS42MzYuNDAzIDMuMi43OSA0LjU1NCAxLjcyOGwuNjk3LS40NTNjLTEuNTQxLTEuMTU4LTMuMzI3LTEuNjAyLTUuMDY1LTIuMDMtMi4wMzktLjUwMy0zLjk2NS0uOTc3LTUuNTE0LTIuNTI2Wm0xLjQxNC0xLjMyMi0uNjY1LjQzMmMuMDIzLjAyNC4wNDQuMDQ5LjA2OC4wNzMgMS43MDIgMS43MDIgMy44MjUgMi4yMjUgNS44NzcgMi43MzEgMS43NzguNDM4IDMuNDY5Ljg1NiA0LjkgMS45ODJsLjY4Mi0uNDQ0Yy0xLjYxMi0xLjM1Ny0zLjUzMi0xLjgzNC01LjM5NS0yLjI5My0yLjAxOS0uNDk3LTMuOTI2LS45NjktNS40NjctMi40ODFabTcuNTAyIDIuMDg0YzEuNTk2LjQxMiAzLjA5Ni45MDQgNC4zNjcgMi4wMzZsLjY3LS40MzZjLTEuNDg0LTEuMzk2LTMuMjY2LTEuOTUzLTUuMDM3LTIuNDAzdi44MDNabS42OTgtMi4zMzdhNjQuNjk1IDY0LjY5NSAwIDAgMS0uNjk4LS4xNzR2LjgwMmwuNTEyLjEyN2MyLjAzOS41MDMgMy45NjUuOTc4IDUuNTE0IDIuNTI2bC4wMDcuMDA5LjY2My0uNDMxYy0uMDQxLS4wNDItLjA3OS0uMDg2LS4xMjEtLjEyOC0xLjcwMi0xLjcwMS0zLjgyNC0yLjIyNS01Ljg3Ny0yLjczMVptLS42OTgtMS45Mjh2LjgxNmMuNjI0LjE5IDEuMjU1LjM0NyAxLjg3OS41MDEgMi4wMzkuNTAyIDMuOTY1Ljk3NyA1LjUxMyAyLjUyNi4wNzcuMDc3LjE1My4xNTcuMjI2LjIzOWEuNzA0LjcwNCAwIDAgMC0uMjM4LS45MTFsLTMuMDY0LTEuOTkyYy0uNzQ0LS4yNDUtMS41MDItLjQzMy0yLjI1MS0uNjE4YTMxLjQzNiAzMS40MzYgMCAwIDEtMi4wNjUtLjU2MVptLTEuNjQ2IDMuMDQ5Yy0xLjUyNi0uNC0yLjk2LS44ODgtNC4xODUtMS45NTVsLS42NzQuNDM5YzEuNDM5IDEuMzI2IDMuMTUxIDEuODggNC44NTkgMi4zMTl2LS44MDNabTAtMS43NzJhOC41NDMgOC41NDMgMCAwIDEtMi40OTItMS4yODNsLS42ODYuNDQ2Yy45NzUuODA0IDIuMDYxIDEuMjkzIDMuMTc4IDEuNjU1di0uODE4Wm0wLTEuOTQ2YTcuNTkgNy41OSAwIDAgMS0uNzc2LS40NTNsLS43MDEuNDU2Yy40NjIuMzM3Ljk1Ny42MjcgMS40NzcuODY1di0uODY4Wm0zLjUzMy4yNjktMS44ODctMS4yMjZ2LjU4MWMuNjE0LjI1NyAxLjI0NC40NzMgMS44ODcuNjQ1Wm01LjQ5My04Ljg2M0wxMi4zODEuMTEyYS43MDUuNzA1IDAgMCAwLS43NjIgMEwzLjc5NyA1LjE5OGEuNjk4LjY5OCAwIDAgMCAwIDEuMTcxbDcuMzggNC43OTdWNy42NzhhLjQxNC40MTQgMCAwIDAtLjQxMi0uNDEyaC0uNTQzYS40MTMuNDEzIDAgMCAxLS4zNTYtLjYxN2wxLjc3Ny0zLjA3OWEuNDEyLjQxMiAwIDAgMSAuNzE0IDBsMS43NzcgMy4wNzlhLjQxMy40MTMgMCAwIDEtLjM1Ni42MTdoLS41NDNhLjQxNC40MTQgMCAwIDAtLjQxMi40MTJ2My40ODhsNy4zOC00Ljc5N2EuNy43IDAgMCAwIDAtMS4xNzFaIi8+PC9zdmc+",
  renovatebot: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTE3LjU3NiAxMC44NTJjLS4xMDggMC0uMjE2LjAxOC0uMzI0LjA1NGExLjM0NCAxLjM0NCAwIDAgMC0uOTE4IDEuMTg4Yy0uMDE4LjM5Ni4xMjYuNzU2LjM5NiAxLjAyNi4yNy4yNTIuNjMuMzk2IDEuMDI2LjM5NmExLjM4IDEuMzggMCAwIDAgMS4wOC0uNTA0Yy4yNy0uMzA2LjM3OC0uNzAyLjMwNi0xLjA5OGExLjM0NCAxLjM0NCAwIDAgMC0uOTE4LTEuMDA4IDEuMTY0IDEuMTY0IDAgMCAwLS42NDgtLjA1NHpNMTIgMEM1LjM3NiAwIDAgNS4zNzYgMCAxMnM1LjM3NiAxMiAxMiAxMiAxMi01LjM3NiAxMi0xMlMxOC42MjQgMCAxMiAwem01LjIwOCAxNC40MThhMy4xODYgMy4xODYgMCAwIDEtMS43NjQgMS4xMTYgMy4xOCAzLjE4IDAgMCAxLTIuMDctLjE5OGwtMy45MjQgNC41OTZjLS4zNzguNDMyLS44ODIuNjg0LTEuNDIyLjcwMmExLjk0NCAxLjk0NCAwIDAgMS0xLjQ1OC0uNTk0IDEuOTQ0IDEuOTQ0IDAgMCAxLS41OTQtMS40NThjLjAxOC0uNTQuMjctMS4wNDQuNzAyLTEuNDIybDQuNTk2LTMuOTI0YTMuMTggMy4xOCAwIDAgMS0uMTk4LTIuMDcgMy4xODYgMy4xODYgMCAwIDEgMS4xMTYtMS43NjQgMy4xNzQgMy4xNzQgMCAwIDEgMi44MjYtLjU5NGwtMS42MzggMS42MzguMTQ0IDEuNzI4IDEuNzI4LjE0NCAxLjYzOC0xLjYzOGEzLjE3NCAzLjE3NCAwIDAgMS0uNTk0IDIuODI2IDIuMDcgMi4wNyAwIDAgMS0uMTI2LjE2MnoiLz48L3N2Zz4K",
};

// --- Metric-specific icons (Material Design, 24x24 viewBox, white fill) ---
const METRIC_LOGOS = {
  clock: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0xMiAyQzYuNDggMiAyIDYuNDggMiAxMnM0LjQ4IDEwIDEwIDEwIDEwLTQuNDggMTAtMTBTMTcuNTIgMiAxMiAyem0wIDE4Yy00LjQyIDAtOC0zLjU4LTgtOHMzLjU4LTggOC04IDggMy41OCA0IDgtMy41OCA4LTggOHoiLz48cGF0aCBkPSJNMTIuNSA3SDExdjZsNS4yNSAzLjE1Ljc1LTEuMjMtNC41LTIuNjd6Ii8+PC9zdmc+",
  node: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxyZWN0IHg9IjMiIHk9IjMiIHdpZHRoPSIxOCIgaGVpZ2h0PSI3IiByeD0iMS41Ii8+PGNpcmNsZSBjeD0iNyIgY3k9IjYuNSIgcj0iMS4yIi8+PHJlY3QgeD0iMTAiIHk9IjUuNSIgd2lkdGg9IjciIGhlaWdodD0iMiIgcng9Ii41Ii8+PHJlY3QgeD0iMyIgeT0iMTQiIHdpZHRoPSIxOCIgaGVpZ2h0PSI3IiByeD0iMS41Ii8+PGNpcmNsZSBjeD0iNyIgY3k9IjE3LjUiIHI9IjEuMiIvPjxyZWN0IHg9IjEwIiB5PSIxNi41IiB3aWR0aD0iNyIgaGVpZ2h0PSIyIiByeD0iLjUiLz48L3N2Zz4=",
  alert: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0xIDIxaDIyTDEyIDIgMSAyMXptMTItM2gtMnYtMmgydjJ6bTAtNGgtMnYtNGgydjR6Ii8+PC9zdmc+",
  cube: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0yMSAxNi41YzAgLjM4LS4yMS43MS0uNTMuODhsLTcuOSA0LjQ0Yy0uMTYuMTItLjM2LjE4LS41Ny4xOHMtLjQxLS4wNi0uNTctLjE4bC03LjktNC40NEEuOTkxLjk5MSAwIDAxMyAxNi41di05YzAtLjM4LjIxLS43MS41My0uODhsNy45LTQuNDRjLjE2LS4xMi4zNi0uMTguNTctLjE4cy40MS4wNi41Ny4xOGw3LjkgNC40NGMuMzIuMTcuNTMuNS41My44OHY5ek0xMiA1LjE1TDUuMDQgOS4wMyAxMiAxMi45Mmw2Ljk2LTMuODlMMTIgNS4xNXoiLz48L3N2Zz4=",
  cpu: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0xNSA5SDl2Nmg2Vjl6bS0yIDRoLTJ2LTJoMnYyem04LTJWOWgtMlY3YzAtMS4xLS45LTItMi0yaC0yVjNoLTJ2MmgtMlYzSDl2Mkg3Yy0xLjEgMC0yIC45LTIgMnYySDN2MmgydjJIM3YyaDJ2MmMwIDEuMS45IDIgMiAyaDJ2Mmgydi0yaDJ2Mmgydi0yaDJjMS4xIDAgMi0uOSAyLTJ2LTJoMnYtMmgtMnYtMmgyem0tNCA2SDdWN2gxMHYxMHoiLz48L3N2Zz4=",
  memory: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0yIDdoNHYxMEgyem02LTRoNHYxOEg4em02IDhoNHY2aC00em02LTJoMnYxMGgtMnoiLz48L3N2Zz4=",
  storage: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxlbGxpcHNlIGN4PSIxMiIgY3k9IjUuNSIgcng9IjgiIHJ5PSIzLjUiLz48cGF0aCBkPSJNNCA1LjV2NWMwIDEuOTMgMy41OCAzLjUgOCAzLjVzOC0xLjU3IDgtMy41di01YzAgMS45My0zLjU4IDMuNS04IDMuNVM0IDcuNDMgNCA1LjV6Ii8+PHBhdGggZD0iTTQgMTAuNXY1YzAgMS45MyAzLjU4IDMuNSA4IDMuNXM4LTEuNTcgOC0zLjV2LTVjMCAxLjkzLTMuNTggMy41LTggMy41cy04LTEuNTctOC0zLjV6Ii8+PC9zdmc+",
  helm: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0xOS40MyAxMi45OGMuMDQtLjMyLjA3LS42NC4wNy0uOThzLS4wMy0uNjYtLjA3LS45OGwyLjExLTEuNjVjLjE5LS4xNS4yNC0uNDIuMTItLjY0bC0yLTMuNDZjLS4xMi0uMjItLjM5LS4zLS42MS0uMjJsLTIuNDkgMWMtLjUyLS40LTEuMDgtLjczLTEuNjktLjk4bC0uMzgtMi42NUMxNC40NiAyLjE4IDE0LjI1IDIgMTQgMmgtNGMtLjI1IDAtLjQ2LjE4LS40OS40MmwtLjM4IDIuNjVjLS42MS4yNS0xLjE3LjU5LTEuNjkuOThsLTIuNDktMWMtLjIzLS4wOS0uNDkgMC0uNjEuMjJsLTIgMy40NmMtLjEzLjIyLS4wNy40OS4xMi42NGwyLjExIDEuNjVjLS4wNC4zMi0uMDcuNjUtLjA3Ljk4cy4wMy42Ni4wNy45OGwtMi4xMSAxLjY1Yy0uMTkuMTUtLjI0LjQyLS4xMi42NGwyIDMuNDZjLjEyLjIyLjM5LjMuNjEuMjJsMi40OS0xYy41Mi40IDEuMDguNzMgMS42OS45OGwuMzggMi42NWMuMDMuMjQuMjQuNDIuNDkuNDJoNGMuMjUgMCAuNDYtLjE4LjQ5LS40MmwuMzgtMi42NWMuNjEtLjI1IDEuMTctLjU5IDEuNjktLjk4bDIuNDkgMWMuMjMuMDkuNDkgMCAuNjEtLjIybDItMy40NmMuMTItLjIyLjA3LS40OS0uMTItLjY0bC0yLjExLTEuNjV6TTEyIDE1LjVjLTEuOTMgMC0zLjUtMS41Ny0zLjUtMy41czEuNTctMy41IDMuNS0zLjUgMy41IDEuNTcgMy41IDMuNS0xLjU3IDMuNS0zLjUgMy41eiIvPjwvc3ZnPg==",
  volume: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0yIDIwaDIwdi00SDJ2NHptMi0zaDJ2Mkg0di0yek0yIDR2NGgyMFY0SDJ6bTQgM0g0VjVoMnYyem0tNCA3aDIwdi00SDJ2NHptMi0zaDJ2Mkg0di0yeiIvPjwvc3ZnPg==",
  shield: "PHN2ZyBmaWxsPSJ3aGl0ZSIgdmlld0JveD0iMCAwIDI0IDI0IiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciPjxwYXRoIGQ9Ik0xMiAxTDMgNXY2YzAgNS41NSAzLjg0IDEwLjc0IDkgMTIgNS4xNi0xLjI2IDktNi40NSA5LTEyVjVsLTktNHptLTIgMTZsLTQtNCAxLjQxLTEuNDFMMTAgMTQuMTdsNi41OS02LjU5TDE4IDlsLTggOHoiLz48L3N2Zz4=",
};


// Map metric names to their icons
const METRIC_ICON_MAP = {
  cluster_age_days: "clock",
  cluster_uptime_days: "clock",
  cluster_node_count: "node",
  cluster_alert_count: "alert",
  cluster_pod_count: "cube",
  container_count: "cube",
  cluster_cpu_usage: "cpu",
  cluster_memory_usage: "memory",
  ceph_storage_used: "storage",
  ceph_health: "storage",
  helmrelease_count: "helm",
  pvc_count: "volume",
  flux_failing_count: "shield",
  cert_expiry_days: "shield",
};

// --- Badge SVG constants ---
const HEIGHT = 34;
const RADIUS = 6;
const PAD = 140; // 14px padding in 10x units
const LOGO_SIZE = 20;
const LOGO_X = 9;
const LOGO_GAP = 40; // 4px gap after logo in 10x
const LABEL_BG = "#30363d"; // GitHub dark surface
const LABEL_BG_VERSION = "#1f2937"; // Darker slate for version badges

function makeBadge(label, message, color, logoName, opts = {}) {
  label = (label || "").toUpperCase();
  message = (message || "").toUpperCase();
  const hex = resolveColor(color);
  const labelBg = opts.labelBg || LABEL_BG;

  const hasLogo = logoName && (LOGOS[logoName] || METRIC_LOGOS[logoName]);
  const logoSpace10x = hasLogo ? (LOGO_X + LOGO_SIZE) * 10 + LOGO_GAP : 0;

  const labelTW = tw(label);
  const msgTW = tw(message);

  const labelW10x = hasLogo ? logoSpace10x + labelTW + PAD : PAD + labelTW + PAD;
  const msgW10x = PAD + msgTW + PAD;

  const lW = Math.round(labelW10x) / 10;
  const mW = Math.round(msgW10x) / 10;
  const tW = Math.round(labelW10x + msgW10x) / 10;

  const labelTX = hasLogo ? logoSpace10x + labelTW / 2 + PAD / 2 : labelW10x / 2;
  const msgTX = labelW10x + msgW10x / 2;

  const logoY = Math.round((HEIGHT - LOGO_SIZE) / 2);
  const logoB64 = LOGOS[logoName] || METRIC_LOGOS[logoName];
  const logoEl = hasLogo
    ? `<image x="${LOGO_X}" y="${logoY}" width="${LOGO_SIZE}" height="${LOGO_SIZE}" href="data:image/svg+xml;base64,${logoB64}"/>`
    : "";

  // Helvetica Bold cap-height ~72% of font-size (110 units at scale .1 = 11px)
  const textY = (HEIGHT * 10 + 80) / 2;

  return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${tW}" height="${HEIGHT}" viewBox="0 0 ${tW} ${HEIGHT}" role="img" aria-label="${escapeXml(label)}: ${escapeXml(message)}"><title>${escapeXml(label)}: ${escapeXml(message)}</title><defs><linearGradient id="hi" x2="0" y2="100%"><stop offset="0" stop-color="#fff" stop-opacity=".10"/><stop offset=".4" stop-color="#fff" stop-opacity="0"/></linearGradient><linearGradient id="sh" x2="0" y2="100%"><stop offset=".6" stop-opacity="0"/><stop offset="1" stop-opacity=".10"/></linearGradient></defs><clipPath id="r"><rect width="${tW}" height="${HEIGHT}" rx="${RADIUS}" fill="#fff"/></clipPath><g clip-path="url(#r)"><rect width="${lW}" height="${HEIGHT}" fill="${labelBg}" shape-rendering="crispEdges"/><rect x="${lW}" width="${mW}" height="${HEIGHT}" fill="${hex}" shape-rendering="crispEdges"/><rect width="${tW}" height="${HEIGHT}" fill="url(#hi)"/><rect width="${tW}" height="${HEIGHT}" fill="url(#sh)"/><rect width="${tW}" height="${HEIGHT}" fill="none" stroke="rgba(255,255,255,0.06)" stroke-width="1" rx="${RADIUS}"/></g><g fill="#fff" text-anchor="middle" font-family="Helvetica,Arial,sans-serif" font-weight="bold" text-rendering="geometricPrecision" font-size="110">${logoEl}<text aria-hidden="true" transform="scale(.1)" x="${labelTX}" y="${textY + 12}" fill="#010101" fill-opacity=".4">${escapeXml(label)}</text><text transform="scale(.1)" x="${labelTX}" y="${textY}" fill="#fff">${escapeXml(label)}</text><text aria-hidden="true" transform="scale(.1)" x="${msgTX}" y="${textY + 12}" fill="#010101" fill-opacity=".4">${escapeXml(message)}</text><text transform="scale(.1)" x="${msgTX}" y="${textY}" fill="#fff">${escapeXml(message)}</text></g></svg>`;
}

function escapeXml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// --- Network status panel ---
function makeNetworkPanel(links) {
  const secW = 186;
  const panelW = secW * 3;
  const panelH = 44;
  const radius = 6;
  const midY = panelH / 2;
  const BG = "#161b22";
  const BORDER = "rgba(255,255,255,0.06)";

  const padL = 16;
  const iconW = 14, iconGap = 7;   // icon + gap before label
  const dotR = 4, dotGap = 7;      // status dot + gap before status text
  const labelGap = 10;             // gap between label and dot

  let content = "";

  links.forEach((link, i) => {
    const bx = i * secW;
    const statusText = (link.message || "?").toUpperCase();
    const statusColor = resolveColor(link.color);
    const isUp = statusText === "UP";
    const labelPx = tw(link.label) * 11 / 100;
    const statusPx = tw(statusText) * 11 / 100;

    const contentW = iconW + iconGap + labelPx + labelGap + dotR * 2 + dotGap + statusPx;
    const offset = Math.max(0, (secW - padL * 2 - contentW) / 2);
    const sx = Math.round(bx + padL + offset);

    // Icon — lightning bolt for fiber, signal bars for cellular
    const iconX = sx;
    const iconY = Math.round(midY - 7);
    if (link.icon === "bolt") {
      // Lightning bolt (fiber) — 14x14 viewBox scaled inline
      content += `<g transform="translate(${iconX},${iconY})">`;
      content += `<polygon points="8,0 3,7 6,7 4,14 11,5 7,5 9,0" fill="#d29922" opacity=".9"/>`;
      content += `</g>`;
    } else {
      // Signal tower (cellular) — 3 ascending bars + radiating arcs
      const bx2 = iconX;
      const by = Math.round(midY + 7);
      content += `<rect x="${bx2}" y="${by - 4}" width="3" height="4" rx=".5" fill="#58a6ff" shape-rendering="crispEdges"/>`;
      content += `<rect x="${bx2 + 4}" y="${by - 8}" width="3" height="8" rx=".5" fill="#58a6ff" shape-rendering="crispEdges"/>`;
      content += `<rect x="${bx2 + 8}" y="${by - 12}" width="3" height="12" rx=".5" fill="#58a6ff" shape-rendering="crispEdges"/>`;
      // Small radiating arc on top bar
      const arcCX = bx2 + 13;
      const arcCY = by - 12;
      content += `<path d="M${arcCX},${arcCY + 4} a5,5 0 0,1 0,-8" fill="none" stroke="#58a6ff" stroke-width="1.2" opacity=".5"/>`;
    }

    // Label text
    const labelX = Math.round(sx + iconW + iconGap);
    const textYP = Math.round(midY + 4);
    content += `<text x="${labelX}" y="${textYP}" fill="#c9d1d9" font-size="11" font-weight="bold" font-family="Helvetica,Arial,sans-serif" text-rendering="geometricPrecision">${escapeXml(link.label)}</text>`;

    // Status dot with glow
    const dotCX = Math.round(labelX + labelPx + labelGap + dotR);
    const dotCY = midY;
    if (isUp) {
      content += `<circle cx="${dotCX}" cy="${dotCY}" r="${dotR + 3}" fill="${statusColor}" opacity=".15"/>`;
    }
    content += `<circle cx="${dotCX}" cy="${dotCY}" r="${dotR}" fill="${statusColor}"/>`;

    // Status text
    const statusX = Math.round(dotCX + dotR + dotGap);
    content += `<text x="${statusX}" y="${textYP}" fill="${statusColor}" font-size="11" font-weight="bold" font-family="Helvetica,Arial,sans-serif" text-rendering="geometricPrecision">${escapeXml(statusText)}</text>`;

    // Dashed divider
    if (i < 2) {
      const dx = Math.round(bx + secW);
      content += `<line x1="${dx}" y1="10" x2="${dx}" y2="${panelH - 10}" stroke="#30363d" stroke-dasharray="2,3" shape-rendering="crispEdges"/>`;
    }
  });

  return `<svg xmlns="http://www.w3.org/2000/svg" width="${panelW}" height="${panelH}" viewBox="0 0 ${panelW} ${panelH}" role="img" aria-label="Network Status"><title>Network Status</title><defs><linearGradient id="nhi" x2="0" y2="100%"><stop offset="0" stop-color="#fff" stop-opacity=".06"/><stop offset=".5" stop-color="#fff" stop-opacity="0"/></linearGradient><clipPath id="nr"><rect width="${panelW}" height="${panelH}" rx="${radius}"/></clipPath></defs><g clip-path="url(#nr)"><rect width="${panelW}" height="${panelH}" fill="${BG}" shape-rendering="crispEdges"/><rect width="${panelW}" height="${panelH}" fill="url(#nhi)"/><rect width="${panelW}" height="${panelH}" fill="none" stroke="${BORDER}" stroke-width="1" rx="${radius}"/>${content}</g></svg>`;
}

// --- Allowed metrics ---
const ALLOWED_METRICS = new Set([
  "talos_version", "kubernetes_version", "flux_version",
  "cluster_node_count", "cluster_pod_count", "cluster_cpu_usage",
  "cluster_memory_usage", "cluster_age_days", "cluster_uptime_days",
  "cluster_alert_count", "ceph_storage_used", "ceph_health",
  "cert_expiry_days", "flux_failing_count", "helmrelease_count",
  "pvc_count", "container_count", "wan_primary", "wan_cellular1", "wan_cellular2",
  "network_status", "renovate",
]);

// --- Response helpers ---
const SECURITY_HEADERS = {
  "Access-Control-Allow-Origin": "*",
  "Cache-Control": "no-cache, max-age=0",
  "X-Robots-Tag": "noindex",
  "Referrer-Policy": "no-referrer",
  "X-Content-Type-Options": "nosniff",
  "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
};

function svgResponse(svg, status) {
  return new Response(svg, {
    status,
    headers: { ...SECURITY_HEADERS, "Content-Type": "image/svg+xml" },
  });
}

function jsonResponse(data, status) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { ...SECURITY_HEADERS, "Content-Type": "application/json" },
  });
}

// --- Origin fetch helpers ---
async function fetchKromgoMetric(metric, env) {
  const resp = await fetch(`https://kromgo.${env.SECRET_DOMAIN}/${metric}`, {
    headers: { "CF-Access-Client-Id": env.CF_CLIENT_ID, "CF-Access-Client-Secret": env.CF_CLIENT_SECRET },
    signal: AbortSignal.timeout(5000),
  });
  const ct = resp.headers.get("content-type") || "";
  if (ct.includes("text/html")) return { ok: false, error: "auth" };
  if (!resp.ok) return { ok: false, error: "unavailable" };
  return { ok: true, data: await resp.json() };
}

async function fetchWanMetric(metric, env) {
  try {
    const result = await fetchKromgoMetric(metric, env);
    if (!result.ok) return { message: "ERR", color: "grey" };
    return result.data;
  } catch {
    return { message: "ERR", color: "grey" };
  }
}

// --- Metric rendering (produces final Response) ---
async function renderMetric(metricName, url, env) {
  // Network status panel — 3 parallel fetches
  if (metricName === "network_status") {
    const [fiber, cell1, cell2] = await Promise.all([
      fetchWanMetric("wan_primary", env),
      fetchWanMetric("wan_cellular1", env),
      fetchWanMetric("wan_cellular2", env),
    ]);
    return svgResponse(makeNetworkPanel([
      { label: "FIBER", icon: "bolt", message: fiber.message, color: fiber.color },
      { label: "CELL 1", icon: "signal", message: cell1.message, color: cell1.color },
      { label: "CELL 2", icon: "signal", message: cell2.message, color: cell2.color },
    ]), 200);
  }

  // Renovate workflow status — GitHub Actions API
  if (metricName === "renovate") {
    if (!env.GIT_PAT) {
      return svgResponse(makeBadge("renovate", "no token", "critical", "renovatebot"), 500);
    }
    try {
      const ghResp = await fetch(
        "https://api.github.com/repos/GizmoTickler/home-ops/actions/workflows/renovate.yaml/runs?branch=main&per_page=1",
        {
          headers: {
            Authorization: `Bearer ${env.GIT_PAT}`,
            Accept: "application/vnd.github+json",
            "User-Agent": "kromgo-proxy-worker",
          },
          signal: AbortSignal.timeout(5000),
        },
      );
      if (!ghResp.ok) {
        return svgResponse(makeBadge("renovate", "api error", "critical", "renovatebot"), 503);
      }
      const data = await ghResp.json();
      const run = data.workflow_runs?.[0];
      if (!run) {
        return svgResponse(makeBadge("renovate", "no runs", "lightgrey", "renovatebot"), 200);
      }
      let message, badgeColor;
      if (run.status !== "completed") {
        message = "running";
        badgeColor = "yellow";
      } else {
        switch (run.conclusion) {
          case "success":   message = "passing"; badgeColor = "brightgreen"; break;
          case "failure":   message = "failing"; badgeColor = "red"; break;
          case "cancelled": message = "cancelled"; badgeColor = "orange"; break;
          case "skipped":   message = "skipped"; badgeColor = "lightgrey"; break;
          default:          message = run.conclusion || "unknown"; badgeColor = "lightgrey";
        }
      }
      const label = url.searchParams.get("label") || "Renovate";
      const color = url.searchParams.get("color") || badgeColor;
      const logo = url.searchParams.get("logo") || "renovatebot";
      return svgResponse(makeBadge(label, message, color, logo, { labelBg: LABEL_BG_VERSION }), 200);
    } catch {
      return svgResponse(makeBadge("renovate", "timeout", "lightgrey", "renovatebot"), 503);
    }
  }

  // Standard kromgo metric
  const wantJson = url.searchParams.has("json");
  try {
    const result = await fetchKromgoMetric(metricName, env);
    if (!result.ok) {
      if (result.error === "auth") {
        return wantJson
          ? jsonResponse({ schemaVersion: 1, label: "kromgo", message: "auth failed", color: "critical" }, 503)
          : svgResponse(makeBadge("kromgo", "auth failed", "critical"), 503);
      }
      return wantJson
        ? jsonResponse({ schemaVersion: 1, label: "error", message: "unavailable", color: "lightgrey" }, 503)
        : svgResponse(makeBadge("error", "unavailable", "lightgrey"), 503);
    }
    if (wantJson) return jsonResponse(result.data, 200);
    const label = url.searchParams.get("label") || result.data.label || metricName;
    const color = url.searchParams.get("color") || result.data.color;
    const logo = url.searchParams.get("logo") || METRIC_ICON_MAP[metricName] || null;
    const isVersion = metricName.endsWith("_version");
    return svgResponse(makeBadge(label, result.data.message, color, logo, isVersion ? { labelBg: LABEL_BG_VERSION } : {}), 200);
  } catch {
    return wantJson
      ? jsonResponse({ schemaVersion: 1, label: "error", message: "timeout", color: "lightgrey" }, 503)
      : svgResponse(makeBadge("error", "timeout", "lightgrey"), 503);
  }
}

// --- Main handler ---
var index_default = {
  async fetch(request, env, ctx) {
    if (request.method === "OPTIONS") {
      return new Response(null, {
        headers: { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Methods": "GET", "Access-Control-Max-Age": "86400" },
      });
    }
    if (request.method !== "GET") return new Response("Method not allowed", { status: 405 });

    const clientIp = request.headers.get("CF-Connecting-IP") || "unknown";
    if (isRateLimited(clientIp)) {
      return new Response("Too many requests", {
        status: 429,
        headers: { "Retry-After": "60", "Content-Type": "text/plain" },
      });
    }

    const url = new URL(request.url);
    const metricName = url.pathname.substring(1);

    // Static assets — long-lived cache, no edge cache needed
    if (metricName === "logo") return serveLogo();

    if (!env.CF_CLIENT_ID || !env.CF_CLIENT_SECRET || !env.SECRET_DOMAIN) {
      return svgResponse(makeBadge("error", "misconfigured", "critical"), 500);
    }

    if (!metricName || !ALLOWED_METRICS.has(metricName)) {
      return svgResponse(makeBadge("error", "not found", "red"), 404);
    }

    // All metric endpoints go through edge cache
    return withEdgeCache(request, () => renderMetric(metricName, url, env), ctx);
  },
};

// --- Logo ---
import LOGO_B64 from "./logo.b64.txt";

let _logoBytes;
function serveLogo() {
  if (!_logoBytes) {
    _logoBytes = Uint8Array.from(atob(LOGO_B64), (c) => c.charCodeAt(0));
  }
  return new Response(_logoBytes, {
    headers: {
      "Content-Type": "image/png",
      "Cache-Control": "public, max-age=86400, s-maxage=604800",
      "Access-Control-Allow-Origin": "*",
      "X-Robots-Tag": "noindex",
    },
  });
}

export { index_default as default };
