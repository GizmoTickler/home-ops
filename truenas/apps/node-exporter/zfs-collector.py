#!/usr/bin/env python3
"""Export ZFS pool and top-level dataset metrics to node_exporter's textfile dir."""

from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Callable

ZPOOL_LIST_ARGS = [
    "/usr/sbin/zpool",
    "list",
    "-Hp",
    "-o",
    "name,size,alloc,free,frag,cap,dedupratio,readonly",
]
ZPOOL_STATUS_ARGS = ["/usr/sbin/zpool", "status", "-j", "--json-int"]
ZFS_LIST_ARGS = [
    "/usr/sbin/zfs",
    "list",
    "-Hp",
    "-d",
    "1",
    "-t",
    "filesystem,volume",
    "-o",
    "name,type,used,avail,refer,usedds,usedchild,usedsnap,compressratio",
]

MetricSamples = list[tuple[dict[str, str], int | float]]
MetricSpec = tuple[str, str, MetricSamples]


def _escape_label(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def _format_labels(labels: dict[str, str]) -> str:
    if not labels:
        return ""
    pairs = [f'{k}="{_escape_label(v)}"' for k, v in labels.items()]
    return "{" + ",".join(pairs) + "}"


def _parse_int(raw: str) -> int | None:
    cleaned = raw.strip()
    if cleaned in ("", "-"):
        return None
    return int(cleaned)


def _parse_float(raw: str) -> float | None:
    cleaned = raw.strip().rstrip("x%")
    if cleaned in ("", "-"):
        return None
    return float(cleaned)


def run_host_command(
    args: list[str],
    *,
    host_root: str = "/host/root",
    timeout: int = 15,
) -> str:
    cmd = ["chroot", host_root, *args] if host_root and host_root != "/" else list(args)
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        check=True,
    )
    return result.stdout


def parse_zpool_list(output: str) -> list[dict[str, int | float | str | None]]:
    pools: list[dict[str, int | float | str | None]] = []
    for raw_line in output.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        fields = line.split("\t")
        if len(fields) != 8:
            raise ValueError(f"Expected 8 fields from zpool list, got {len(fields)}: {line!r}")
        name, size_s, alloc_s, free_s, frag_s, cap_s, dedup_s, readonly_s = fields
        cap_pct = _parse_float(cap_s)
        frag_pct = _parse_float(frag_s)
        pools.append(
            {
                "name": name,
                "size": _parse_int(size_s),
                "alloc": _parse_int(alloc_s),
                "free": _parse_int(free_s),
                "frag_ratio": round(frag_pct / 100.0, 6) if frag_pct is not None else None,
                "cap_ratio": round(cap_pct / 100.0, 6) if cap_pct is not None else None,
                "dedup_ratio": _parse_float(dedup_s),
                "readonly": 1 if readonly_s.strip().lower() == "on" else 0,
            }
        )
    return pools


def parse_zpool_status(output: str) -> dict[str, dict[str, int | None]]:
    payload = json.loads(output)
    raw_pools = payload.get("pools")
    if not isinstance(raw_pools, dict):
        raise ValueError("Missing 'pools' object in zpool status JSON")

    parsed: dict[str, dict[str, int | None]] = {}
    for pool_name, info in raw_pools.items():
        if not isinstance(info, dict):
            continue
        error_count = int(info.get("error_count") or 0)
        scan = info.get("scan_stats")
        if not isinstance(scan, dict):
            scan = {}
        is_scrub = str(scan.get("function", "")).upper() == "SCRUB"
        scrub_in_progress = (
            1 if is_scrub and str(scan.get("state", "")).upper() == "SCANNING" else 0
        )
        scrub_errors = int(scan.get("errors") or 0) if is_scrub else 0
        raw_end = int(scan.get("end_time") or 0) if is_scrub else 0
        last_scrub_end = raw_end if raw_end > 0 else None
        parsed[str(pool_name)] = {
            "error_count": error_count,
            "scrub_in_progress": scrub_in_progress,
            "scrub_errors": scrub_errors,
            "last_scrub_end": last_scrub_end,
        }
    return parsed


def parse_zfs_list(output: str) -> list[dict[str, int | float | str | None]]:
    datasets: list[dict[str, int | float | str | None]] = []
    for raw_line in output.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        fields = line.split("\t")
        if len(fields) != 9:
            raise ValueError(f"Expected 9 fields from zfs list, got {len(fields)}: {line!r}")
        name, ds_type, used_s, avail_s, refer_s, usedds_s, usedchild_s, usedsnap_s, cratio_s = fields
        zpool = name.split("/", 1)[0]
        datasets.append(
            {
                "zpool": zpool,
                "dataset": name,
                "type": ds_type.strip(),
                "used": _parse_int(used_s),
                "avail": _parse_int(avail_s),
                "refer": _parse_int(refer_s),
                "usedds": _parse_int(usedds_s),
                "usedchild": _parse_int(usedchild_s),
                "usedsnap": _parse_int(usedsnap_s),
                "compressratio": _parse_float(cratio_s),
            }
        )
    return datasets


