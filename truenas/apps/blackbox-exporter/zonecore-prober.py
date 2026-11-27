#!/usr/bin/env python3
"""Blackbox-compatible Prometheus HTTP /probe exporter for ZoneCore DoQ (UDP/853) and NTPv4 (UDP/123).

Runs alongside blackbox-exporter on the internal `monitoring` network (egress NATs via nas01 SGT 15 so
SC01 TrustSec permits UDP/853 and UDP/123 to ZoneCore anycast VIPs). Uses Python 3 stdlib plus OpenSSL
3.5's native QUIC client in libssl.so.3 (shipped in python:3.15-alpine) with zero external packages.

Endpoints:
  GET /health
  GET /metrics
  GET /probe?module=dns_doq&target=<ip>:853
  GET /probe?module=ntp&target=<ip>:123
"""

from __future__ import annotations

import calendar
import ctypes
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import os
import select
import signal
import socket
import struct
import sys
import threading
import time
import urllib.parse

NTP_CLIENT_PACKET = b"\x23" + b"\x00" * 47  # LI=0, VN=4, Mode=3 (client)

SSL_CTRL_SET_TLSEXT_HOSTNAME = 55
TLSEXT_NAMETYPE_host_name = 0
SSL_VERIFY_PEER = 1
SSL_ERROR_WANT_READ = 2
SSL_ERROR_WANT_WRITE = 3


class _Timeval(ctypes.Structure):
    _fields_ = [("tv_sec", ctypes.c_long), ("tv_usec", ctypes.c_long)]


class _Tm(ctypes.Structure):
    _fields_ = [
        ("tm_sec", ctypes.c_int),
        ("tm_min", ctypes.c_int),
        ("tm_hour", ctypes.c_int),
        ("tm_mday", ctypes.c_int),
        ("tm_mon", ctypes.c_int),
        ("tm_year", ctypes.c_int),
        ("tm_wday", ctypes.c_int),
        ("tm_yday", ctypes.c_int),
        ("tm_isdst", ctypes.c_int),
        ("tm_gmtoff", ctypes.c_long),
        ("tm_zone", ctypes.c_char_p),
    ]


