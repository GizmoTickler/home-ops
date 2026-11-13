"""Keep a NickServ registration alive.

Anope drops a nick that has not been seen for its expiry window (21 days on
TorrentLeech). autobrr only keeps the alias it connects as fresh, so this job
connects as the account's main nick, logs in with SASL PLAIN, asks NickServ
for the nick's status and disconnects. Exits non-zero if the login fails, so
a failed Job (KubeJobFailed) surfaces the problem long before expiry.
"""
import base64
import os
import socket
import ssl
import sys
import time

SERVER = os.environ.get("IRC_SERVER", "irc.torrentleech.org")
PORT = int(os.environ.get("IRC_PORT", "7021"))
NICK = os.environ["IRC_NICK"]
PASSWORD = os.environ["IRC_NICK_PASS"]
DEADLINE = time.monotonic() + 90


def main() -> int:
    raw = socket.create_connection((SERVER, PORT), timeout=20)
    sock = ssl.create_default_context().wrap_socket(raw, server_hostname=SERVER)

    def send(line: str) -> None:
        sock.sendall((line + "\r\n").encode())

    send("CAP REQ :sasl")
    send(f"NICK {NICK}")
    send(f"USER {NICK} 0 * :{NICK}")

    logged_in, asked, quit_at, buf = False, False, None, b""
    while time.monotonic() < DEADLINE:
        if quit_at and time.monotonic() >= quit_at:
            send("QUIT :keepalive")
            break
        try:
            data = sock.recv(4096)
        except socket.timeout:
            continue
        if not data:
            break
        buf += data
        while b"\r\n" in buf:
            line, buf = buf.split(b"\r\n", 1)
            text = line.decode(errors="replace")
            parts = text.split()
            cmd = parts[1] if len(parts) > 1 else ""
            if text.startswith("PING"):
                send("PONG" + text[4:])
            elif cmd == "CAP" and " ACK " in text:
                send("AUTHENTICATE PLAIN")
            elif text.startswith("AUTHENTICATE +"):
                token = f"{NICK}\0{NICK}\0{PASSWORD}".encode()
                send("AUTHENTICATE " + base64.b64encode(token).decode())
            elif cmd == "903":
                logged_in = True
                print(f"SASL login OK as {NICK}")
                send("CAP END")
            elif cmd in ("902", "904", "905", "906"):
                print(f"SASL login FAILED ({cmd}): {text.split(' :', 1)[-1]}")
                send("QUIT :keepalive")
                return 1
            elif cmd == "433":
                # Main nick is online elsewhere (a real client), so its alias is
                # already being seen; log in under a spare nick just to verify.
                print(f"{NICK} is in use (online elsewhere); verifying the login only")
                send(f"NICK {NICK}_ka")
            elif cmd in ("376", "422") and logged_in and not asked:
                send(f"PRIVMSG NickServ :INFO {NICK}")
                asked, quit_at = True, time.monotonic() + 6
            elif text.startswith(":NickServ") and any(
                k in text for k in ("Registered", "Last seen:", "Expires", "online")
            ):
                print("NickServ:", text.split(" :", 1)[-1].strip())
    sock.close()
    if not logged_in:
        print("never completed SASL login before the deadline")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
