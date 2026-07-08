// src/index.js — Kromgo stat-tile proxy
// Renders README metric panels as uniform stat-tile rows (quiet cards: muted
// label, semibold value, status carried by a small dot — never a color block).
// Light/dark theming via prefers-color-scheme CSS inside the SVG, with stale
// fallback caching at the edge.

// --- Edge cache for stale fallback ---
// Always try to fetch fresh data for GET requests so README refreshes stay current.
// Keep the last good response for up to 15 minutes and serve it only when the
// origin fails. HEAD requests are handled locally to avoid double-fetching
// through Cloudflare Access on cache misses.
const CACHE_STALE_S = 900;
const CLIENT_CACHE_CONTROL = "no-cache, max-age=0";
const ERROR_CACHE_CONTROL = "no-store, max-age=0";

async function withEdgeCache(request, renderFn, ctx) {
  const cache = caches.default;
  const cacheReq = new Request(request.url, { method: "GET" });

  const cached = await cache.match(cacheReq);
  const cachedAt = parseInt(cached?.headers.get("x-fetch-time") || "0");
  const cachedAge = cachedAt ? (Date.now() - cachedAt) / 1000 : Number.POSITIVE_INFINITY;

  // README/image proxies often probe with HEAD before GET. Never forward HEAD
  // to origin; serve the last good response when possible, otherwise return a
  // cheap synthetic 200 so the subsequent GET can fetch fresh data.
  if (request.method === "HEAD") {
    if (cached && cachedAge < CACHE_STALE_S) {
      return toClientResponse(cached);
    }
    return headProbeResponse(request);
  }

  // Fetch synchronously so normal refreshes return the latest metric value.
  try {
    const resp = await renderFn();
    if (resp.status === 200) {
      const toReturn = resp.clone();
      ctx.waitUntil(putInCache(cache, cacheReq, resp));
      return toReturn;
    }
    // Non-200: prefer the last known-good tile over an error tile.
    if (cached && cachedAge < CACHE_STALE_S) return toClientResponse(cached);
    return resp;
  } catch (e) {
    if (cached && cachedAge < CACHE_STALE_S) return toClientResponse(cached);
    return svgResponse(makeTileSvg({ label: "Error", message: "timeout", status: true, color: "grey" }), 503);
  }
}

function toClientResponse(cached) {
  const ct = cached.headers.get("Content-Type") || "image/svg+xml";
  return new Response(cached.body, {
    status: 200,
    headers: {
      ...SECURITY_HEADERS,
      "Content-Type": ct,
      "Cache-Control": CLIENT_CACHE_CONTROL,
    },
  });
}

function headProbeResponse(request) {
  const url = new URL(request.url);
  const contentType = url.searchParams.has("json") ? "application/json" : "image/svg+xml";
  return new Response(null, {
    status: 200,
    headers: {
      ...SECURITY_HEADERS,
      "Content-Type": contentType,
      "Cache-Control": CLIENT_CACHE_CONTROL,
    },
  });
}

async function putInCache(cache, req, resp) {
  const body = await resp.arrayBuffer();
  const h = new Headers();
  h.set("Content-Type", resp.headers.get("Content-Type") || "image/svg+xml");
  h.set("x-fetch-time", Date.now().toString());
  h.set("Cache-Control", "public, s-maxage=900");
  await cache.put(req, new Response(body, { status: 200, headers: h }));
}

// --- Status mapping ---
// Kromgo color names collapse to four semantic states; color is only ever
// rendered as a small dot beside the value (GitHub Primer status tokens,
// contrast-validated on both tile surfaces).
function statusKind(color) {
  switch ((color || "").toLowerCase()) {
    case "green":
    case "brightgreen":
    case "yellowgreen":
      return "ok";
    case "yellow":
    case "orange":
      return "warn";
    case "red":
    case "critical":
      return "err";
    default:
      return "neu";
  }
}

