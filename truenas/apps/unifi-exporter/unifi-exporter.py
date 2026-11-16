#!/usr/bin/env python3
"""Prometheus exporter for UniFi Network controller wireless/RF, client, and roaming telemetry."""

from __future__ import annotations

import argparse
from collections import defaultdict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import re
import ssl
import sys
import threading
import time
import urllib.error
import urllib.request
from typing import Any

DOMAIN_SUFFIX_RE = re.compile(r"\.[a-z0-9-]+(?:\.[a-z0-9-]+)+", re.IGNORECASE)
RADIO_MAP = {
    "2g": "ng",
    "2.4g": "ng",
    "ng": "ng",
    "5g": "na",
    "na": "na",
    "6g": "6e",
    "6e": "6e",
    "all": "all",
}


def strip_domain(name: str) -> str:
    """Remove multi-label DNS suffixes so client labels remain concise and private."""
    cleaned = DOMAIN_SUFFIX_RE.sub("", name or "").strip()
    return cleaned


def esc_label(value: Any) -> str:
    """Escape a Prometheus label value per the text exposition format."""
    s = "" if value is None else str(value)
    return s.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def norm_radio(value: Any, channel: Any = None) -> str:
    """Normalize UniFi band/radio identifiers to ng, na, 6e, or all."""
    if value is not None:
        key = str(value).strip().lower()
        if key in RADIO_MAP:
            return RADIO_MAP[key]
    if isinstance(channel, (int, float)):
        if 1 <= int(channel) <= 14:
            return "ng"
        if 32 <= int(channel) <= 177:
            return "na"
        if int(channel) >= 1 and int(channel) not in range(15, 32):
            return "6e"
    return "unknown"


def num(value: Any) -> float | None:
    """Convert int/float or numeric string to float, returning None if invalid or NaN."""
    if value is None or isinstance(value, bool):
        return None
    try:
        out = float(value)
    except (TypeError, ValueError):
        return None
    if out != out:  # NaN
        return None
    return out


class MetricBuilder:
    """Collects Prometheus metrics grouped cleanly by HELP/TYPE header."""

    def __init__(self) -> None:
        self._meta: dict[str, tuple[str, str]] = {}
        self._series: dict[str, dict[tuple[tuple[str, str], ...], float | int]] = {}

    def add(self, name: str, mtype: str, help_text: str, labels: dict[str, Any], value: float | int | None) -> None:
        if value is None or isinstance(value, bool):
            return
        if name not in self._meta:
            self._meta[name] = (mtype, help_text)
            self._series[name] = {}
        key = tuple(sorted((str(k), "" if v is None else str(v)) for k, v in labels.items()))
        self._series[name][key] = value

    def inc_sum(self, name: str, mtype: str, help_text: str, labels: dict[str, Any], delta: float | int) -> None:
        if name not in self._meta:
            self._meta[name] = (mtype, help_text)
            self._series[name] = {}
        key = tuple(sorted((str(k), "" if v is None else str(v)) for k, v in labels.items()))
        self._series[name][key] = self._series[name].get(key, 0) + delta

    def render(self) -> str:
        lines: list[str] = []
        for name, (mtype, help_text) in self._meta.items():
            samples = self._series[name]
            if not samples:
                continue
            lines.append(f"# HELP {name} {help_text}")
            lines.append(f"# TYPE {name} {mtype}")
            for key, val in sorted(samples.items()):
                if key:
                    lbl = ",".join(f'{k}="{esc_label(v)}"' for k, v in key)
                    lines.append(f"{name}{{{lbl}}} {val}")
                else:
                    lines.append(f"{name} {val}")
        return "\n".join(lines) + "\n"