class _OpenSSLQuic:
    """Thin ctypes binding around OpenSSL 3.5+ OSSL_QUIC_client_method()."""

    def __init__(self) -> None:
        self.libssl = ctypes.CDLL("libssl.so.3")
        self.libcrypto = ctypes.CDLL("libcrypto.so.3")

        s, c = self.libssl, self.libcrypto
        s.OSSL_QUIC_client_method.restype = ctypes.c_void_p
        s.SSL_CTX_new.argtypes = [ctypes.c_void_p]
        s.SSL_CTX_new.restype = ctypes.c_void_p
        s.SSL_CTX_free.argtypes = [ctypes.c_void_p]
        s.SSL_CTX_set_default_verify_paths.argtypes = [ctypes.c_void_p]
        s.SSL_CTX_set_default_verify_paths.restype = ctypes.c_int
        s.SSL_CTX_set_verify.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_void_p]

        s.SSL_new.argtypes = [ctypes.c_void_p]
        s.SSL_new.restype = ctypes.c_void_p
        s.SSL_free.argtypes = [ctypes.c_void_p]
        s.SSL_set_fd.argtypes = [ctypes.c_void_p, ctypes.c_int]
        s.SSL_set_fd.restype = ctypes.c_int
        s.SSL_set_blocking_mode.argtypes = [ctypes.c_void_p, ctypes.c_int]
        s.SSL_set_blocking_mode.restype = ctypes.c_int
        s.SSL_set_alpn_protos.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_uint]
        s.SSL_set_alpn_protos.restype = ctypes.c_int
        s.SSL_ctrl.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_long, ctypes.c_void_p]
        s.SSL_ctrl.restype = ctypes.c_long
        s.SSL_set1_host.argtypes = [ctypes.c_void_p, ctypes.c_char_p]
        s.SSL_set1_host.restype = ctypes.c_int
        s.SSL_set1_initial_peer_addr.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
        s.SSL_set1_initial_peer_addr.restype = ctypes.c_int
        s.SSL_connect.argtypes = [ctypes.c_void_p]
        s.SSL_connect.restype = ctypes.c_int
        s.SSL_get_error.argtypes = [ctypes.c_void_p, ctypes.c_int]
        s.SSL_get_error.restype = ctypes.c_int
        s.SSL_handle_events.argtypes = [ctypes.c_void_p]
        s.SSL_handle_events.restype = ctypes.c_int
        s.SSL_net_read_desired.argtypes = [ctypes.c_void_p]
        s.SSL_net_read_desired.restype = ctypes.c_int
        s.SSL_net_write_desired.argtypes = [ctypes.c_void_p]
        s.SSL_net_write_desired.restype = ctypes.c_int
        s.SSL_get_event_timeout.argtypes = [
            ctypes.c_void_p,
            ctypes.POINTER(_Timeval),
            ctypes.POINTER(ctypes.c_int),
        ]
        s.SSL_get_event_timeout.restype = ctypes.c_int
        s.SSL_write.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int]
        s.SSL_write.restype = ctypes.c_int
        s.SSL_stream_conclude.argtypes = [ctypes.c_void_p, ctypes.c_uint64]
        s.SSL_stream_conclude.restype = ctypes.c_int
        s.SSL_read.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int]
        s.SSL_read.restype = ctypes.c_int
        s.SSL_get_verify_result.argtypes = [ctypes.c_void_p]
        s.SSL_get_verify_result.restype = ctypes.c_long
        s.SSL_get1_peer_certificate.argtypes = [ctypes.c_void_p]
        s.SSL_get1_peer_certificate.restype = ctypes.c_void_p
        s.SSL_shutdown.argtypes = [ctypes.c_void_p]
        s.SSL_shutdown.restype = ctypes.c_int

        c.BIO_ADDR_new.restype = ctypes.c_void_p
        c.BIO_ADDR_free.argtypes = [ctypes.c_void_p]
        c.BIO_ADDR_rawmake.argtypes = [
            ctypes.c_void_p,
            ctypes.c_int,
            ctypes.c_void_p,
            ctypes.c_size_t,
            ctypes.c_uint16,
        ]
        c.BIO_ADDR_rawmake.restype = ctypes.c_int
        c.X509_free.argtypes = [ctypes.c_void_p]
        c.X509_get0_notAfter.argtypes = [ctypes.c_void_p]
        c.X509_get0_notAfter.restype = ctypes.c_void_p
        c.ASN1_TIME_to_tm.argtypes = [ctypes.c_void_p, ctypes.POINTER(_Tm)]
        c.ASN1_TIME_to_tm.restype = ctypes.c_int

        method = s.OSSL_QUIC_client_method()
        if not method:
            raise RuntimeError("OSSL_QUIC_client_method unavailable")
        self.ctx = s.SSL_CTX_new(method)
        if not self.ctx:
            raise RuntimeError("SSL_CTX_new failed")
        if s.SSL_CTX_set_default_verify_paths(self.ctx) != 1:
            raise RuntimeError("SSL_CTX_set_default_verify_paths failed")
        s.SSL_CTX_set_verify(self.ctx, SSL_VERIFY_PEER, None)
        self._ctx_lock = threading.Lock()

    def _wait_io(self, ssl_obj: int, sock: socket.socket, deadline: float) -> bool:
        rem = deadline - time.monotonic()
        if rem <= 0:
            return False
        tv = _Timeval()
        is_inf = ctypes.c_int(0)
        if (
            self.libssl.SSL_get_event_timeout(ssl_obj, ctypes.byref(tv), ctypes.byref(is_inf)) == 1
            and not is_inf.value
        ):
            wait = min(rem, max(0.001, tv.tv_sec + tv.tv_usec / 1e6))
        else:
            wait = min(rem, 0.05)
        rlist = [sock] if self.libssl.SSL_net_read_desired(ssl_obj) else []
        wlist = [sock] if self.libssl.SSL_net_write_desired(ssl_obj) else []
        if rlist or wlist:
            select.select(rlist, wlist, [], wait)
        else:
            time.sleep(min(wait, 0.01))
        self.libssl.SSL_handle_events(ssl_obj)
        return time.monotonic() < deadline

    def _peer_cert_expiry(self, ssl_obj: int) -> float | None:
        cert = self.libssl.SSL_get1_peer_certificate(ssl_obj)
        if not cert:
            return None
        try:
            not_after = self.libcrypto.X509_get0_notAfter(cert)
            if not not_after:
                return None
            tm = _Tm()
            if self.libcrypto.ASN1_TIME_to_tm(not_after, ctypes.byref(tm)) != 1:
                return None
            return float(
                calendar.timegm(
                    (
                        tm.tm_year + 1900,
                        tm.tm_mon + 1,
                        tm.tm_mday,
                        tm.tm_hour,
                        tm.tm_min,
                        tm.tm_sec,
                    )
                )
            )
        finally:
            self.libcrypto.X509_free(cert)

    def probe_doq(
        self, ip: str, port: int, server_name: str, query_name: str, timeout: float = 4.0
    ) -> dict[str, object]:
        t0 = time.monotonic()
        deadline = t0 + timeout
        result: dict[str, object] = {
            "success": False,
            "duration": 0.0,
            "answer_rrs": 0,
            "rcode": -1,
            "cert_expiry": None,
        }
        if not query_name or not server_name:
            result["duration"] = round(time.monotonic() - t0, 6)
            return result

        addr_obj = ipaddress.ip_address(ip)
        family = socket.AF_INET6 if addr_obj.version == 6 else socket.AF_INET
        packed_ip = socket.inet_pton(family, ip)

        sock = socket.socket(family, socket.SOCK_DGRAM)
        ssl_obj = None
        peer = None
        try:
            sock.setblocking(False)
            sock.connect((ip, port))
            with self._ctx_lock:
                ssl_obj = self.libssl.SSL_new(self.ctx)
            if not ssl_obj:
                return result
            peer = self.libcrypto.BIO_ADDR_new()
            if not peer:
                return result

            if self.libssl.SSL_set_fd(ssl_obj, sock.fileno()) != 1:
                return result
            if self.libssl.SSL_set_blocking_mode(ssl_obj, 0) != 1:
                return result
            alpn = b"\x03doq"
            if self.libssl.SSL_set_alpn_protos(ssl_obj, alpn, len(alpn)) != 0:
                return result
            sni_b = server_name.encode("ascii")
            if (
                self.libssl.SSL_ctrl(ssl_obj, SSL_CTRL_SET_TLSEXT_HOSTNAME, TLSEXT_NAMETYPE_host_name, sni_b)
                != 1
            ):
                return result
            if self.libssl.SSL_set1_host(ssl_obj, sni_b) != 1:
                return result
            if (
                self.libcrypto.BIO_ADDR_rawmake(peer, family, packed_ip, len(packed_ip), socket.htons(port))
                != 1
            ):
                return result
            if self.libssl.SSL_set1_initial_peer_addr(ssl_obj, peer) != 1:
                return result

            while True:
                rc = self.libssl.SSL_connect(ssl_obj)
                if rc == 1:
                    break
                err = self.libssl.SSL_get_error(ssl_obj, rc)
                if err in (SSL_ERROR_WANT_READ, SSL_ERROR_WANT_WRITE):
                    if not self._wait_io(ssl_obj, sock, deadline):
                        return result
                else:
                    return result

            result["cert_expiry"] = self._peer_cert_expiry(ssl_obj)
            if self.libssl.SSL_get_verify_result(ssl_obj) != 0:
                return result

            frame = build_dns_soa_query(query_name)
            sent = 0
            while sent < len(frame):
                n = self.libssl.SSL_write(ssl_obj, frame[sent:], len(frame) - sent)
                if n > 0:
                    sent += n
                else:
                    err = self.libssl.SSL_get_error(ssl_obj, n)
                    if err in (SSL_ERROR_WANT_READ, SSL_ERROR_WANT_WRITE):
                        if not self._wait_io(ssl_obj, sock, deadline):
                            return result
                    else:
                        return result

            if self.libssl.SSL_stream_conclude(ssl_obj, 0) != 1:
                return result

            buf = ctypes.create_string_buffer(4096)
            resp = b""
            while len(resp) < 4096:
                n = self.libssl.SSL_read(ssl_obj, buf, 4096)
                if n > 0:
                    resp += buf.raw[:n]
                    if len(resp) >= 2:
                        expected = struct.unpack("!H", resp[:2])[0]
                        if len(resp) >= 2 + expected:
                            break
                else:
                    err = self.libssl.SSL_get_error(ssl_obj, n)
                    if err in (SSL_ERROR_WANT_READ, SSL_ERROR_WANT_WRITE):
                        if not self._wait_io(ssl_obj, sock, deadline):
                            return result
                    else:
                        break

            self.libssl.SSL_shutdown(ssl_obj)
            ok, rcode, ancount = parse_dns_soa_response(resp)
            result["success"] = ok
            result["rcode"] = rcode
            result["answer_rrs"] = ancount
            return result
        except OSError:
            return result
        finally:
            result["duration"] = round(time.monotonic() - t0, 6)
            if peer:
                self.libcrypto.BIO_ADDR_free(peer)
            if ssl_obj:
                self.libssl.SSL_free(ssl_obj)
            sock.close()