// --- Inline icons (currentColor, themed via CSS) ---
// Each icon: [viewBoxW, viewBoxH, innerMarkup]. Fills/strokes use currentColor
// so the document stylesheet controls color in light and dark mode.
const ICONS = {
  flatcar: [137, 84, '<path fill="currentColor" d="M78.2191 12.1074H74.2061V16.1136H78.2191V12.1074Z"/><path fill="currentColor" d="M68.2618 46.1768H60.229V50.1829H68.2618V46.1768Z"/><path fill="currentColor" d="M104.406 46.1768H96.3735V50.1829H104.406V46.1768Z"/><path fill="currentColor" d="M132.497 72.1521V48.1014H128.498V72.1521H124.499V32.0699H116.445V0H20.0855V32.0699H12.039V72.1521H8.03965V48.1014H3.99934V72.1521H0V80.0075H8.03282V84H20.0786V80.0075H24.078V84H36.1238V80.0075H100.373V84H112.419V80.0075H116.418V84H128.464V80.0075H136.497V72.1521H132.497ZM90.2652 4.08807H110.344V12.1072H104.399V28.1456H96.3734V12.1072H90.272V4.08807H90.2652ZM26.0231 20.1264V4.08807H42.0887V10.0188H34.0559V14.0114H42.0887V20.1196H34.0559V28.1388H26.0231V20.1264ZM44.1839 46.1768V50.1693H32.1245V58.1885H44.1703V70.2139H20.0786V38.1576H44.1703L44.1839 46.1768ZM46.1085 28.1456V4.0949H54.1414V20.1332H62.1742V28.1524H46.0949H46.1085V28.1456ZM80.3078 46.1768V70.2275H68.2619V58.2022H60.2291V70.2275H48.1833V42.1706H52.1826V38.1781H76.2743V42.1706H80.2736L80.3078 46.1768ZM78.2194 28.1456V20.1264H74.22V28.1456H66.1804V8.10107H70.1797V4.10855H82.2255V8.10107H86.2249V28.1456H78.2194ZM116.445 46.1768V58.2022H112.446V62.1947H116.445V70.2139H104.399V62.2083H100.4V58.2158H96.4007V70.2411H84.3412V38.1644H116.445V46.1768Z"/>'],
  kubernetes: [24, 24, '<path fill="currentColor" d="M10.204 14.35l.007.01-.999 2.413a5.171 5.171 0 0 1-2.075-2.597l2.578-.437.004.005a.44.44 0 0 1 .484.606zm-.833-2.129a.44.44 0 0 0 .173-.756l.002-.011L7.585 9.7a5.143 5.143 0 0 0-.73 3.255l2.514-.725.002-.009zm1.145-1.98a.44.44 0 0 0 .699-.337l.01-.005.15-2.62a5.144 5.144 0 0 0-3.01 1.442l2.147 1.523.004-.002zm.76 2.75l.723.349.722-.347.18-.78-.5-.623h-.804l-.5.623.179.779zm1.5-3.095a.44.44 0 0 0 .7.336l.008.003 2.134-1.513a5.188 5.188 0 0 0-2.992-1.442l.148 2.615.002.001zm10.876 5.97l-5.773 7.181a1.6 1.6 0 0 1-1.248.594l-9.261.003a1.6 1.6 0 0 1-1.247-.596l-5.776-7.18a1.583 1.583 0 0 1-.307-1.34L2.1 5.573c.108-.47.425-.864.863-1.073L11.305.513a1.606 1.606 0 0 1 1.385 0l8.345 3.985c.438.209.755.604.863 1.073l2.062 8.955c.108.47-.005.963-.308 1.34zm-3.289-2.057c-.042-.01-.103-.026-.145-.034-.174-.033-.315-.025-.479-.038-.35-.037-.638-.067-.895-.148-.105-.04-.18-.165-.216-.216l-.201-.059a6.45 6.45 0 0 0-.105-2.332 6.465 6.465 0 0 0-.936-2.163c.052-.047.15-.133.177-.159.008-.09.001-.183.094-.282.197-.185.444-.338.743-.522.142-.084.273-.137.415-.242.032-.024.076-.062.11-.089.24-.191.295-.52.123-.736-.172-.216-.506-.236-.745-.045-.034.027-.08.062-.111.088-.134.116-.217.23-.33.35-.246.25-.45.458-.673.609-.097.056-.239.037-.303.033l-.19.135a6.545 6.545 0 0 0-4.146-2.003l-.012-.223c-.065-.062-.143-.115-.163-.25-.022-.268.015-.557.057-.905.023-.163.061-.298.068-.475.001-.04-.001-.099-.001-.142 0-.306-.224-.555-.5-.555-.275 0-.499.249-.499.555l.001.014c0 .041-.002.092 0 .128.006.177.044.312.067.475.042.348.078.637.056.906a.545.545 0 0 1-.162.258l-.012.211a6.424 6.424 0 0 0-4.166 2.003 8.373 8.373 0 0 1-.18-.128c-.09.012-.18.04-.297-.029-.223-.15-.427-.358-.673-.608-.113-.12-.195-.234-.329-.349-.03-.026-.077-.062-.111-.088a.594.594 0 0 0-.348-.132.481.481 0 0 0-.398.176c-.172.216-.117.546.123.737l.007.005.104.083c.142.105.272.159.414.242.299.185.546.338.743.522.076.082.09.226.1.288l.16.143a6.462 6.462 0 0 0-1.02 4.506l-.208.06c-.055.072-.133.184-.215.217-.257.081-.546.11-.895.147-.164.014-.305.006-.48.039-.037.007-.09.02-.133.03l-.004.002-.007.002c-.295.071-.484.342-.423.608.061.267.349.429.645.365l.007-.001.01-.003.129-.029c.17-.046.294-.113.448-.172.33-.118.604-.217.87-.256.112-.009.23.069.288.101l.217-.037a6.5 6.5 0 0 0 2.88 3.596l-.09.218c.033.084.069.199.044.282-.097.252-.263.517-.452.813-.091.136-.185.242-.268.399-.02.037-.045.095-.064.134-.128.275-.034.591.213.71.248.12.556-.007.69-.282v-.002c.02-.039.046-.09.062-.127.07-.162.094-.301.144-.458.132-.332.205-.68.387-.897.05-.06.13-.082.215-.105l.113-.205a6.453 6.453 0 0 0 4.609.012l.106.192c.086.028.18.042.256.155.136.232.229.507.342.84.05.156.074.295.145.457.016.037.043.09.062.129.133.276.442.402.69.282.247-.118.341-.435.213-.71-.02-.039-.045-.096-.065-.134-.083-.156-.177-.261-.268-.398-.19-.296-.346-.541-.443-.793-.04-.13.007-.21.038-.294-.018-.022-.059-.144-.083-.202a6.499 6.499 0 0 0 2.88-3.622c.064.01.176.03.213.038.075-.05.144-.114.28-.104.266.039.54.138.87.256.154.06.277.128.448.173.036.01.088.019.13.028l.009.003.007.001c.297.064.584-.098.645-.365.06-.266-.128-.537-.423-.608zM16.4 9.701l-1.95 1.746v.005a.44.44 0 0 0 .173.757l.003.01 2.526.728a5.199 5.199 0 0 0-.108-1.674A5.208 5.208 0 0 0 16.4 9.7zm-4.013 5.325a.437.437 0 0 0-.404-.232.44.44 0 0 0-.372.233h-.002l-1.268 2.292a5.164 5.164 0 0 0 3.326.003l-1.27-2.296h-.01zm1.888-1.293a.44.44 0 0 0-.27.036.44.44 0 0 0-.214.572l-.003.004 1.01 2.438a5.15 5.15 0 0 0 2.081-2.615l-2.6-.44-.004.005z"/>'],
  flux: [24, 24, '<path fill="currentColor" d="M11.402 23.747c.154.075.306.154.454.238.181.038.37.004.525-.097l.386-.251c-1.242-.831-2.622-1.251-3.998-1.602l2.633 1.712Zm-7.495-5.783a8.088 8.088 0 0 1-.222-.236.696.696 0 0 0 .112 1.075l2.304 1.498c1.019.422 2.085.686 3.134.944 1.636.403 3.2.79 4.554 1.728l.697-.453c-1.541-1.158-3.327-1.602-5.065-2.03-2.039-.503-3.965-.977-5.514-2.526Zm1.414-1.322-.665.432c.023.024.044.049.068.073 1.702 1.702 3.825 2.225 5.877 2.731 1.778.438 3.469.856 4.9 1.982l.682-.444c-1.612-1.357-3.532-1.834-5.395-2.293-2.019-.497-3.926-.969-5.467-2.481Zm7.502 2.084c1.596.412 3.096.904 4.367 2.036l.67-.436c-1.484-1.396-3.266-1.953-5.037-2.403v.803Zm.698-2.337a64.695 64.695 0 0 1-.698-.174v.802l.512.127c2.039.503 3.965.978 5.514 2.526l.007.009.663-.431c-.041-.042-.079-.086-.121-.128-1.702-1.701-3.824-2.225-5.877-2.731Zm-.698-1.928v.816c.624.19 1.255.347 1.879.501 2.039.502 3.965.977 5.513 2.526.077.077.153.157.226.239a.704.704 0 0 0-.238-.911l-3.064-1.992c-.744-.245-1.502-.433-2.251-.618a31.436 31.436 0 0 1-2.065-.561Zm-1.646 3.049c-1.526-.4-2.96-.888-4.185-1.955l-.674.439c1.439 1.326 3.151 1.88 4.859 2.319v-.803Zm0-1.772a8.543 8.543 0 0 1-2.492-1.283l-.686.446c.975.804 2.061 1.293 3.178 1.655v-.818Zm0-1.946a7.59 7.59 0 0 1-.776-.453l-.701.456c.462.337.957.627 1.477.865v-.868Zm3.533.269-1.887-1.226v.581c.614.257 1.244.473 1.887.645Zm5.493-8.863L12.381.112a.705.705 0 0 0-.762 0L3.797 5.198a.698.698 0 0 0 0 1.171l7.38 4.797V7.678a.414.414 0 0 0-.412-.412h-.543a.413.413 0 0 1-.356-.617l1.777-3.079a.412.412 0 0 1 .714 0l1.777 3.079a.413.413 0 0 1-.356.617h-.543a.414.414 0 0 0-.412.412v3.488l7.38-4.797a.7.7 0 0 0 0-1.171Z"/>'],
  renovatebot: [24, 24, '<path fill="currentColor" d="M17.576 10.852c-.108 0-.216.018-.324.054a1.344 1.344 0 0 0-.918 1.188c-.018.396.126.756.396 1.026.27.252.63.396 1.026.396a1.38 1.38 0 0 0 1.08-.504c.27-.306.378-.702.306-1.098a1.344 1.344 0 0 0-.918-1.008 1.164 1.164 0 0 0-.648-.054zM12 0C5.376 0 0 5.376 0 12s5.376 12 12 12 12-5.376 12-12S18.624 0 12 0zm5.208 14.418a3.186 3.186 0 0 1-1.764 1.116 3.18 3.18 0 0 1-2.07-.198l-3.924 4.596c-.378.432-.882.684-1.422.702a1.944 1.944 0 0 1-1.458-.594 1.944 1.944 0 0 1-.594-1.458c.018-.54.27-1.044.702-1.422l4.596-3.924a3.18 3.18 0 0 1-.198-2.07 3.186 3.186 0 0 1 1.116-1.764 3.174 3.174 0 0 1 2.826-.594l-1.638 1.638.144 1.728 1.728.144 1.638-1.638a3.174 3.174 0 0 1-.594 2.826 2.07 2.07 0 0 1-.126.162z"/>'],
  calendar: [24, 24, '<path fill="currentColor" d="M19 3h-1V1h-2v2H8V1H6v2H5c-1.11 0-2 .9-2 2v14c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V8h14v11z"/>'],
  uptime: [24, 24, '<path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8z"/><path fill="currentColor" d="M12 8l-4 4h3v4h2v-4h3z"/>'],
  node: [24, 24, '<g fill="currentColor"><rect x="3" y="3" width="18" height="7" rx="1.5"/><circle cx="7" cy="6.5" r="1.2"/><rect x="10" y="5.5" width="7" height="2" rx=".5"/><rect x="3" y="14" width="18" height="7" rx="1.5"/><circle cx="7" cy="17.5" r="1.2"/><rect x="10" y="16.5" width="7" height="2" rx=".5"/></g>'],
  alert: [24, 24, '<path fill="currentColor" d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/>'],
  pod: [24, 24, '<path fill="currentColor" d="M21 16.5c0 .38-.21.71-.53.88l-7.9 4.44c-.16.12-.36.18-.57.18s-.41-.06-.57-.18l-7.9-4.44A.991.991 0 013 16.5v-9c0-.38.21-.71.53-.88l7.9-4.44c.16-.12.36-.18.57-.18s.41.06.57.18l7.9 4.44c.32.17.53.5.53.88v9zM12 5.15L5.04 9.03 12 12.92l6.96-3.89L12 5.15z"/>'],
  container: [24, 24, '<g stroke="currentColor" stroke-width=".9" fill="none"><rect x="1" y="10" width="5" height="4" rx=".5"/><rect x="7" y="10" width="5" height="4" rx=".5"/><rect x="13" y="10" width="5" height="4" rx=".5"/><rect x="1" y="5" width="5" height="4" rx=".5"/><rect x="7" y="5" width="5" height="4" rx=".5"/><rect x="7" y="0" width="5" height="4" rx=".5"/></g><path fill="currentColor" d="M19 13c1.5-.3 3-.3 4 .5 0 0-.8 2.5-3.5 3.5-1 3-3.5 5-7.5 5C7 22 3 18.5 3 15h16z"/>'],
  cpu: [24, 24, '<path fill="currentColor" d="M15 9H9v6h6V9zm-2 4h-2v-2h2v2zm8-2V9h-2V7c0-1.1-.9-2-2-2h-2V3h-2v2h-2V3H9v2H7c-1.1 0-2 .9-2 2v2H3v2h2v2H3v2h2v2c0 1.1.9 2 2 2h2v2h2v-2h2v2h2v-2h2c1.1 0 2-.9 2-2v-2h2v-2h-2v-2h2zm-4 6H7V7h10v10z"/>'],
  ram: [24, 24, '<g fill="currentColor"><rect x="2" y="6" width="20" height="12" rx="2"/><rect x="4" y="18" width="2" height="2" rx=".5"/><rect x="8" y="18" width="2" height="2" rx=".5"/><rect x="14" y="18" width="2" height="2" rx=".5"/><rect x="18" y="18" width="2" height="2" rx=".5"/></g><g class="cut"><rect x="5" y="9" width="2" height="4" rx=".5"/><rect x="9" y="9" width="2" height="4" rx=".5"/><rect x="13" y="9" width="2" height="4" rx=".5"/><rect x="17" y="9" width="2" height="4" rx=".5"/></g>'],
  storage: [24, 24, '<g fill="currentColor"><ellipse cx="12" cy="5.5" rx="8" ry="3.5"/><path d="M4 5.5v5c0 1.93 3.58 3.5 8 3.5s8-1.57 8-3.5v-5c0 1.93-3.58 3.5-8 3.5S4 7.43 4 5.5z"/><path d="M4 10.5v5c0 1.93 3.58 3.5 8 3.5s8-1.57 8-3.5v-5c0 1.93-3.58 3.5-8 3.5s-8-1.57-8-3.5z"/></g>'],
  shieldcheck: [24, 24, '<path fill="currentColor" d="M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm-2 16l-4-4 1.41-1.41L10 14.17l6.59-6.59L18 9l-8 8z"/>'],
  helm: [24, 24, '<path fill="currentColor" d="M19.43 12.98c.04-.32.07-.64.07-.98s-.03-.66-.07-.98l2.11-1.65c.19-.15.24-.42.12-.64l-2-3.46c-.12-.22-.39-.3-.61-.22l-2.49 1c-.52-.4-1.08-.73-1.69-.98l-.38-2.65C14.46 2.18 14.25 2 14 2h-4c-.25 0-.46.18-.49.42l-.38 2.65c-.61.25-1.17.59-1.69.98l-2.49-1c-.23-.09-.49 0-.61.22l-2 3.46c-.13.22-.07.49.12.64l2.11 1.65c-.04.32-.07.65-.07.98s.03.66.07.98l-2.11 1.65c-.19.15-.24.42-.12.64l2 3.46c.12.22.39.3.61.22l2.49-1c.52.4 1.08.73 1.69.98l.38 2.65c.03.24.24.42.49.42h4c.25 0 .46-.18.49-.42l.38-2.65c.61-.25 1.17-.59 1.69-.98l2.49 1c.23.09.49 0 .61-.22l2-3.46c.12-.22.07-.49-.12-.64l-2.11-1.65zM12 15.5c-1.93 0-3.5-1.57-3.5-3.5s1.57-3.5 3.5-3.5 3.5 1.57 3.5 3.5-1.57 3.5-3.5 3.5z"/>'],
  volume: [24, 24, '<path fill="currentColor" d="M2 20h20v-4H2v4zm2-3h2v2H4v-2zM2 4v4h20V4H2zm4 3H4V5h2v2zm-4 7h20v-4H2v4zm2-3h2v2H4v-2z"/>'],
  xerror: [24, 24, '<path fill="currentColor" d="M12 2C6.47 2 2 6.47 2 12s4.47 10 10 10 10-4.47 10-10S17.53 2 12 2zm5 13.59L15.59 17 12 13.41 8.41 17 7 15.59 10.59 12 7 8.41 8.41 7 12 10.59 15.59 7 17 8.41 13.41 12 17 15.59z"/>'],
  lock: [24, 24, '<path fill="currentColor" d="M18 8h-1V6c0-2.76-2.24-5-5-5S7 3.24 7 6v2H6c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V10c0-1.1-.9-2-2-2zm-6 9c-1.1 0-2-.9-2-2s.9-2 2-2 2 .9 2 2-.9 2-2 2zm3.1-9H8.9V6c0-1.71 1.39-3.1 3.1-3.1 1.71 0 3.1 1.39 3.1 3.1v2z"/>'],
  bolt: [24, 24, '<path fill="currentColor" d="M7 2v11h3v9l7-12h-4l4-8z"/>'],
  signal: [24, 24, '<g fill="currentColor"><rect x="1" y="16" width="4" height="6" rx="1"/><rect x="7" y="11" width="4" height="11" rx="1"/><rect x="13" y="6" width="4" height="16" rx="1"/><rect x="19" y="1" width="4" height="21" rx="1"/></g>'],
};