def render_prometheus_text(specs: list[MetricSpec]) -> str:
    lines: list[str] = []
    for name, help_text, samples in specs:
        if not samples:
            continue
        lines.append(f"# HELP {name} {help_text}")
        lines.append(f"# TYPE {name} gauge")
        for labels, value in samples:
            lines.append(f"{name}{_format_labels(labels)} {value}")
    return "\n".join(lines) + "\n"


def collect_metrics_text(
    *,
    runner: Callable[[list[str]], str],
    now: float | None = None,
) -> tuple[str, float]:
    zpool_rows = parse_zpool_list(runner(ZPOOL_LIST_ARGS))
    status_by_pool = parse_zpool_status(runner(ZPOOL_STATUS_ARGS))
    dataset_rows = parse_zfs_list(runner(ZFS_LIST_ARGS))
    scrape_ts = round(time.time() if now is None else now, 3)

    pool_size: MetricSamples = []
    pool_alloc: MetricSamples = []
    pool_free: MetricSamples = []
    pool_cap: MetricSamples = []
    pool_frag: MetricSamples = []
    pool_dedup: MetricSamples = []
    pool_ro: MetricSamples = []
    pool_errs: MetricSamples = []
    pool_scrub_active: MetricSamples = []
    pool_scrub_errs: MetricSamples = []
    pool_last_scrub: MetricSamples = []

    for row in zpool_rows:
        zpool = str(row["name"])
        labels = {"zpool": zpool}
        if row["size"] is not None:
            pool_size.append((labels, int(row["size"])))
        if row["alloc"] is not None:
            pool_alloc.append((labels, int(row["alloc"])))
        if row["free"] is not None:
            pool_free.append((labels, int(row["free"])))
        if row["cap_ratio"] is not None:
            pool_cap.append((labels, float(row["cap_ratio"])))
        if row["frag_ratio"] is not None:
            pool_frag.append((labels, float(row["frag_ratio"])))
        if row["dedup_ratio"] is not None:
            pool_dedup.append((labels, float(row["dedup_ratio"])))
        pool_ro.append((labels, int(row["readonly"] or 0)))

        st = status_by_pool.get(zpool)
        if st is not None:
            if st["error_count"] is not None:
                pool_errs.append((labels, int(st["error_count"])))
            if st["scrub_in_progress"] is not None:
                pool_scrub_active.append((labels, int(st["scrub_in_progress"])))
            if st["scrub_errors"] is not None:
                pool_scrub_errs.append((labels, int(st["scrub_errors"])))
            if st["last_scrub_end"] is not None:
                pool_last_scrub.append((labels, int(st["last_scrub_end"])))

    ds_used: MetricSamples = []
    ds_avail: MetricSamples = []
    ds_refer: MetricSamples = []
    ds_usedds: MetricSamples = []
    ds_usedchild: MetricSamples = []
    ds_usedsnap: MetricSamples = []
    ds_cratio: MetricSamples = []

    for row in dataset_rows:
        labels = {
            "zpool": str(row["zpool"]),
            "dataset": str(row["dataset"]),
            "type": str(row["type"]),
        }
        if row["used"] is not None:
            ds_used.append((labels, int(row["used"])))
        if row["avail"] is not None:
            ds_avail.append((labels, int(row["avail"])))
        if row["refer"] is not None:
            ds_refer.append((labels, int(row["refer"])))
        if row["usedds"] is not None:
            ds_usedds.append((labels, int(row["usedds"])))
        if row["usedchild"] is not None:
            ds_usedchild.append((labels, int(row["usedchild"])))
        if row["usedsnap"] is not None:
            ds_usedsnap.append((labels, int(row["usedsnap"])))
        if row["compressratio"] is not None:
            ds_cratio.append((labels, float(row["compressratio"])))

    specs: list[MetricSpec] = [
        ("node_zfs_zpool_size_bytes", "Total raw storage capacity of the ZFS pool in bytes.", pool_size),
        ("node_zfs_zpool_allocated_bytes", "Raw space allocated on the ZFS pool in bytes.", pool_alloc),
        ("node_zfs_zpool_free_bytes", "Raw unallocated space remaining on the ZFS pool in bytes.", pool_free),
        ("node_zfs_zpool_capacity_ratio", "Raw ZFS pool space utilization ratio (0.0-1.0).", pool_cap),
        ("node_zfs_zpool_fragmentation_ratio", "ZFS pool free-space fragmentation ratio (0.0-1.0).", pool_frag),
        ("node_zfs_zpool_dedup_ratio", "ZFS pool deduplication ratio.", pool_dedup),
        ("node_zfs_zpool_readonly", "Whether the ZFS pool is mounted read-only (1=readonly, 0=readwrite).", pool_ro),
        ("node_zfs_zpool_error_count", "Number of known ZFS data errors reported by zpool status.", pool_errs),
        ("node_zfs_zpool_scrub_in_progress", "Whether a ZFS pool scrub is currently running (1=yes, 0=no).", pool_scrub_active),
        ("node_zfs_zpool_scrub_errors", "Errors repaired or detected during the latest ZFS pool scrub.", pool_scrub_errs),
        ("node_zfs_zpool_last_scrub_end_timestamp_seconds", "Unix timestamp when the most recent ZFS scrub finished.", pool_last_scrub),
        ("node_zfs_dataset_used_bytes", "Logical space consumed by the ZFS dataset and all descendants/snapshots in bytes.", ds_used),
        ("node_zfs_dataset_avail_bytes", "Logical space available to the ZFS dataset in bytes.", ds_avail),
        ("node_zfs_dataset_referenced_bytes", "Logical space accessible directly by the ZFS dataset in bytes.", ds_refer),
        ("node_zfs_dataset_used_by_dataset_bytes", "Space consumed directly by the dataset excluding snapshots and children in bytes.", ds_usedds),
        ("node_zfs_dataset_used_by_children_bytes", "Space consumed by descendant datasets in bytes.", ds_usedchild),
        ("node_zfs_dataset_used_by_snapshots_bytes", "Space consumed by snapshots of the dataset in bytes.", ds_usedsnap),
        ("node_zfs_dataset_compress_ratio", "Achieved compression ratio for the ZFS dataset.", ds_cratio),
        ("node_zfs_collector_up", "Whether the last ZFS textfile collection succeeded (1=up, 0=failed).", [({}, 1)]),
        ("node_zfs_collector_last_success_timestamp_seconds", "Unix timestamp of the last successful ZFS textfile collection.", [({}, scrape_ts)]),
    ]
    return render_prometheus_text(specs), scrape_ts