def build_dns_soa_query(qname: str) -> bytes:
    """Build an RFC 9250 length-prefixed DNS wire SOA query (Message ID 0 per RFC 9250 §4.2.1)."""
    header = struct.pack("!HHHHHH", 0, 0x0100, 1, 0, 0, 0)
    labels = b"".join(bytes([len(part)]) + part.encode("ascii") for part in qname.strip(".").split(".")) + b"\x00"
    question = labels + struct.pack("!HH", 6, 1)  # QTYPE=SOA(6), QCLASS=IN(1)
    msg = header + question
    return struct.pack("!H", len(msg)) + msg


def _skip_dns_name(msg: bytes, offset: int) -> int | None:
    """Advance past a DNS wire name (handling compression pointers); return next offset or None."""
    length = len(msg)
    steps = 0
    while offset < length and steps < 128:
        steps += 1
        tag = msg[offset]
        if tag == 0:
            return offset + 1
        if (tag & 0xC0) == 0xC0:
            if offset + 2 > length:
                return None
            return offset + 2
        if (tag & 0xC0) != 0:
            return None
        offset += 1 + tag
    return None


def parse_dns_soa_response(framed: bytes) -> tuple[bool, int, int]:
    """Validate an RFC 9250 length-prefixed DNS reply: QR=1, RCODE=NOERROR, >=1 IN SOA answer RR."""
    if len(framed) < 14:
        return False, -1, 0
    msg_len = struct.unpack("!H", framed[:2])[0]
    if msg_len < 12 or len(framed) < 2 + msg_len:
        return False, -1, 0
    msg = framed[2 : 2 + msg_len]
    _mid, flags, qdcount, ancount, _nscount, _arcount = struct.unpack("!HHHHHH", msg[:12])
    qr = (flags >> 15) & 1
    rcode = flags & 0x0F
    if qr != 1 or rcode != 0 or ancount < 1:
        return False, rcode, ancount

    pos = 12
    for _ in range(qdcount):
        nxt = _skip_dns_name(msg, pos)
        if nxt is None or nxt + 4 > len(msg):
            return False, rcode, ancount
        pos = nxt + 4

    found_soa = False
    for _ in range(ancount):
        nxt = _skip_dns_name(msg, pos)
        if nxt is None or nxt + 10 > len(msg):
            return False, rcode, ancount
        rtype, rclass, _ttl, rdlen = struct.unpack("!HHIH", msg[nxt : nxt + 10])
        pos = nxt + 10 + rdlen
        if pos > len(msg):
            return False, rcode, ancount
        if rtype == 6 and rclass == 1:
            found_soa = True
    return found_soa, rcode, ancount