// Map metric names to their icons
const METRIC_ICON_MAP = {
  talos_version: "flatcar",
  kubernetes_version: "kubernetes",
  flux_version: "flux",
  renovate: "renovatebot",
  cluster_age_days: "calendar",
  cluster_uptime_days: "uptime",
  cluster_node_count: "node",
  cluster_alert_count: "alert",
  cluster_pod_count: "pod",
  container_count: "container",
  cluster_cpu_usage: "cpu",
  cluster_memory_usage: "ram",
  ceph_storage_used: "storage",
  ceph_health: "shieldcheck",
  helmrelease_count: "helm",
  pvc_count: "volume",
  flux_failing_count: "xerror",
  cert_expiry_days: "lock",
  wan_primary: "bolt",
  wan_cellular1: "signal",
  wan_cellular2: "signal",
};

// Metrics whose value carries a real state — these render a status dot.
// Plain counts and facts (nodes, pods, age, versions) stay neutral.
const METRIC_STATUS = new Set([
  "cluster_alert_count", "ceph_health", "cluster_cpu_usage",
  "cluster_memory_usage", "ceph_storage_used", "cert_expiry_days",
  "flux_failing_count", "renovate", "wan_primary", "wan_cellular1",
  "wan_cellular2",
]);

// --- Tile geometry (compact) ---
const ROW_WIDTH = 832;    // uniform row width so stacked panels align as a grid
const TILE_HEIGHT = 52;
const TILE_GAP = 8;
const TILE_RADIUS = 10;
const PAD_X = 14;
const ICON_SIZE = 13;
const CHIP_SIZE = 24;
const DOT_R = 3.5;
const HALO_R = 7;
const SINGLE_TILE_WIDTH = 202;

