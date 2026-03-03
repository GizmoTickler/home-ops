// src/index.js — Kromgo badge proxy
// Generates polished SVG badges with rounded corners, gradients, and logos

// --- Rate limiter (per-isolate, sliding window) ---
const RATE_LIMIT = 60;          // max requests per window
const RATE_WINDOW_MS = 60_000;  // 1 minute window
const rateMap = new Map();      // IP -> { count, resetAt }

function isRateLimited(ip) {
  const now = Date.now();
  let entry = rateMap.get(ip);
  if (!entry || now > entry.resetAt) {
    entry = { count: 0, resetAt: now + RATE_WINDOW_MS };
    rateMap.set(ip, entry);
  }
  entry.count++;
  // Evict stale entries every 256 requests to prevent memory bloat
  if (rateMap.size > 1024) {
    for (const [key, val] of rateMap) {
      if (now > val.resetAt) rateMap.delete(key);
    }
  }
  return entry.count > RATE_LIMIT;
}

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

// Verdana Bold 10px character widths (in 10x units for sub-pixel precision)
const W = {
  " ": 33, "%": 86, ".": 33, "/": 46, "-": 40,
  0: 66, 1: 66, 2: 66, 3: 66, 4: 66, 5: 66, 6: 66, 7: 66, 8: 66, 9: 66,
  A: 73, B: 73, C: 70, D: 80, E: 66, F: 61, G: 80, H: 80, I: 46, J: 53,
  K: 73, L: 61, M: 93, N: 80, O: 80, P: 66, Q: 80, R: 73, S: 66, T: 61,
  U: 80, V: 73, W: 100, X: 70, Y: 66, Z: 66,
};
const LETTER_SP = 15; // per-character spacing in 10x units

function tw(text) {
  let w = 0;
  for (let i = 0; i < text.length; i++) {
    w += W[text[i]] || W[text[i].toUpperCase()] || 73;
    if (i < text.length - 1) w += LETTER_SP;
  }
  return w;
}

