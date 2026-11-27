#!/usr/bin/env python3
"""Active NTPv4 UDP/123 probe for ZoneCore anycast VIPs -> node_exporter textfile collector."""

from __future__ import annotations

import os
import signal
import socket
import struct
import sys
import tempfile
import threading
import time

DEFAULT_TARGETS = (
    ("10.123.123.123", "zonecore-0"),
    ("10.123.123.124", "zonecore-1"),
)
NTP_CLIENT_PACKET = b"\x23" + b"\x00" * 47  # LI=0, VN=4, Mode=3 (client)


def _escape_label(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def parse_targets(raw: str) -> list[tuple[str, str]]:
    if not raw.strip():
        return list(DEFAULT_TARGETS)
    out: list[tuple[str, str]] = []
    for entry in raw.split(","):
        entry = entry.strip()
        if not entry:
            continue
        if "=" in entry:
            node, ip = entry.split("=", 1)
            out.append((ip.strip(), node.strip()))
        else:
            out.append((entry, entry))
    return out or list(DEFAULT_TARGETS)


def probe_ntp_target(ip: str, timeout: float = 2.0) -> tuple[bool, int, float]:
    """Send an RFC 5905 NTPv4 Mode-3 request and validate a Mode-4 synced reply."""
    start = time.monotonic()
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        sock.sendto(NTP_CLIENT_PACKET, (ip, 123))
        data, _ = sock.recvfrom(128)
        rtt = round(time.monotonic() - start, 6)
        if len(data) < 48:
            return False, 0, rtt
        b0, stratum = struct.unpack("!BB", data[:2])
        leap = (b0 >> 6) & 0x03
        mode = b0 & 0x07
        ok = mode == 4 and leap != 3 and 1 <= stratum <= 15
        return ok, stratum, rtt
    except OSError:
        rtt = round(time.monotonic() - start, 6)
        return False, 0, rtt
    finally:
        sock.close()


def collect_metrics_text(
    targets: list[tuple[str, str]],
    *,
    timeout: float = 2.0,
    now: float | None = None,
) -> str:
    lines: list[str] = [
        "# HELP zonecore_ntp_probe_success 1 if ZoneCore NTPv4 UDP/123 probe returned a synchronized Mode-4 reply, 0 otherwise.",
        "# TYPE zonecore_ntp_probe_success gauge",
    ]
    stratum_lines = [
        "# HELP zonecore_ntp_probe_stratum Stratum reported by ZoneCore NTPv4 reply (0 on failure).",
        "# TYPE zonecore_ntp_probe_stratum gauge",
    ]
    rtt_lines = [
        "# HELP zonecore_ntp_probe_rtt_seconds Round-trip duration of the NTPv4 UDP/123 probe in seconds.",
        "# TYPE zonecore_ntp_probe_rtt_seconds gauge",
    ]
    for ip, node in targets:
        ok, stratum, rtt = probe_ntp_target(ip, timeout=timeout)
        lbl = f'target="{_escape_label(ip)}",node="{_escape_label(node)}"'
        lines.append(f"zonecore_ntp_probe_success{{{lbl}}} {1 if ok else 0}")
        stratum_lines.append(f"zonecore_ntp_probe_stratum{{{lbl}}} {stratum}")
        rtt_lines.append(f"zonecore_ntp_probe_rtt_seconds{{{lbl}}} {rtt}")
    ts = round(time.time() if now is None else now, 3)
    lines.extend(stratum_lines)
    lines.extend(rtt_lines)
    lines.extend(
        [
            "# HELP zonecore_ntp_probe_last_run_timestamp_seconds Unix timestamp of the last ZoneCore NTPv4 probe cycle.",
            "# TYPE zonecore_ntp_probe_last_run_timestamp_seconds gauge",
            f"zonecore_ntp_probe_last_run_timestamp_seconds {ts}",
        ]
    )
    return "\n".join(lines) + "\n"


def atomic_write_text(path: str, content: str) -> None:
    parent = os.path.dirname(path) or "."
    fd, tmp_path = tempfile.mkstemp(dir=parent, prefix=".ntp_probe.prom.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fchmod(handle.fileno(), 0o644)
        os.replace(tmp_path, path)
    except Exception:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise


def main() -> int:
    interval = max(10, int(os.environ.get("INTERVAL_SECONDS", "30")))
    timeout = max(0.5, float(os.environ.get("PROBE_TIMEOUT_SECONDS", "2.0")))
    textfile_path = os.environ.get("TEXTFILE_PATH", "/textfile/ntp_probe.prom")
    targets = parse_targets(os.environ.get("NTP_TARGETS", ""))

    stop = threading.Event()

    def _handle_signal(_signum: int, _frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGTERM, _handle_signal)
    signal.signal(signal.SIGINT, _handle_signal)

    print(
        f"ntp-probe starting: interval={interval}s timeout={timeout}s targets={targets} textfile={textfile_path}",
        file=sys.stderr,
    )

    while not stop.is_set():
        try:
            body = collect_metrics_text(targets, timeout=timeout)
            atomic_write_text(textfile_path, body)
        except Exception as exc:  # noqa: BLE001
            print(f"ntp-probe error: {exc}", file=sys.stderr)
        if stop.wait(interval):
            break

    print("ntp-probe shutting down cleanly", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