// Document stylesheet: GitHub Primer tokens. Color never paints text — the
// value and label stay in ink; state lives in the dot.
//
// Theming: GitHub's camo-proxied <img> SVGs evaluate prefers-color-scheme
// against the *browser/OS*, not the GitHub theme, so auto-theming clashes
// when the two disagree. The README therefore uses <picture> sources with
// explicit ?theme=dark / ?theme=light variants (GitHub resolves those against
// its own theme). No/invalid theme param falls back to auto (media query).
const STYLE_BASE = `
  text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
  .label{font-size:9px;font-weight:600;letter-spacing:.9px}
  .value{font-size:16px;font-weight:600;letter-spacing:-.1px}
  .tile{fill:url(#cbg);stroke:url(#cbd)}
  .halo{fill-opacity:.18}
  .chip-acc{fill-opacity:.13}
  .chip-ok{fill-opacity:.13}
  .chip-warn{fill-opacity:.14}
  .chip-err{fill-opacity:.13}
  .chip-neu{fill-opacity:.12}`;
// Accent (soft indigo) for neutral tiles; softened status hues for stateful
// ones. Text always stays in ink — hue lives in the chip, dot, and glow.
const STYLE_DARK = `
  .cut{fill:#171b21}
  .label{fill:#8b95a5}
  .value{fill:#f1f5f9}
  .chip-acc,.halo-acc,.dot-acc{fill:#7c9cf5}
  .chip-ok,.halo-ok,.dot-ok{fill:#4ade80}
  .chip-warn,.halo-warn,.dot-warn{fill:#fbbf24}
  .chip-err,.halo-err,.dot-err{fill:#f87171}
  .chip-neu,.halo-neu,.dot-neu{fill:#8b95a5}
  .ic-acc{color:#a5bcff}
  .ic-ok{color:#6ee7a0}
  .ic-warn{color:#fcd34d}
  .ic-err{color:#fca5a5}
  .ic-neu{color:#9aa4b2}
  .s0{stop-color:#1c2129}.s1{stop-color:#141920}
  .b0{stop-color:#ffffff;stop-opacity:.14}.b1{stop-color:#ffffff;stop-opacity:.05}`;