// Full SVG logos (Simple Icons, white fill, 24x24 viewBox) — base64 encoded
const LOGOS = {
  kubernetes: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTEwLjIwNCAxNC4zNWwuMDA3LjAxLS45OTkgMi40MTNhNS4xNzEgNS4xNzEgMCAwIDEtMi4wNzUtMi41OTdsMi41NzgtLjQzNy4wMDQuMDA1YS40NC40NCAwIDAgMSAuNDg0LjYwNnptLS44MzMtMi4xMjlhLjQ0LjQ0IDAgMCAwIC4xNzMtLjc1NmwuMDAyLS4wMTFMNy41ODUgOS43YTUuMTQzIDUuMTQzIDAgMCAwLS43MyAzLjI1NWwyLjUxNC0uNzI1LjAwMi0uMDA5em0xLjE0NS0xLjk4YS40NC40NCAwIDAgMCAuNjk5LS4zMzdsLjAxLS4wMDUuMTUtMi42MmE1LjE0NCA1LjE0NCAwIDAgMC0zLjAxIDEuNDQybDIuMTQ3IDEuNTIzLjAwNC0uMDAyem0uNzYgMi43NWwuNzIzLjM0OS43MjItLjM0Ny4xOC0uNzgtLjUtLjYyM2gtLjgwNGwtLjUuNjIzLjE3OS43Nzl6bTEuNS0zLjA5NWEuNDQuNDQgMCAwIDAgLjcuMzM2bC4wMDguMDAzIDIuMTM0LTEuNTEzYTUuMTg4IDUuMTg4IDAgMCAwLTIuOTkyLTEuNDQybC4xNDggMi42MTUuMDAyLjAwMXptMTAuODc2IDUuOTdsLTUuNzczIDcuMTgxYTEuNiAxLjYgMCAwIDEtMS4yNDguNTk0bC05LjI2MS4wMDNhMS42IDEuNiAwIDAgMS0xLjI0Ny0uNTk2bC01Ljc3Ni03LjE4YTEuNTgzIDEuNTgzIDAgMCAxLS4zMDctMS4zNEwyLjEgNS41NzNjLjEwOC0uNDcuNDI1LS44NjQuODYzLTEuMDczTDExLjMwNS41MTNhMS42MDYgMS42MDYgMCAwIDEgMS4zODUgMGw4LjM0NSAzLjk4NWMuNDM4LjIwOS43NTUuNjA0Ljg2MyAxLjA3M2wyLjA2MiA4Ljk1NWMuMTA4LjQ3LS4wMDUuOTYzLS4zMDggMS4zNHptLTMuMjg5LTIuMDU3Yy0uMDQyLS4wMS0uMTAzLS4wMjYtLjE0NS0uMDM0LS4xNzQtLjAzMy0uMzE1LS4wMjUtLjQ3OS0uMDM4LS4zNS0uMDM3LS42MzgtLjA2Ny0uODk1LS4xNDgtLjEwNS0uMDQtLjE4LS4xNjUtLjIxNi0uMjE2bC0uMjAxLS4wNTlhNi40NSA2LjQ1IDAgMCAwLS4xMDUtMi4zMzIgNi40NjUgNi40NjUgMCAwIDAtLjkzNi0yLjE2M2MuMDUyLS4wNDcuMTUtLjEzMy4xNzctLjE1OS4wMDgtLjA5LjAwMS0uMTgzLjA5NC0uMjgyLjE5Ny0uMTg1LjQ0NC0uMzM4Ljc0My0uNTIyLjE0Mi0uMDg0LjI3My0uMTM3LjQxNS0uMjQyLjAzMi0uMDI0LjA3Ni0uMDYyLjExLS4wODkuMjQtLjE5MS4yOTUtLjUyLjEyMy0uNzM2LS4xNzItLjIxNi0uNTA2LS4yMzYtLjc0NS0uMDQ1LS4wMzQuMDI3LS4wOC4wNjItLjExMS4wODgtLjEzNC4xMTYtLjIxNy4yMy0uMzMuMzUtLjI0Ni4yNS0uNDUuNDU4LS42NzMuNjA5LS4wOTcuMDU2LS4yMzkuMDM3LS4zMDMuMDMzbC0uMTkuMTM1YTYuNTQ1IDYuNTQ1IDAgMCAwLTQuMTQ2LTIuMDAzbC0uMDEyLS4yMjNjLS4wNjUtLjA2Mi0uMTQzLS4xMTUtLjE2My0uMjUtLjAyMi0uMjY4LjAxNS0uNTU3LjA1Ny0uOTA1LjAyMy0uMTYzLjA2MS0uMjk4LjA2OC0uNDc1LjAwMS0uMDQtLjAwMS0uMDk5LS4wMDEtLjE0MiAwLS4zMDYtLjIyNC0uNTU1LS41LS41NTUtLjI3NSAwLS40OTkuMjQ5LS40OTkuNTU1bC4wMDEuMDE0YzAgLjA0MS0uMDAyLjA5MiAwIC4xMjguMDA2LjE3Ny4wNDQuMzEyLjA2Ny40NzUuMDQyLjM0OC4wNzguNjM3LjA1Ni45MDZhLjU0NS41NDUgMCAwIDEtLjE2Mi4yNThsLS4wMTIuMjExYTYuNDI0IDYuNDI0IDAgMCAwLTQuMTY2IDIuMDAzIDguMzczIDguMzczIDAgMCAxLS4xOC0uMTI4Yy0uMDkuMDEyLS4xOC4wNC0uMjk3LS4wMjktLjIyMy0uMTUtLjQyNy0uMzU4LS42NzMtLjYwOC0uMTEzLS4xMi0uMTk1LS4yMzQtLjMyOS0uMzQ5LS4wMy0uMDI2LS4wNzctLjA2Mi0uMTExLS4wODhhLjU5NC41OTQgMCAwIDAtLjM0OC0uMTMyLjQ4MS40ODEgMCAwIDAtLjM5OC4xNzZjLS4xNzIuMjE2LS4xMTcuNTQ2LjEyMy43MzdsLjAwNy4wMDUuMTA0LjA4M2MuMTQyLjEwNS4yNzIuMTU5LjQxNC4yNDIuMjk5LjE4NS41NDYuMzM4Ljc0My41MjIuMDc2LjA4Mi4wOS4yMjYuMS4yODhsLjE2LjE0M2E2LjQ2MiA2LjQ2MiAwIDAgMC0xLjAyIDQuNTA2bC0uMjA4LjA2Yy0uMDU1LjA3Mi0uMTMzLjE4NC0uMjE1LjIxNy0uMjU3LjA4MS0uNTQ2LjExLS44OTUuMTQ3LS4xNjQuMDE0LS4zMDUuMDA2LS40OC4wMzktLjAzNy4wMDctLjA5LjAyLS4xMzMuMDNsLS4wMDQuMDAyLS4wMDcuMDAyYy0uMjk1LjA3MS0uNDg0LjM0Mi0uNDIzLjYwOC4wNjEuMjY3LjM0OS40MjkuNjQ1LjM2NWwuMDA3LS4wMDEuMDEtLjAwMy4xMjktLjAyOWMuMTctLjA0Ni4yOTQtLjExMy40NDgtLjE3Mi4zMy0uMTE4LjYwNC0uMjE3Ljg3LS4yNTYuMTEyLS4wMDkuMjMuMDY5LjI4OC4xMDFsLjIxNy0uMDM3YTYuNSA2LjUgMCAwIDAgMi44OCAzLjU5NmwtLjA5LjIxOGMuMDMzLjA4NC4wNjkuMTk5LjA0NC4yODItLjA5Ny4yNTItLjI2My41MTctLjQ1Mi44MTMtLjA5MS4xMzYtLjE4NS4yNDItLjI2OC4zOTktLjAyLjAzNy0uMDQ1LjA5NS0uMDY0LjEzNC0uMTI4LjI3NS0uMDM0LjU5MS4yMTMuNzEuMjQ4LjEyLjU1Ni0uMDA3LjY5LS4yODJ2LS4wMDJjLjAyLS4wMzkuMDQ2LS4wOS4wNjItLjEyNy4wNy0uMTYyLjA5NC0uMzAxLjE0NC0uNDU4LjEzMi0uMzMyLjIwNS0uNjguMzg3LS44OTcuMDUtLjA2LjEzLS4wODIuMjE1LS4xMDVsLjExMy0uMjA1YTYuNDUzIDYuNDUzIDAgMCAwIDQuNjA5LjAxMmwuMTA2LjE5MmMuMDg2LjAyOC4xOC4wNDIuMjU2LjE1NS4xMzYuMjMyLjIyOS41MDcuMzQyLjg0LjA1LjE1Ni4wNzQuMjk1LjE0NS40NTcuMDE2LjAzNy4wNDMuMDkuMDYyLjEyOS4xMzMuMjc2LjQ0Mi40MDIuNjkuMjgyLjI0Ny0uMTE4LjM0MS0uNDM1LjIxMy0uNzEtLjAyLS4wMzktLjA0NS0uMDk2LS4wNjUtLjEzNC0uMDgzLS4xNTYtLjE3Ny0uMjYxLS4yNjgtLjM5OC0uMTktLjI5Ni0uMzQ2LS41NDEtLjQ0My0uNzkzLS4wNC0uMTMuMDA3LS4yMS4wMzgtLjI5NC0uMDE4LS4wMjItLjA1OS0uMTQ0LS4wODMtLjIwMmE2LjQ5OSA2LjQ5OSAwIDAgMCAyLjg4LTMuNjIyYy4wNjQuMDEuMTc2LjAzLjIxMy4wMzguMDc1LS4wNS4xNDQtLjExNC4yOC0uMTA0LjI2Ni4wMzkuNTQuMTM4Ljg3LjI1Ni4xNTQuMDYuMjc3LjEyOC40NDguMTczLjAzNi4wMS4wODguMDE5LjEzLjAyOGwuMDA5LjAwMy4wMDcuMDAxYy4yOTcuMDY0LjU4NC0uMDk4LjY0NS0uMzY1LjA2LS4yNjYtLjEyOC0uNTM3LS40MjMtLjYwOHpNMTYuNCA5LjcwMWwtMS45NSAxLjc0NnYuMDA1YS40NC40NCAwIDAgMCAuMTczLjc1N2wuMDAzLjAxIDIuNTI2LjcyOGE1LjE5OSA1LjE5OSAwIDAgMC0uMTA4LTEuNjc0QTUuMjA4IDUuMjA4IDAgMCAwIDE2LjQgOS43em0tNC4wMTMgNS4zMjVhLjQzNy40MzcgMCAwIDAtLjQwNC0uMjMyLjQ0LjQ0IDAgMCAwLS4zNzIuMjMzaC0uMDAybC0xLjI2OCAyLjI5MmE1LjE2NCA1LjE2NCAwIDAgMCAzLjMyNi4wMDNsLTEuMjctMi4yOTZoLS4wMXptMS44ODgtMS4yOTNhLjQ0LjQ0IDAgMCAwLS4yNy4wMzYuNDQuNDQgMCAwIDAtLjIxNC41NzJsLS4wMDMuMDA0IDEuMDEgMi40MzhhNS4xNSA1LjE1IDAgMCAwIDIuMDgxLTIuNjE1bC0yLjYtLjQ0LS4wMDQuMDA1eiIvPjwvc3ZnPg==",
  talos: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTkuNjc4IDExLjk4YzAtMi42NjQtMS4xMy02Ljg5Ni0yLjg2Ny0xMC44MDRhMTIgMTIgMCAwIDAtMS41ODUuOTE3YzEuNjA4IDMuNjY4IDIuNjQ3IDcuNTUzIDIuNjQ3IDkuODg2IDAgMi4yNTQtMS4wOCA2LjE0NS0yLjczNSA5Ljg2NWExMiAxMiAwIDAgMCAxLjU3Ni45M2MxLjc5LTMuOTc2IDIuOTY0LTguMjI5IDIuOTY0LTEwLjc5NW02LjQ0MiAwYzAtMi4zMzYgMS4wNDItNi4yMiAyLjY0Ni05Ljg5YTEyIDEyIDAgMCAwLTEuNjA4LS45MjJjLTEuNzU2IDMuOTU3LTIuODQzIDguMTY2LTIuODQzIDEwLjgxNiAwIDIuNTY0IDEuMTc3IDYuODE5IDIuOTY1IDEwLjc5N2ExMiAxMiAwIDAgMCAxLjU3NS0uOTMxYy0xLjY1NS0zLjcyMy0yLjczNS03LjYxNi0yLjczNS05Ljg3bTUuNDUgNi41MjUuMzEuMzA3YTEyIDEyIDAgMCAwIC45MzYtMS42MTJjLTEuODY2LTEuODkzLTMuNDU3LTMuOTM4LTMuNDctNS4yMzMtLjAxMi0xLjI2NCAxLjU3LTMuMzA4IDMuNDQ2LTUuMjIyYTEyIDEyIDAgMCAwLS45NDUtMS42MDNsLS4yNTkuMjU4Yy0yLjczOSAyLjc2Ni00LjA2MyA0LjkyLTQuMDQ3IDYuNTgzLjAxNiAxLjY2MiAxLjMzMiAzLjgxIDQuMDI4IDYuNTIyTTIuNDExIDUuNDA1bC0uMjYtLjI1OWExMiAxMiAwIDAgMC0uOTQ2IDEuNjA4YzMuMTIzIDMuMTczIDMuNDUyIDQuNzA0IDMuNDQ4IDUuMjE3LS4wMTIgMS4zLTEuNjAzIDMuMzQtMy40NyA1LjIyOWExMiAxMiAwIDAgMCAuOTM5IDEuNjA4Yy4xMDYtLjEwNi4yMDctLjIwNC4zMS0uMzA4IDIuNjk0LTIuNzExIDQuMDEtNC44NDIgNC4wMjYtNi41MTZzLTEuMzA4LTMuODA5LTQuMDQ3LTYuNThNMTIuMDAyIDI0Yy4zMDMgMCAuNjAyLS4wMTYuODk4LS4wMzdWLjAzN0ExMiAxMiAwIDAgMCAxMiAwYy0uMzA0IDAtLjYwNS4wMTUtLjkwNS4wMzd2MjMuOTI1cS40NDguMDM1LjkwMy4wMzh6Ii8+PC9zdmc+",
  flux: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTExLjQwMiAyMy43NDdjLjE1NC4wNzUuMzA2LjE1NC40NTQuMjM4LjE4MS4wMzguMzcuMDA0LjUyNS0uMDk3bC4zODYtLjI1MWMtMS4yNDItLjgzMS0yLjYyMi0xLjI1MS0zLjk5OC0xLjYwMmwyLjYzMyAxLjcxMlptLTcuNDk1LTUuNzgzYTguMDg4IDguMDg4IDAgMCAxLS4yMjItLjIzNi42OTYuNjk2IDAgMCAwIC4xMTIgMS4wNzVsMi4zMDQgMS40OThjMS4wMTkuNDIyIDIuMDg1LjY4NiAzLjEzNC45NDQgMS42MzYuNDAzIDMuMi43OSA0LjU1NCAxLjcyOGwuNjk3LS40NTNjLTEuNTQxLTEuMTU4LTMuMzI3LTEuNjAyLTUuMDY1LTIuMDMtMi4wMzktLjUwMy0zLjk2NS0uOTc3LTUuNTE0LTIuNTI2Wm0xLjQxNC0xLjMyMi0uNjY1LjQzMmMuMDIzLjAyNC4wNDQuMDQ5LjA2OC4wNzMgMS43MDIgMS43MDIgMy44MjUgMi4yMjUgNS44NzcgMi43MzEgMS43NzguNDM4IDMuNDY5Ljg1NiA0LjkgMS45ODJsLjY4Mi0uNDQ0Yy0xLjYxMi0xLjM1Ny0zLjUzMi0xLjgzNC01LjM5NS0yLjI5My0yLjAxOS0uNDk3LTMuOTI2LS45NjktNS40NjctMi40ODFabTcuNTAyIDIuMDg0YzEuNTk2LjQxMiAzLjA5Ni45MDQgNC4zNjcgMi4wMzZsLjY3LS40MzZjLTEuNDg0LTEuMzk2LTMuMjY2LTEuOTUzLTUuMDM3LTIuNDAzdi44MDNabS42OTgtMi4zMzdhNjQuNjk1IDY0LjY5NSAwIDAgMS0uNjk4LS4xNzR2LjgwMmwuNTEyLjEyN2MyLjAzOS41MDMgMy45NjUuOTc4IDUuNTE0IDIuNTI2bC4wMDcuMDA5LjY2My0uNDMxYy0uMDQxLS4wNDItLjA3OS0uMDg2LS4xMjEtLjEyOC0xLjcwMi0xLjcwMS0zLjgyNC0yLjIyNS01Ljg3Ny0yLjczMVptLS42OTgtMS45Mjh2LjgxNmMuNjI0LjE5IDEuMjU1LjM0NyAxLjg3OS41MDEgMi4wMzkuNTAyIDMuOTY1Ljk3NyA1LjUxMyAyLjUyNi4wNzcuMDc3LjE1My4xNTcuMjI2LjIzOWEuNzA0LjcwNCAwIDAgMC0uMjM4LS45MTFsLTMuMDY0LTEuOTkyYy0uNzQ0LS4yNDUtMS41MDItLjQzMy0yLjI1MS0uNjE4YTMxLjQzNiAzMS40MzYgMCAwIDEtMi4wNjUtLjU2MVptLTEuNjQ2IDMuMDQ5Yy0xLjUyNi0uNC0yLjk2LS44ODgtNC4xODUtMS45NTVsLS42NzQuNDM5YzEuNDM5IDEuMzI2IDMuMTUxIDEuODggNC44NTkgMi4zMTl2LS44MDNabTAtMS43NzJhOC41NDMgOC41NDMgMCAwIDEtMi40OTItMS4yODNsLS42ODYuNDQ2Yy45NzUuODA0IDIuMDYxIDEuMjkzIDMuMTc4IDEuNjU1di0uODE4Wm0wLTEuOTQ2YTcuNTkgNy41OSAwIDAgMS0uNzc2LS40NTNsLS43MDEuNDU2Yy40NjIuMzM3Ljk1Ny42MjcgMS40NzcuODY1di0uODY4Wm0zLjUzMy4yNjktMS44ODctMS4yMjZ2LjU4MWMuNjE0LjI1NyAxLjI0NC40NzMgMS44ODcuNjQ1Wm01LjQ5My04Ljg2M0wxMi4zODEuMTEyYS43MDUuNzA1IDAgMCAwLS43NjIgMEwzLjc5NyA1LjE5OGEuNjk4LjY5OCAwIDAgMCAwIDEuMTcxbDcuMzggNC43OTdWNy42NzhhLjQxNC40MTQgMCAwIDAtLjQxMi0uNDEyaC0uNTQzYS40MTMuNDEzIDAgMCAxLS4zNTYtLjYxN2wxLjc3Ny0zLjA3OWEuNDEyLjQxMiAwIDAgMSAuNzE0IDBsMS43NzcgMy4wNzlhLjQxMy40MTMgMCAwIDEtLjM1Ni42MTdoLS41NDNhLjQxNC40MTQgMCAwIDAtLjQxMi40MTJ2My40ODhsNy4zOC00Ljc5N2EuNy43IDAgMCAwIDAtMS4xNzFaIi8+PC9zdmc+",
  renovatebot: "PHN2ZyBmaWxsPSJ3aGl0ZSIgcm9sZT0iaW1nIiB2aWV3Qm94PSIwIDAgMjQgMjQiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyI+PHBhdGggZD0iTTE3LjU3NiAxMC44NTJjLS4xMDggMC0uMjE2LjAxOC0uMzI0LjA1NGExLjM0NCAxLjM0NCAwIDAgMC0uOTE4IDEuMTg4Yy0uMDE4LjM5Ni4xMjYuNzU2LjM5NiAxLjAyNi4yNy4yNTIuNjMuMzk2IDEuMDI2LjM5NmExLjM4IDEuMzggMCAwIDAgMS4wOC0uNTA0Yy4yNy0uMzA2LjM3OC0uNzAyLjMwNi0xLjA5OGExLjM0NCAxLjM0NCAwIDAgMC0uOTE4LTEuMDA4IDEuMTY0IDEuMTY0IDAgMCAwLS42NDgtLjA1NHpNMTIgMEM1LjM3NiAwIDAgNS4zNzYgMCAxMnM1LjM3NiAxMiAxMiAxMiAxMi01LjM3NiAxMi0xMlMxOC42MjQgMCAxMiAwem01LjIwOCAxNC40MThhMy4xODYgMy4xODYgMCAwIDEtMS43NjQgMS4xMTYgMy4xOCAzLjE4IDAgMCAxLTIuMDctLjE5OGwtMy45MjQgNC41OTZjLS4zNzguNDMyLS44ODIuNjg0LTEuNDIyLjcwMmExLjk0NCAxLjk0NCAwIDAgMS0xLjQ1OC0uNTk0IDEuOTQ0IDEuOTQ0IDAgMCAxLS41OTQtMS40NThjLjAxOC0uNTQuMjctMS4wNDQuNzAyLTEuNDIybDQuNTk2LTMuOTI0YTMuMTggMy4xOCAwIDAgMS0uMTk4LTIuMDcgMy4xODYgMy4xODYgMCAwIDEgMS4xMTYtMS43NjQgMy4xNzQgMy4xNzQgMCAwIDEgMi44MjYtLjU5NGwtMS42MzggMS42MzguMTQ0IDEuNzI4IDEuNzI4LjE0NCAxLjYzOC0xLjYzOGEzLjE3NCAzLjE3NCAwIDAgMS0uNTk0IDIuODI2IDIuMDcgMi4wNyAwIDAgMS0uMTI2LjE2MnoiLz48L3N2Zz4K",
};