def probe_ntp(ip: str, port: int = 123, timeout: float = 2.0) -> dict[str, object]:
    """Send an RFC 5905 NTPv4 Mode-3 request and validate a synchronized Mode-4 reply."""
    t0 = time.monotonic()
    addr_obj = ipaddress.ip_address(ip)
    family = socket.AF_INET6 if addr_obj.version == 6 else socket.AF_INET
    sock = socket.socket(family, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        sock.sendto(NTP_CLIENT_PACKET, (ip, port))
        data, _ = sock.recvfrom(128)
        dur = round(time.monotonic() - t0, 6)
        if len(data) < 48:
            return {"success": False, "duration": dur, "stratum": 0}
        b0, stratum = struct.unpack("!BB", data[:2])
        leap = (b0 >> 6) & 0x03
        mode = b0 & 0x07
        ok = mode == 4 and leap != 3 and 1 <= stratum <= 15
        return {"success": ok, "duration": dur, "stratum": stratum}
    except OSError:
        return {"success": False, "duration": round(time.monotonic() - t0, 6), "stratum": 0}
    finally:
        sock.close()


def parse_target(raw: str, default_port: int) -> tuple[str, int]:
    """Parse 'ip' or 'ip:port' (or '[ipv6]:port') into a validated IP literal and port."""
    raw = raw.strip()
    if not raw or any(ch.isspace() for ch in raw):
        raise ValueError("empty or invalid target")
    if raw.startswith("["):
        end = raw.find("]")
        if end < 0:
            raise ValueError("unclosed IPv6 bracket")
        host = raw[1:end]
        rest = raw[end + 1 :]
        port = int(rest[1:]) if rest.startswith(":") else default_port
    elif raw.count(":") == 1:
        host, port_s = raw.rsplit(":", 1)
        port = int(port_s)
    else:
        host, port = raw, default_port
    if not (1 <= port <= 65535):
        raise ValueError("port out of range")
    ipaddress.ip_address(host)
    return host, port


def format_doq_metrics(res: dict[str, object]) -> str:
    lines = [
        "# HELP probe_success Displays whether or not the probe was a success",
        "# TYPE probe_success gauge",
        f"probe_success {1 if res['success'] else 0}",
        "# HELP probe_duration_seconds Returns how long the probe took to complete in seconds",
        "# TYPE probe_duration_seconds gauge",
        f"probe_duration_seconds {res['duration']}",
        "# HELP probe_dns_answer_rrs Returns number of entries in the answer resource record list",
        "# TYPE probe_dns_answer_rrs gauge",
        f"probe_dns_answer_rrs {res['answer_rrs']}",
        "# HELP probe_dns_rcode Returns DNS response RCODE (-1 on transport or TLS failure)",
        "# TYPE probe_dns_rcode gauge",
        f"probe_dns_rcode {res['rcode']}",
    ]
    expiry = res.get("cert_expiry")
    if isinstance(expiry, (int, float)) and expiry > 0:
        lines.extend(
            [
                "# HELP probe_ssl_earliest_cert_expiry Returns last SSL chain expiry in unixtime",
                "# TYPE probe_ssl_earliest_cert_expiry gauge",
                f"probe_ssl_earliest_cert_expiry {expiry}",
            ]
        )
    return "\n".join(lines) + "\n"


def format_ntp_metrics(res: dict[str, object]) -> str:
    lines = [
        "# HELP probe_success Displays whether or not the probe was a success",
        "# TYPE probe_success gauge",
        f"probe_success {1 if res['success'] else 0}",
        "# HELP probe_duration_seconds Returns how long the probe took to complete in seconds",
        "# TYPE probe_duration_seconds gauge",
        f"probe_duration_seconds {res['duration']}",
        "# HELP probe_ntp_stratum Stratum reported by NTPv4 server reply (0 on failure)",
        "# TYPE probe_ntp_stratum gauge",
        f"probe_ntp_stratum {res['stratum']}",
    ]
    return "\n".join(lines) + "\n"


def make_handler(quic: _OpenSSLQuic, query_name: str, server_name: str, timeout: float):
    class ProbeHandler(BaseHTTPRequestHandler):
        def log_message(self, _fmt: str, *_args: object) -> None:
            return

        def _send(self, status: int, body: str, content_type: str = "text/plain; version=0.0.4; charset=utf-8") -> None:
            data = body.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self) -> None:  # noqa: N802
            parsed = urllib.parse.urlsplit(self.path)
            if parsed.path == "/health":
                self._send(200, "OK\n", "text/plain; charset=utf-8")
                return
            if parsed.path == "/metrics":
                self._send(
                    200,
                    "# HELP zonecore_prober_up 1 if zonecore-prober is serving.\n"
                    "# TYPE zonecore_prober_up gauge\n"
                    "zonecore_prober_up 1\n",
                )
                return
            if parsed.path != "/probe":
                self._send(404, "not found\n", "text/plain; charset=utf-8")
                return

            qs = urllib.parse.parse_qs(parsed.query)
            module = (qs.get("module") or [""])[0]
            raw_target = (qs.get("target") or [""])[0]
            if module == "dns_doq":
                try:
                    ip, port = parse_target(raw_target, 853)
                except ValueError as exc:
                    self._send(400, f"invalid target: {exc}\n", "text/plain; charset=utf-8")
                    return
                res = quic.probe_doq(ip, port, server_name, query_name, timeout=timeout)
                self._send(200, format_doq_metrics(res))
                return
            if module == "ntp":
                try:
                    ip, port = parse_target(raw_target, 123)
                except ValueError as exc:
                    self._send(400, f"invalid target: {exc}\n", "text/plain; charset=utf-8")
                    return
                res = probe_ntp(ip, port, timeout=min(timeout, 2.0))
                self._send(200, format_ntp_metrics(res))
                return
            self._send(400, f"unknown module: {module}\n", "text/plain; charset=utf-8")

    return ProbeHandler


def main() -> int:
    port = int(os.environ.get("LISTEN_PORT", "9116"))
    timeout = max(0.5, float(os.environ.get("PROBE_TIMEOUT_SECONDS", "4.0")))
    query_name = os.environ.get("DOQ_QUERY_NAME", "").strip()
    server_name = os.environ.get("DOQ_SERVER_NAME", "").strip()

    quic = _OpenSSLQuic()
    handler = make_handler(quic, query_name, server_name, timeout)
    server = ThreadingHTTPServer(("0.0.0.0", port), handler)
    server.daemon_threads = True

    def _shutdown(_signum: int, _frame: object) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    print(
        f"zonecore-prober listening on :{port} (timeout={timeout}s)",
        file=sys.stderr,
    )
    try:
        server.serve_forever()
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