const STYLE_LIGHT = `
  .cut{fill:#ffffff}
  .label{fill:#667085}
  .value{fill:#101828}
  .chip-acc,.halo-acc,.dot-acc{fill:#4f6ef7}
  .chip-ok,.halo-ok,.dot-ok{fill:#039855}
  .chip-warn,.halo-warn,.dot-warn{fill:#dc6803}
  .chip-err,.halo-err,.dot-err{fill:#d92d20}
  .chip-neu,.halo-neu,.dot-neu{fill:#667085}
  .ic-acc{color:#4f6ef7}
  .ic-ok{color:#039855}
  .ic-warn{color:#c05621}
  .ic-err{color:#d92d20}
  .ic-neu{color:#667085}
  .s0{stop-color:#ffffff}.s1{stop-color:#f7f9fc}
  .b0{stop-color:#101828;stop-opacity:.14}.b1{stop-color:#101828;stop-opacity:.06}`;

function tileStyle(theme) {
  if (theme === "dark") return STYLE_BASE + STYLE_DARK;
  if (theme === "light") return STYLE_BASE + STYLE_LIGHT;
  return `${STYLE_BASE}${STYLE_DARK}
  @media (prefers-color-scheme: light){${STYLE_LIGHT}
  }`;
}

function escapeXml(s) {
  return (s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// Semantic hue slot for a tile: neutral tiles wear the accent; status tiles
// wear their state's hue.
function tileKind(tile) {
  return tile.status ? statusKind(tile.color) : "acc";
}

// Tinted icon chip, right-center: rounded square wash in the tile's hue with
// the icon inked in a lighter step of the same hue.
function iconMarkup(name, tileW, kind) {
  const icon = ICONS[name];
  if (!icon) return "";
  const [vw, vh, inner] = icon;
  const chipX = tileW - PAD_X - CHIP_SIZE;
  const chipY = (TILE_HEIGHT - CHIP_SIZE) / 2;
  const scale = Math.min(ICON_SIZE / vw, ICON_SIZE / vh);
  const w = vw * scale;
  const h = vh * scale;
  const ix = chipX + (CHIP_SIZE - w) / 2;
  const iy = chipY + (CHIP_SIZE - h) / 2;
  return `<rect class="chip-${kind}" x="${round2(chipX)}" y="${round2(chipY)}" width="${CHIP_SIZE}" height="${CHIP_SIZE}" rx="7"/>` +
    `<g class="ic-${kind}" transform="translate(${round2(ix)},${round2(iy)}) scale(${round4(scale)})">${inner}</g>`;
}

function round2(n) { return Math.round(n * 100) / 100; }
function round4(n) { return Math.round(n * 10000) / 10000; }

// One stat tile: tracked-uppercase micro-label over a large value in ink,
// glowing status dot for stateful metrics, tinted icon chip right-center.
function makeTileInner(tile, tileW) {
  const label = escapeXml((tile.label || "").toUpperCase());
  const message = escapeXml(tile.message);
  const kind = tileKind(tile);
  const dotCy = 34.5;
  const dot = tile.status
    ? `<circle class="halo halo-${kind}" cx="${PAD_X + DOT_R}" cy="${dotCy}" r="${HALO_R}"/>` +
      `<circle class="dot-${kind}" cx="${PAD_X + DOT_R}" cy="${dotCy}" r="${DOT_R}"/>`
    : "";
  const valueX = tile.status ? PAD_X + DOT_R + HALO_R + 6 : PAD_X;
  // Long values (rare error strings) drop a size so they never clip.
  const valueSize = (tile.message || "").length > 12 ? 12 : null;
  const sizeAttr = valueSize ? ` style="font-size:${valueSize}px"` : "";
  return `<rect class="tile" x=".5" y=".5" width="${tileW - 1}" height="${TILE_HEIGHT - 1}" rx="${TILE_RADIUS}"/>` +
    `${iconMarkup(tile.logo, tileW, kind)}` +
    `<text class="label" x="${PAD_X}" y="20">${label}</text>` +
    `${dot}<text class="value" x="${valueX}" y="40"${sizeAttr}>${message}</text>`;
}

// Shared defs: card-surface gradient and hairline border gradient (brighter
// at the top edge — the "glass" highlight). Stop colors are set via CSS so
// the same defs serve both themes.
const TILE_DEFS = `<defs>` +
  `<linearGradient id="cbg" x1="0" y1="0" x2="0" y2="1"><stop class="s0" offset="0"/><stop class="s1" offset="1"/></linearGradient>` +
  `<linearGradient id="cbd" x1="0" y1="0" x2="0" y2="1"><stop class="b0" offset="0"/><stop class="b1" offset="1"/></linearGradient>` +
  `</defs>`;

function tileRowSvg(tiles, totalWidth, theme) {
  const n = tiles.length;
  const tileW = round2((totalWidth - (n - 1) * TILE_GAP) / n);
  const aria = tiles.map((t) => `${t.label}: ${t.message}`).join(", ");
  let inner = "";
  let x = 0;
  for (const t of tiles) {
    inner += `<g transform="translate(${round2(x)},0)">${makeTileInner(t, tileW)}</g>`;
    x += tileW + TILE_GAP;
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${totalWidth}" height="${TILE_HEIGHT}" viewBox="0 0 ${totalWidth} ${TILE_HEIGHT}" role="img" aria-label="${escapeXml(aria)}"><title>${escapeXml(aria)}</title><style>${tileStyle(theme)}</style>${TILE_DEFS}${inner}</svg>`;
}

// Single-tile SVG (standalone metric endpoints and error responses).
function makeTileSvg(tile, theme) {
  return tileRowSvg([tile], SINGLE_TILE_WIDTH, theme);
}

function requestedTheme(url) {
  const t = url.searchParams.get("theme");
  return t === "dark" || t === "light" ? t : null;
}

function panelMessage(message) {
  const normalized = (message || "").toLowerCase();
  switch (normalized) {
    case "timeout":
    case "unavailable":
    case "auth failed":
    case "api error":
    case "no token":
    case "no runs":
      return "ERR";
    case "cancelled":
      return "Cancelled";
    default:
      return message || "";
  }
}

// --- Allowed metrics ---
const ALLOWED_METRICS = new Set([
  "talos_version", "kubernetes_version", "flux_version",
  "cluster_node_count", "cluster_pod_count", "cluster_cpu_usage",
  "cluster_memory_usage", "cluster_age_days", "cluster_uptime_days",
  "cluster_alert_count", "ceph_storage_used", "ceph_health",
  "cert_expiry_days", "flux_failing_count", "helmrelease_count",
  "pvc_count", "container_count", "wan_primary", "wan_cellular1", "wan_cellular2",
  "network_status", "renovate", "stack_panel", "health_panel",
  "usage_panel", "gitops_panel",
]);

// --- Response helpers ---
const SECURITY_HEADERS = {
  "Access-Control-Allow-Origin": "*",
  "Cache-Control": CLIENT_CACHE_CONTROL,
  "X-Robots-Tag": "noindex",
  "Referrer-Policy": "no-referrer",
  "X-Content-Type-Options": "nosniff",
  "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
};

function svgResponse(svg, status, cacheControl = status === 200 ? CLIENT_CACHE_CONTROL : ERROR_CACHE_CONTROL) {
  return new Response(svg, {
    status,
    headers: {
      ...SECURITY_HEADERS,
      "Content-Type": "image/svg+xml",
      "Cache-Control": cacheControl,
    },
  });
}

function jsonResponse(data, status, cacheControl = status === 200 ? CLIENT_CACHE_CONTROL : ERROR_CACHE_CONTROL) {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      ...SECURITY_HEADERS,
      "Content-Type": "application/json",
      "Cache-Control": cacheControl,
    },
  });
}