const HEIGHT = 30;
const RADIUS = 4;
const PAD = 130; // 13px padding in 10x units
const LOGO_SIZE = 18;
const LOGO_X = 8;
const LOGO_GAP = 40; // 4px gap after logo in 10x

function makeBadge(label, message, color, logoName) {
  label = (label || "").toUpperCase();
  message = (message || "").toUpperCase();
  const hex = resolveColor(color);

  const hasLogo = logoName && LOGOS[logoName];
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
  const logoEl = hasLogo
    ? `<image x="${LOGO_X}" y="${logoY}" width="${LOGO_SIZE}" height="${LOGO_SIZE}" href="data:image/svg+xml;base64,${LOGOS[logoName]}"/>`
    : "";

  // Vertical center: baseline = (badgeHeight + capHeight) / 2 in 10x units
  // Verdana Bold caps are ~72% of font-size (100 units), so capHeight ≈ 72
  const textY = (HEIGHT * 10 + 72) / 2; // ≈ 186 for 30px badge

  return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${tW}" height="${HEIGHT}" role="img" aria-label="${escapeXml(label)}: ${escapeXml(message)}"><title>${escapeXml(label)}: ${escapeXml(message)}</title><linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient><clipPath id="r"><rect width="${tW}" height="${HEIGHT}" rx="${RADIUS}" fill="#fff"/></clipPath><g clip-path="url(#r)"><rect width="${lW}" height="${HEIGHT}" fill="#555"/><rect x="${lW}" width="${mW}" height="${HEIGHT}" fill="${hex}"/><rect width="${tW}" height="${HEIGHT}" fill="url(#s)"/></g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="100">${logoEl}<text aria-hidden="true" transform="scale(.1)" x="${labelTX}" y="${textY + 10}" fill="#010101" fill-opacity=".3">${escapeXml(label)}</text><text transform="scale(.1)" x="${labelTX}" y="${textY}" fill="#fff">${escapeXml(label)}</text><text aria-hidden="true" transform="scale(.1)" x="${msgTX}" y="${textY + 10}" fill="#010101" fill-opacity=".3" font-weight="bold">${escapeXml(message)}</text><text transform="scale(.1)" x="${msgTX}" y="${textY}" fill="#fff" font-weight="bold">${escapeXml(message)}</text></g></svg>`;
}