class UniFiClient:
    """Minimal stdlib UniFi Network REST API client."""

    def __init__(self, base_url: str, api_key: str, site: str, verify_tls: bool, timeout: float = 10.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.site = site
        self.timeout = timeout
        self.ssl_ctx = ssl.create_default_context() if verify_tls else ssl._create_unverified_context()

    def request(self, path: str, payload: dict[str, Any] | None = None) -> Any:
        url = f"{self.base_url}{path}"
        data = None if payload is None else json.dumps(payload).encode("utf-8")
        headers = {"X-API-KEY": self.api_key, "Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers, method="POST" if data is not None else "GET")
        with urllib.request.urlopen(req, context=self.ssl_ctx, timeout=self.timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))


class UniFiCollector:
    """Scrapes UniFi endpoints, maintains thread-safe cache and monotonic error counters."""

    def __init__(self, client: UniFiClient, cache_ttl: float) -> None:
        self.client = client
        self.cache_ttl = cache_ttl
        self._lock = threading.Lock()
        self._cached_payload = ""
        self._cached_at = 0.0
        self._last_success = 0.0
        self._endpoint_errors: dict[str, int] = defaultdict(int)

    def _safe_fetch(self, ep_name: str, path: str, payload: dict[str, Any] | None = None) -> tuple[Any, bool]:
        try:
            return self.client.request(path, payload), True
        except Exception as exc:  # noqa: BLE001 - exporter must stay up on transient controller/API faults
            self._endpoint_errors[ep_name] += 1
            print(f"WARN endpoint={ep_name} err={exc}", file=sys.stderr, flush=True)
            return None, False

    def collect_text(self) -> str:
        with self._lock:
            now = time.monotonic()
            if self._cached_payload and (now - self._cached_at) < self.cache_ttl:
                return self._cached_payload
            body = self._scrape_once()
            self._cached_payload = body
            self._cached_at = time.monotonic()
            return body

    def _scrape_once(self) -> str:
        t0 = time.monotonic()
        mb = MetricBuilder()
        site = self.client.site

        if not self.client.api_key:
            self._endpoint_errors["config"] += 1
            self._emit_collector_stats(mb, up=0, elapsed=time.monotonic() - t0)
            return mb.render()

        dev_raw, ok_dev = self._safe_fetch("stat_device", f"/api/s/{site}/stat/device")
        wlan_raw, ok_wlan = self._safe_fetch("rest_wlanconf", f"/api/s/{site}/rest/wlanconf")
        clients_raw, ok_clients = self._safe_fetch("clients_active", f"/v2/api/site/{site}/clients/active")

        core_ok = ok_dev and ok_wlan and ok_clients
        ap_by_mac: dict[str, str] = {}
        uap_names: list[str] = []
        own_macs: set[str] = set()

        if ok_dev and isinstance(dev_raw, dict):
            devices = dev_raw.get("data") or []
            self._emit_devices(mb, devices, ap_by_mac, uap_names, own_macs)

        if ok_wlan and isinstance(wlan_raw, dict):
            self._emit_wlans(mb, wlan_raw.get("data") or [])

        if ok_clients and isinstance(clients_raw, list):
            self._emit_clients(mb, clients_raw, ap_by_mac)

        self._scrape_system_log(mb, site, ap_by_mac, uap_names)
        self._scrape_connectivity(mb, site)
        self._scrape_rogues(mb, site, ap_by_mac, own_macs)

        if core_ok:
            self._last_success = time.time()
        self._emit_collector_stats(mb, up=1 if core_ok else 0, elapsed=time.monotonic() - t0)
        return mb.render()

    def _emit_collector_stats(self, mb: MetricBuilder, up: int, elapsed: float) -> None:
        mb.add("unifi_collector_up", "gauge", "1 if core UniFi API scrape succeeded, 0 otherwise.", {}, up)
        mb.add("unifi_collector_scrape_duration_seconds", "gauge", "Duration of the UniFi API scrape.", {}, round(elapsed, 4))
        mb.add("unifi_collector_last_success_timestamp_seconds", "gauge", "Unix timestamp of last successful core UniFi scrape.", {}, int(self._last_success))
        for ep, count in sorted(self._endpoint_errors.items()):
            mb.add("unifi_collector_endpoint_errors_total", "counter", "Cumulative endpoint fetch errors per API endpoint.", {"endpoint": ep}, count)

    def _emit_devices(
        self,
        mb: MetricBuilder,
        devices: list[dict[str, Any]],
        ap_by_mac: dict[str, str],
        uap_names: list[str],
        own_macs: set[str],
    ) -> None:
        for dev in devices:
            mac = (dev.get("mac") or "").lower()
            name = strip_domain(dev.get("name") or "") or mac
            dtype = str(dev.get("type") or "unknown")
            if mac:
                ap_by_mac[mac] = name
                own_macs.add(mac)

            base_lbl = {"ap": name, "mac": mac, "type": dtype}
            mb.add("unifi_device_info", "gauge", "Static UniFi device metadata.", {**base_lbl, "model": dev.get("model") or "", "version": dev.get("version") or ""}, 1)
            mb.add("unifi_device_state", "gauge", "UniFi device state (1=online).", base_lbl, num(dev.get("state")) or 0)
            mb.add("unifi_device_uptime_seconds", "gauge", "UniFi device uptime in seconds.", base_lbl, num(dev.get("uptime")))
            mb.add("unifi_device_upgradable", "gauge", "1 if firmware upgrade is available.", base_lbl, 1 if dev.get("upgradable") else 0)
            mb.add("unifi_device_clients", "gauge", "Active client count reported on the UniFi device.", base_lbl, num(dev.get("num_sta")))

            uplink = dev.get("uplink") or {}
            if isinstance(uplink, dict):
                mb.add("unifi_device_uplink_speed_mbps", "gauge", "Negotiated uplink speed in Mbps.", base_lbl, num(uplink.get("speed")))

            sys_stats = dev.get("system-stats") or {}
            if isinstance(sys_stats, dict):
                mb.add("unifi_device_cpu_utilization_pct", "gauge", "UniFi device CPU utilization percentage.", base_lbl, num(sys_stats.get("cpu")))
                mb.add("unifi_device_memory_utilization_pct", "gauge", "UniFi device memory utilization percentage.", base_lbl, num(sys_stats.get("mem")))

            if dtype != "uap":
                continue

            uap_names.append(name)
            bs_mode = str(dev.get("bandsteering_mode") or "").lower()
            mb.add("unifi_ap_bandsteering_off", "gauge", "1 if bandsteering_mode is off on the AP.", {"ap": name, "mac": mac}, 1 if bs_mode == "off" else 0)

            rt_by_radio: dict[str, dict[str, Any]] = {}
            for rt in dev.get("radio_table") or []:
                rkey = norm_radio(rt.get("radio"))
                rt_by_radio[rkey] = rt
                mb.add(
                    "unifi_radio_min_rssi_enabled",
                    "gauge",
                    "1 if minimum RSSI enforcement is enabled on this radio.",
                    {"ap": name, "mac": mac, "radio": rkey},
                    1 if rt.get("min_rssi_enabled") else 0,
                )

            for rts in dev.get("radio_table_stats") or []:
                rkey = norm_radio(rts.get("radio"), rts.get("channel"))
                rlbl = {"ap": name, "mac": mac, "radio": rkey}
                rt = rt_by_radio.get(rkey, {})
                mb.add("unifi_radio_running", "gauge", "1 if AP radio state is RUN.", rlbl, 1 if rts.get("state") == "RUN" else 0)
                mb.add("unifi_radio_channel", "gauge", "Active channel number for the AP radio.", rlbl, num(rts.get("channel")))
                mb.add("unifi_radio_channel_width_mhz", "gauge", "Active channel width in MHz (ht).", rlbl, num(rts.get("bw") or rt.get("ht")))
                mb.add("unifi_radio_tx_power_dbm", "gauge", "Current transmit power in dBm.", rlbl, num(rts.get("tx_power")))
                mb.add("unifi_radio_utilization_pct", "gauge", "Total channel airtime utilization percentage.", rlbl, num(rts.get("cu_total")))
                mb.add("unifi_radio_self_tx_pct", "gauge", "Self TX airtime percentage.", rlbl, num(rts.get("cu_self_tx")))
                mb.add("unifi_radio_self_rx_pct", "gauge", "Self RX airtime percentage.", rlbl, num(rts.get("cu_self_rx")))
                mb.add("unifi_radio_interference_pct", "gauge", "Non-self / adjacent interference channel utilization percentage.", rlbl, num(rts.get("cu_interf")))
                mb.add("unifi_radio_tx_retries_pct", "gauge", "AP radio TX packet retry percentage.", rlbl, num(rts.get("tx_retries_pct")))
                mb.add("unifi_radio_clients", "gauge", "Associated client count per AP radio.", rlbl, num(rts.get("num_sta")))

            vap_up: dict[tuple[str, str], int] = {}
            for vap in dev.get("vap_table") or []:
                bssid = (vap.get("bssid") or "").lower()
                if bssid:
                    own_macs.add(bssid)
                ssid = vap.get("essid") or ""
                if not ssid:
                    continue
                rkey = norm_radio(vap.get("radio"), vap.get("channel"))
                vlbl = {"ap": name, "mac": mac, "ssid": ssid, "radio": rkey}
                vkey = (ssid, rkey)
                vap_up[vkey] = max(vap_up.get(vkey, 0), 1 if vap.get("state") == "RUN" else 0)
                mb.add("unifi_vap_up", "gauge", "1 if VAP state is RUN on AP+SSID+radio.", vlbl, vap_up[vkey])
                mb.inc_sum("unifi_vap_clients", "gauge", "Clients associated per AP+SSID+radio.", vlbl, int(num(vap.get("num_sta")) or 0))
                mb.inc_sum("unifi_vap_tx_packets_total", "counter", "Total VAP TX packets since AP boot.", vlbl, int(num(vap.get("tx_packets")) or 0))
                mb.inc_sum("unifi_vap_tx_retries_total", "counter", "Total VAP TX retried packets since AP boot.", vlbl, int(num(vap.get("tx_retries")) or 0))

    def _emit_wlans(self, mb: MetricBuilder, wlans: list[dict[str, Any]]) -> None:
        for wlan in wlans:
            ssid = wlan.get("name") or ""
            if not ssid:
                continue
            slbl = {"ssid": ssid}
            mb.add("unifi_wlan_enabled", "gauge", "1 if SSID configuration is enabled.", slbl, 1 if wlan.get("enabled") else 0)
            mb.add("unifi_wlan_l2_isolation", "gauge", "1 if L2 client isolation is enabled on SSID.", slbl, 1 if wlan.get("l2_isolation") else 0)
            mb.add("unifi_wlan_mlo_enabled", "gauge", "1 if Wi-Fi 7 MLO is enabled on SSID.", slbl, 1 if wlan.get("mlo_enabled") else 0)
            pmf = str(wlan.get("pmf_mode") or "unknown").lower()
            mb.add("unifi_wlan_pmf_mode", "gauge", "Configured Protected Management Frames (PMF) mode per SSID.", {"ssid": ssid, "pmf_mode": pmf}, 1)

    def _emit_clients(self, mb: MetricBuilder, clients: list[dict[str, Any]], ap_by_mac: dict[str, str]) -> None:
        seen_macs: set[str] = set()
        group_counts: dict[tuple[str, str, str], int] = defaultdict(int)
        mlo_counts: dict[str, int] = defaultdict(int)

        for c in clients:
            if c.get("is_wired"):
                continue
            mac = (c.get("mac") or "").lower()
            if not mac or mac in seen_macs:
                continue
            seen_macs.add(mac)

            ap_mac = (c.get("ap_mac") or "").lower()
            ap = ap_by_mac.get(ap_mac, ap_mac or "unknown")
            ssid = c.get("essid") or "unknown"
            rkey = norm_radio(c.get("radio"), c.get("channel"))
            raw_name = c.get("display_name") or c.get("hostname") or mac
            cname = strip_domain(raw_name) or mac

            group_counts[(ap, ssid, rkey)] += 1
            if c.get("is_mlo"):
                mlo_counts[ssid] += 1

            clbl = {"client": cname, "mac": mac, "ap": ap, "ssid": ssid, "radio": rkey}
            mb.add("unifi_client_signal_dbm", "gauge", "Wireless client RSSI signal in dBm.", clbl, num(c.get("signal")))
            tx_kbps = num(c.get("tx_rate"))
            rx_kbps = num(c.get("rx_rate"))
            mb.add("unifi_client_tx_rate_mbps", "gauge", "Wireless client TX PHY rate in Mbps.", clbl, round(tx_kbps / 1000.0, 2) if tx_kbps is not None else None)
            mb.add("unifi_client_rx_rate_mbps", "gauge", "Wireless client RX PHY rate in Mbps.", clbl, round(rx_kbps / 1000.0, 2) if rx_kbps is not None else None)
            mb.add("unifi_client_tx_retries_pct", "gauge", "Wireless client TX retries percentage.", clbl, num(c.get("wifi_tx_retries_percentage")))
            mb.add("unifi_client_channel", "gauge", "Channel number client is associated on.", clbl, num(c.get("channel")))
            mb.add("unifi_client_mlo", "gauge", "1 if client is operating with Wi-Fi 7 MLO.", clbl, 1 if c.get("is_mlo") else 0)
            mb.add("unifi_client_fixed_ap", "gauge", "1 if client is locked to a fixed AP.", clbl, 1 if c.get("fixed_ap_enabled") else 0)

        for (ap, ssid, rkey), count in group_counts.items():
            mb.add("unifi_clients_wireless_total", "gauge", "Active wireless clients per AP, SSID, and radio.", {"ap": ap, "ssid": ssid, "radio": rkey}, count)
        for ssid, count in mlo_counts.items():
            mb.add("unifi_clients_mlo_total", "gauge", "Active MLO-capable wireless clients per SSID.", {"ssid": ssid}, count)

    def _scrape_system_log(self, mb: MetricBuilder, site: str, ap_by_mac: dict[str, str], uap_names: list[str]) -> None:
        now_ms = int(time.time() * 1000)
        from_ms = now_ms - 3600_000
        events: list[dict[str, Any]] = []
        ok_any = False

        for page in range(5):
            raw, ok = self._safe_fetch(
                "system_log",
                f"/v2/api/site/{site}/system-log/all",
                {"timestampFrom": from_ms, "timestampTo": now_ms, "pageSize": 200, "pageNumber": page},
            )
            if not ok or not isinstance(raw, dict):
                break
            ok_any = True
            batch = raw.get("data") or []
            events.extend(batch)
            total = int(raw.get("totalCount") or 0)
            if len(batch) < 200 or len(events) >= total:
                break

        if not ok_any:
            return

        total_roams = 0
        total_disc = 0
        roams_by_client: dict[str, int] = defaultdict(int)
        roams_by_path: dict[tuple[str, str, str], int] = defaultdict(int)
        disc_by_ap: dict[str, int] = {ap: 0 for ap in uap_names}
        disc_by_client_ap: dict[tuple[str, str], int] = defaultdict(int)

        def param_name(params: dict[str, Any], key: str, default: str = "unknown") -> str:
            val = params.get(key)
            if isinstance(val, dict):
                raw_n = val.get("name") or val.get("id") or default
                mac_id = str(val.get("id") or "").lower()
                if mac_id in ap_by_mac:
                    return ap_by_mac[mac_id]
                return strip_domain(str(raw_n)) or default
            return default

        for ev in events:
            ekey = ev.get("key") or ""
            params = ev.get("parameters") or {}
            if ekey == "CLIENT_ROAMED_2":
                client = param_name(params, "CLIENT")
                from_ap = param_name(params, "DEVICE_FROM")
                to_ap = param_name(params, "DEVICE_TO")
                total_roams += 1
                roams_by_client[client] += 1
                roams_by_path[(client, from_ap, to_ap)] += 1
            elif ekey == "CLIENT_DISCONNECTED_WIRELESS_2":
                client = param_name(params, "CLIENT")
                ap = param_name(params, "DEVICE")
                total_disc += 1
                disc_by_ap[ap] = disc_by_ap.get(ap, 0) + 1
                disc_by_client_ap[(client, ap)] += 1

        mb.add("unifi_roams_1h_total", "gauge", "Total wireless client roaming events in the past 1 hour.", {}, total_roams)
        for client, cnt in roams_by_client.items():
            mb.add("unifi_roams_client_1h", "gauge", "Client roaming events in the past 1 hour per client.", {"client": client}, cnt)
        for (client, from_ap, to_ap), cnt in roams_by_path.items():
            mb.add("unifi_roams_transition_1h", "gauge", "Client roam transitions in the past 1 hour.", {"client": client, "from_ap": from_ap, "to_ap": to_ap}, cnt)

        mb.add("unifi_disconnects_1h_total", "gauge", "Total wireless client disconnect events in the past 1 hour.", {}, total_disc)
        for ap, cnt in disc_by_ap.items():
            mb.add("unifi_ap_disconnects_1h", "gauge", "Wireless client disconnects in the past 1 hour per AP.", {"ap": ap}, cnt)
        for (client, ap), cnt in disc_by_client_ap.items():
            mb.add("unifi_client_disconnects_1h", "gauge", "Wireless client disconnects in the past 1 hour per client and AP.", {"client": client, "ap": ap}, cnt)

    def _scrape_connectivity(self, mb: MetricBuilder, site: str) -> None:
        agg_raw, ok_agg = self._safe_fetch("aggregated_dashboard", f"/v2/api/site/{site}/aggregated-dashboard?historySeconds=3600")
        entries: list[dict[str, Any]] = []
        if ok_agg and isinstance(agg_raw, dict):
            entries = (agg_raw.get("wifi_connectivity") or {}).get("radio_connectivity") or []

        if not entries:
            conn_raw, ok_conn = self._safe_fetch("wifi_connectivity", f"/v2/api/site/{site}/wifi-connectivity?historySeconds=3600")
            if ok_conn and isinstance(conn_raw, dict) and "attempts" in conn_raw:
                entries = [{"radio_filter": "all", "attempts": conn_raw.get("attempts")}]

        for item in entries:
            radio = norm_radio(item.get("radio_filter") or "all")
            att = item.get("attempts") or {}
            mb.add("unifi_connectivity_success_pct", "gauge", "Wi-Fi connection attempt success percentage over last 1 hour.", {"radio": radio}, num(att.get("success_ratio")))
            mb.add("unifi_connectivity_attempts_1h", "gauge", "Total Wi-Fi connection attempts over last 1 hour.", {"radio": radio}, num(att.get("total_attempts")))
            mb.add("unifi_connectivity_failed_1h", "gauge", "Failed Wi-Fi connection attempts over last 1 hour.", {"radio": radio}, num(att.get("failed_client_connections")))

    def _scrape_rogues(self, mb: MetricBuilder, site: str, ap_by_mac: dict[str, str], own_macs: set[str]) -> None:
        raw, ok = self._safe_fetch("stat_rogueap", f"/api/s/{site}/stat/rogueap")
        if not ok or not isinstance(raw, dict):
            return

        now = time.time()
        seen_bssids: dict[tuple[str, str], set[str]] = defaultdict(set)
        open_max_dbm: dict[str, float] = {ap: -120.0 for ap in ap_by_mac.values()}

        for item in raw.get("data") or []:
            age = num(item.get("age"))
            if age is None and item.get("last_seen"):
                age = max(0.0, now - float(item["last_seen"]))
            if age is not None and age > 3600:
                continue

            bssid = (item.get("bssid") or "").lower()
            if not bssid or bssid in own_macs:
                continue

            ap_mac = (item.get("ap_mac") or "").lower()
            ap = ap_by_mac.get(ap_mac, ap_mac or "unknown")
            rkey = norm_radio(item.get("radio") or item.get("band"), item.get("channel"))
            seen_bssids[(ap, rkey)].add(bssid)

            sec = str(item.get("security") or "").strip().lower()
            sig = num(item.get("signal"))
            if sec == "open" and sig is not None:
                prev = open_max_dbm.get(ap, -120.0)
                if sig > prev:
                    open_max_dbm[ap] = sig

        for (ap, rkey), bssids in seen_bssids.items():
            mb.add("unifi_neighbor_bssids", "gauge", "Distinct non-self neighbor BSSIDs observed in the last 1 hour.", {"ap": ap, "radio": rkey}, len(bssids))
        for ap, sig in open_max_dbm.items():
            mb.add("unifi_local_open_rogue_max_dbm", "gauge", "Strongest recent Open neighbor AP RSSI in dBm (-120 if none).", {"ap": ap}, sig)


def make_handler(collector: UniFiCollector) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802 - required by BaseHTTPRequestHandler
            path = self.path.split("?", 1)[0]
            if path == "/healthz":
                body = b"ok\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return
            if path == "/metrics":
                payload = collector.collect_text().encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
                return
            self.send_response(404)
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - silence per-scrape stdout noise
            return

    return Handler


class ReusableThreadingHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True


def main() -> int:
    parser = argparse.ArgumentParser(description="UniFi Network Prometheus exporter")
    parser.add_argument("--check", action="store_true", help="Validate syntax and exit without starting HTTP server")
    args = parser.parse_args()
    if args.check:
        print("unifi-exporter: syntax and imports OK")
        return 0

    base_url = os.environ.get("UNIFI_URL", "https://192.168.120.6/proxy/network")
    api_key = os.environ.get("UNIFI_API_KEY", "").strip()
    site = os.environ.get("UNIFI_SITE", "default")
    verify_tls = os.environ.get("UNIFI_VERIFY_TLS", "false").lower() in ("1", "true", "yes")
    listen_addr = os.environ.get("LISTEN_ADDR", "0.0.0.0")
    listen_port = int(os.environ.get("LISTEN_PORT", "9130"))
    cache_ttl = float(os.environ.get("CACHE_TTL_SECONDS", "20"))

    client = UniFiClient(base_url=base_url, api_key=api_key, site=site, verify_tls=verify_tls)
    collector = UniFiCollector(client=client, cache_ttl=cache_ttl)
    server = ReusableThreadingHTTPServer((listen_addr, listen_port), make_handler(collector))
    print(f"unifi-exporter listening on {listen_addr}:{listen_port} target={base_url}", flush=True)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