def render_failure_text(last_success_ts: float | None) -> str:
    specs: list[MetricSpec] = [
        ("node_zfs_collector_up", "Whether the last ZFS textfile collection succeeded (1=up, 0=failed).", [({}, 0)]),
    ]
    if last_success_ts is not None and last_success_ts > 0:
        specs.append(
            (
                "node_zfs_collector_last_success_timestamp_seconds",
                "Unix timestamp of the last successful ZFS textfile collection.",
                [({}, last_success_ts)],
            )
        )
    return render_prometheus_text(specs)


def atomic_write_text(path: str, content: str) -> None:
    parent = os.path.dirname(path) or "."
    fd, tmp_path = tempfile.mkstemp(dir=parent, prefix=".zfs.prom.", suffix=".tmp")
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
    interval = max(5, int(os.environ.get("INTERVAL_SECONDS", "30")))
    textfile_path = os.environ.get("TEXTFILE_PATH", "/textfile/zfs.prom")
    host_root = os.environ.get("HOST_ROOT", "/host/root")
    cmd_timeout = max(5, int(os.environ.get("CMD_TIMEOUT", "15")))

    stop = threading.Event()

    def _handle_signal(_signum: int, _frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGTERM, _handle_signal)
    signal.signal(signal.SIGINT, _handle_signal)

    last_success_ts: float | None = None
    runner = lambda args: run_host_command(args, host_root=host_root, timeout=cmd_timeout)

    print(
        f"zfs-collector starting: interval={interval}s textfile={textfile_path} host_root={host_root}",
        file=sys.stderr,
    )

    while not stop.is_set():
        try:
            body, last_success_ts = collect_metrics_text(runner=runner)
            atomic_write_text(textfile_path, body)
        except Exception as exc:
            print(f"zfs-collector error: {exc}", file=sys.stderr)
            try:
                atomic_write_text(textfile_path, render_failure_text(last_success_ts))
            except Exception as write_exc:
                print(f"zfs-collector failed writing error state: {write_exc}", file=sys.stderr)
        if stop.wait(interval):
            break

    print("zfs-collector shutting down cleanly", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