function escapeXml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

const ALLOWED_METRICS = new Set([
  "talos_version", "kubernetes_version", "flux_version",
  "cluster_node_count", "cluster_pod_count", "cluster_cpu_usage",
  "cluster_memory_usage", "cluster_age_days", "cluster_uptime_days",
  "cluster_alert_count", "ceph_storage_used", "ceph_health",
  "cert_expiry_days", "flux_failing_count", "helmrelease_count",
  "pvc_count", "container_count", "wan_primary", "wan_cellular1", "wan_cellular2",
  "network_status", "renovate",
]);

var index_default = {
  async fetch(request, env) {
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

    if (!env.CF_CLIENT_ID || !env.CF_CLIENT_SECRET || !env.SECRET_DOMAIN) {
      return svgResponse(makeBadge("error", "misconfigured", "critical"), 500);
    }

    const url = new URL(request.url);
    const metricName = url.pathname.substring(1);

    // Proxy the repo logo image with caching
    if (metricName === "logo") {
      return serveLogo();
    }

    if (!metricName || !ALLOWED_METRICS.has(metricName)) {
      return svgResponse(makeBadge("error", "not found", "red"), 404);
    }

    // Unified network status panel — fetches all 3 WAN metrics
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

    // Renovate workflow status — fetches from GitHub Actions API
    if (metricName === "renovate") {
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
        return svgResponse(makeBadge(label, message, color, logo), 200);
      } catch {
        return svgResponse(makeBadge("renovate", "timeout", "lightgrey", "renovatebot"), 503);
      }
    }

    const wantJson = url.searchParams.has("json");
    try {
      const response = await fetch(`https://kromgo.${env.SECRET_DOMAIN}/${metricName}`, {
        headers: { "CF-Access-Client-Id": env.CF_CLIENT_ID, "CF-Access-Client-Secret": env.CF_CLIENT_SECRET },
        signal: AbortSignal.timeout(5000),
      });
      const ct = response.headers.get("content-type") || "";
      if (ct.includes("text/html")) {
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
      if (wantJson) return jsonResponse(data, 200);
      const label = url.searchParams.get("label") || data.label || metricName;
      const color = url.searchParams.get("color") || data.color;
      const logo = url.searchParams.get("logo") || null;
      return svgResponse(makeBadge(label, data.message, color, logo), 200);
    } catch (e) {
      return wantJson
        ? jsonResponse({ schemaVersion: 1, label: "error", message: "timeout", color: "lightgrey" }, 503)
        : svgResponse(makeBadge("error", "timeout", "lightgrey"), 503);
    }
  },
};

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

