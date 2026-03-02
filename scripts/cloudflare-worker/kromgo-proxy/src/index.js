// src/index.js — Kromgo badge proxy
// Generates polished SVG badges with rounded corners, gradients, and logos

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

  const textY = HEIGHT * 5 + 10; // vertical center in 10x space

  return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${tW}" height="${HEIGHT}" role="img" aria-label="${escapeXml(label)}: ${escapeXml(message)}"><title>${escapeXml(label)}: ${escapeXml(message)}</title><linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient><clipPath id="r"><rect width="${tW}" height="${HEIGHT}" rx="${RADIUS}" fill="#fff"/></clipPath><g clip-path="url(#r)"><rect width="${lW}" height="${HEIGHT}" fill="#555"/><rect x="${lW}" width="${mW}" height="${HEIGHT}" fill="${hex}"/><rect width="${tW}" height="${HEIGHT}" fill="url(#s)"/></g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="100">${logoEl}<text aria-hidden="true" transform="scale(.1)" x="${labelTX}" y="${textY + 10}" fill="#010101" fill-opacity=".3" textLength="${labelTW}">${escapeXml(label)}</text><text transform="scale(.1)" x="${labelTX}" y="${textY}" fill="#fff" textLength="${labelTW}">${escapeXml(label)}</text><text aria-hidden="true" transform="scale(.1)" x="${msgTX}" y="${textY + 10}" fill="#010101" fill-opacity=".3" textLength="${msgTW}" font-weight="bold">${escapeXml(message)}</text><text transform="scale(.1)" x="${msgTX}" y="${textY}" fill="#fff" textLength="${msgTW}" font-weight="bold">${escapeXml(message)}</text></g></svg>`;
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
]);

var index_default = {
  async fetch(request, env) {
    if (request.method === "OPTIONS") {
      return new Response(null, {
        headers: { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Methods": "GET", "Access-Control-Max-Age": "86400" },
      });
    }
    if (request.method !== "GET") return new Response("Method not allowed", { status: 405 });
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