function errorTileResponse(label, message, status) {
  return svgResponse(makeTileSvg({ label, message, color: "critical", status: true }), status);
}

// --- Origin fetch helpers ---
async function fetchKromgoMetric(metric, env) {
  const resp = await fetch(`https://kromgo.${env.SECRET_DOMAIN}/${metric}`, {
    headers: { "CF-Access-Client-Id": env.CF_CLIENT_ID, "CF-Access-Client-Secret": env.CF_CLIENT_SECRET },
    signal: AbortSignal.timeout(10000),
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

// --- Uptime Kuma status page API ---
async function fetchKumaStatus(env) {
  if (!env.KUMA_DOMAIN) return null;
  try {
    const resp = await fetch(`https://status.${env.KUMA_DOMAIN}/api/status-page/heartbeat/internet`, {
      signal: AbortSignal.timeout(8000),
    });
    if (!resp.ok) return null;
    const data = await resp.json();
    // Monitor 2 = Frontier Fiber — get latest heartbeat
    const beats = data.heartbeatList?.["2"];
    if (!beats || beats.length === 0) return null;
    const latest = beats[beats.length - 1];
    // status: 1 = UP, 0 = DOWN, 2 = PENDING
    return {
      up: latest.status === 1,
      ping: latest.ping,
      time: latest.time,
    };
  } catch {
    return null;
  }
}

function metricStateCacheRequest(key) {
  return new Request(`https://metric-cache.local/${key}`, { method: "GET" });
}

async function getCachedMetricState(key) {
  const cached = await caches.default.match(metricStateCacheRequest(key));
  if (!cached) return null;
  const cachedAt = parseInt(cached.headers.get("x-fetch-time") || "0");
  const cachedAge = cachedAt ? (Date.now() - cachedAt) / 1000 : Number.POSITIVE_INFINITY;
  if (cachedAge >= CACHE_STALE_S) return null;
  try {
    return await cached.json();
  } catch {
    return null;
  }
}

async function putMetricStateCache(key, value) {
  const headers = new Headers({
    "Content-Type": "application/json",
    "Cache-Control": "public, s-maxage=900",
    "x-fetch-time": Date.now().toString(),
  });
  await caches.default.put(metricStateCacheRequest(key), new Response(JSON.stringify(value), { status: 200, headers }));
}

function metricState(item, message, color) {
  return {
    label: item.label || item.metric,
    message,
    color,
    logo: item.logo || METRIC_ICON_MAP[item.metric] || null,
    status: !!item.status,
  };
}

const PANEL_DEFINITIONS = {
  stack_panel: {
    items: [
      { metric: "talos_version", label: "Flatcar" },
      { metric: "kubernetes_version", label: "Kubernetes" },
      { metric: "flux_version", label: "Flux" },
      { metric: "renovate", label: "Renovate", status: true },
    ],
  },
  health_panel: {
    items: [
      { metric: "cluster_node_count", label: "Nodes" },
      { metric: "cluster_age_days", label: "Age" },
      { metric: "cluster_uptime_days", label: "Uptime" },
      { metric: "cluster_alert_count", label: "Alerts", status: true },
      { metric: "ceph_health", label: "Ceph", status: true },
    ],
  },
  usage_panel: {
    items: [
      { metric: "cluster_pod_count", label: "Pods" },
      { metric: "container_count", label: "Containers" },
      { metric: "cluster_cpu_usage", label: "CPU", status: true },
      { metric: "cluster_memory_usage", label: "Memory", status: true },
      { metric: "ceph_storage_used", label: "Storage", status: true },
    ],
  },
  gitops_panel: {
    items: [
      { metric: "helmrelease_count", label: "HelmReleases" },
      { metric: "pvc_count", label: "PVCs" },
      { metric: "flux_failing_count", label: "Flux errors", status: true },
      { metric: "cert_expiry_days", label: "Cert expiry", status: true },
    ],
  },
};

function capitalize(s) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

async function fetchRenovateRun(env) {
  const ghResp = await fetch(
    "https://api.github.com/repos/GizmoTickler/home-ops/actions/workflows/renovate.yaml/runs?branch=main&per_page=1",
    {
      headers: {
        Authorization: `Bearer ${env.GIT_PAT}`,
        Accept: "application/vnd.github+json",
        "User-Agent": "kromgo-proxy-worker",
      },
      signal: AbortSignal.timeout(10000),
    },
  );
  if (!ghResp.ok) return { ok: false };
  const data = await ghResp.json();
  return { ok: true, run: data.workflow_runs?.[0] };
}

function renovateRunState(run) {
  let message = "Running";
  let color = "yellow";
  if (run.status === "completed") {
    switch (run.conclusion) {
      case "success":   message = "Passing"; color = "brightgreen"; break;
      case "failure":   message = "Failing"; color = "red"; break;
      case "cancelled": message = "Cancelled"; color = "orange"; break;
      case "skipped":   message = "Skipped"; color = "lightgrey"; break;
      default:          message = capitalize(run.conclusion || "unknown"); color = "lightgrey";
    }
  }
  return { message, color };
}

async function buildRenovateBadgeData(item, env) {
  const cacheKey = `panel/renovate`;

  if (!env.GIT_PAT) {
    return metricState(item, "no token", "critical");
  }
  try {
    const result = await fetchRenovateRun(env);
    if (!result.ok) {
      return (await getCachedMetricState(cacheKey)) || metricState(item, "api error", "critical");
    }
    if (!result.run) {
      const state = metricState(item, "no runs", "lightgrey");
      await putMetricStateCache(cacheKey, state);
      return state;
    }
    const { message, color } = renovateRunState(result.run);
    const state = metricState(item, message, item.color || color);
    await putMetricStateCache(cacheKey, state);
    return state;
  } catch {
    return (await getCachedMetricState(cacheKey)) || metricState(item, "timeout", "lightgrey");
  }
}

async function buildMetricBadgeData(item, env) {
  const cacheKey = `panel/${item.metric}`;

  try {
    const result = await fetchKromgoMetric(item.metric, env);
    if (!result.ok) {
      if (result.error === "auth") {
        return (await getCachedMetricState(cacheKey)) || metricState(item, "auth failed", "critical");
      }
      return (await getCachedMetricState(cacheKey)) || metricState(item, "unavailable", "lightgrey");
    }

    const msg = (result.data.message || "").toLowerCase();
    if (msg.includes("no data") || msg.includes("error")) {
      return (await getCachedMetricState(cacheKey)) || metricState(item, result.data.message, "lightgrey");
    }

    const state = metricState(item, result.data.message, item.color || result.data.color);
    await putMetricStateCache(cacheKey, state);
    return state;
  } catch {
    return (await getCachedMetricState(cacheKey)) || metricState(item, "timeout", "lightgrey");
  }
}

async function renderMetricPanel(metricName, env, theme) {
  const panel = PANEL_DEFINITIONS[metricName];
  const states = await Promise.all(panel.items.map((item) => {
    if (item.metric === "renovate") return buildRenovateBadgeData(item, env);
    return buildMetricBadgeData(item, env);
  }));
  const tiles = states.map((s) => ({ ...s, message: panelMessage(s.message) }));
  return svgResponse(tileRowSvg(tiles, ROW_WIDTH, theme), 200);
}

// --- Metric rendering (produces final Response) ---
async function renderMetric(metricName, url, env) {
  const theme = requestedTheme(url);

  if (PANEL_DEFINITIONS[metricName]) {
    return renderMetricPanel(metricName, env, theme);
  }

  // Network status panel — Fiber from Uptime Kuma (external), cells from kromgo (internal)
  if (metricName === "network_status") {
    const [kuma, cell1, cell2] = await Promise.all([
      fetchKumaStatus(env),
      fetchWanMetric("wan_cellular1", env),
      fetchWanMetric("wan_cellular2", env),
    ]);
    let fiberTile;
    if (!kuma) {
      fiberTile = { label: "Fiber", message: "ERR", color: "grey", logo: "bolt", status: true };
    } else if (!kuma.up) {
      fiberTile = { label: "Fiber", message: "DOWN", color: "red", logo: "bolt", status: true };
    } else {
      fiberTile = { label: "Fiber", message: `UP · ${Math.round(kuma.ping)}ms`, color: "brightgreen", logo: "bolt", status: true };
    }
    return svgResponse(tileRowSvg([
      fiberTile,
      { label: "Cell 1", message: cell1.message, color: cell1.color, logo: "signal", status: true },
      { label: "Cell 2", message: cell2.message, color: cell2.color, logo: "signal", status: true },
    ], ROW_WIDTH, theme), 200);
  }

  // Renovate workflow status — GitHub Actions API
  if (metricName === "renovate") {
    if (!env.GIT_PAT) {
      return errorTileResponse("Renovate", "no token", 200);
    }
    try {
      const result = await fetchRenovateRun(env);
      if (!result.ok) {
        return errorTileResponse("Renovate", "api error", 200);
      }
      if (!result.run) {
        return svgResponse(makeTileSvg({ label: "Renovate", message: "no runs", color: "lightgrey", logo: "renovatebot", status: true }, theme), 200);
      }
      const { message, color } = renovateRunState(result.run);
      const label = url.searchParams.get("label") || "Renovate";
      return svgResponse(makeTileSvg({
        label,
        message,
        color: url.searchParams.get("color") || color,
        logo: "renovatebot",
        status: true,
      }, theme), 200);
    } catch {
      return svgResponse(makeTileSvg({ label: "Renovate", message: "timeout", color: "lightgrey", logo: "renovatebot", status: true }, theme), 200);
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
          : errorTileResponse("Kromgo", "auth failed", 200);
      }
      return wantJson
        ? jsonResponse({ schemaVersion: 1, label: "error", message: "unavailable", color: "lightgrey" }, 503)
        : errorTileResponse("Error", "unavailable", 200);
    }
    // Treat kromgo "no data" responses as errors for caching, but still return
    // a renderable 200 tile so README image consumers don't break.
    const msg = (result.data.message || "").toLowerCase();
    if (msg.includes("no data") || msg.includes("error")) {
      return svgResponse(makeTileSvg({
        label: result.data.label || metricName,
        message: result.data.message,
        color: "lightgrey",
        logo: METRIC_ICON_MAP[metricName],
        status: true,
      }, theme), 200);
    }
    if (wantJson) return jsonResponse(result.data, 200);
    return svgResponse(makeTileSvg({
      label: url.searchParams.get("label") || result.data.label || metricName,
      message: result.data.message,
      color: url.searchParams.get("color") || result.data.color,
      logo: url.searchParams.get("logo") || METRIC_ICON_MAP[metricName] || null,
      status: METRIC_STATUS.has(metricName),
    }, theme), 200);
  } catch {
    return wantJson
      ? jsonResponse({ schemaVersion: 1, label: "error", message: "timeout", color: "lightgrey" }, 503)
      : svgResponse(makeTileSvg({ label: "Error", message: "timeout", color: "lightgrey", status: true }, theme), 200);
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
    if (request.method !== "GET" && request.method !== "HEAD") return new Response("Method not allowed", { status: 405 });

    const url = new URL(request.url);
    const metricName = url.pathname.substring(1);

    // Static assets — long-lived cache, no edge cache needed
    if (metricName === "logo") return serveLogo();

    if (!env.CF_CLIENT_ID || !env.CF_CLIENT_SECRET || !env.SECRET_DOMAIN) {
      return errorTileResponse("Error", "misconfigured", 500);
    }

    if (!metricName || !ALLOWED_METRICS.has(metricName)) {
      return errorTileResponse("Error", "not found", 404);
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