async function fetchWanMetric(metric, env) {
  try {
    const resp = await fetch(`https://kromgo.${env.SECRET_DOMAIN}/${metric}`, {
      headers: { "CF-Access-Client-Id": env.CF_CLIENT_ID, "CF-Access-Client-Secret": env.CF_CLIENT_SECRET },
      signal: AbortSignal.timeout(5000),
    });
    const ct = resp.headers.get("content-type") || "";
    if (ct.includes("text/html") || !resp.ok) return { message: "ERR", color: "grey" };
    return await resp.json();
  } catch {
    return { message: "ERR", color: "grey" };
  }
}

function makeNetworkPanel(links) {
  const secW = 156;
  const panelW = secW * 3;
  const panelH = 38;
  const radius = 8;
  const midY = panelH / 2;

  // Layout constants (pixels)
  const padL = 14, padR = 14;
  const iconW = 12, iconGap = 6;
  const dotGap = 8, dotDiam = 8, dotTextGap = 4;

  let content = "";

  links.forEach((link, i) => {
    const bx = i * secW;
    const statusText = (link.message || "?").toUpperCase();
    const dotColor = resolveColor(link.color);
    const labelPx = tw(link.label) * 11 / 100;
    const statusPx = tw(statusText) * 11 / 100;

    // Center content block within section
    const contentW = iconW + iconGap + labelPx + dotGap + dotDiam + dotTextGap + statusPx;
    const offset = Math.max(0, (secW - padL - padR - contentW) / 2);
    const sx = bx + padL + offset;

    // Icon
    if (link.icon === "bolt") {
      content += `<polygon points="${sx + 5},${midY - 7} ${sx + 1},${midY} ${sx + 3.5},${midY} ${sx + 2.5},${midY + 7} ${sx + 8},${midY - 2} ${sx + 5},${midY - 2}" fill="#f0c040"/>`;
    } else {
      const bb = midY + 7;
      content += `<rect x="${sx}" y="${bb - 5}" width="3" height="5" rx="0.5" fill="#58a6ff"/>`;
      content += `<rect x="${sx + 4.5}" y="${bb - 9}" width="3" height="9" rx="0.5" fill="#58a6ff"/>`;
      content += `<rect x="${sx + 9}" y="${bb - 13}" width="3" height="13" rx="0.5" fill="#58a6ff"/>`;
    }

    // Label
    const labelX = sx + iconW + iconGap;
    content += `<text x="${labelX}" y="${midY + 4}" fill="#fff" font-size="11" font-weight="bold" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision">${escapeXml(link.label)}</text>`;

    // Status dot with glow
    const dotCx = labelX + labelPx + dotGap + dotDiam / 2;
    content += `<circle cx="${dotCx}" cy="${midY}" r="6" fill="${dotColor}" opacity="0.25"/>`;
    content += `<circle cx="${dotCx}" cy="${midY}" r="4" fill="${dotColor}"/>`;

    // Status text
    const statusX = dotCx + dotDiam / 2 + dotTextGap;
    content += `<text x="${statusX}" y="${midY + 4}" fill="${dotColor}" font-size="11" font-weight="bold" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision">${escapeXml(statusText)}</text>`;

    // Divider
    if (i < 2) {
      content += `<line x1="${bx + secW}" y1="8" x2="${bx + secW}" y2="${panelH - 8}" stroke="#6e7681" stroke-opacity="0.4"/>`;
    }
  });

  return `<svg xmlns="http://www.w3.org/2000/svg" width="${panelW}" height="${panelH}" role="img" aria-label="Network Status"><title>Network Status</title><defs><linearGradient id="g" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient><clipPath id="r"><rect width="${panelW}" height="${panelH}" rx="${radius}"/></clipPath></defs><g clip-path="url(#r)"><rect width="${panelW}" height="${panelH}" fill="#555"/><rect width="${panelW}" height="${panelH}" fill="url(#g)"/>${content}</g></svg>`;
}

// Repo logo — 350x350 PNG (2x retina for 175px display), base64 in separate file
// Loaded at build time by wrangler's module bundler
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
