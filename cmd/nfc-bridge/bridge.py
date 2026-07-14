#!/usr/bin/env python3
"""seedhammer-nfc-bridge: a localhost daemon that writes SeedHammer
curves/text payloads to a USB NFC reader on behalf of the web editor,
so the Studio "Send" button works on desktop, where Web NFC does not
exist.

It also serves Studio itself, so the local page POSTs same-origin with
no CORS or Private Network Access friction. The hosted site may use it
cross-origin: only allow-listed origins get the CORS + PNA headers.

Security posture (this drives a hardware wallet tool):
  - binds 127.0.0.1 only, so no other host can reach it;
  - rejects browser requests whose Origin is not allow-listed;
  - the write does nothing until the operator taps the reader, and the
    device shows a confirm screen they approve, so a rogue page can at
    most make the SeedHammer ask "engrave this?".

Run:  /home/wodan/.nfc-venv/bin/python3 bridge.py
Env:  SH_BRIDGE_PORT (default 8787), SH_BRIDGE_EDITOR (Studio dir).
"""

import datetime
import http.server
import json
import os
import threading
import time

import ndef
import nfc

PORT = int(os.environ.get("SH_BRIDGE_PORT", "8787"))
EDITOR_DIR = os.environ.get(
    "SH_BRIDGE_EDITOR", "/home/wodan/seedhammer/cmd/svgplate/editor"
)
LOG_PATH = os.path.expanduser("~/bench/nfc-bridge.log")
RECORD_TYPE = "urn:nfc:ext:seedhammer.com:curves"
TAP_TIMEOUT_S = 30

ALLOWED_ORIGINS = {
    "https://gangleri42.github.io",
    f"http://127.0.0.1:{PORT}",
    f"http://localhost:{PORT}",
}

# One NFC write at a time: the reader is a single shared device.
_write_lock = threading.Lock()


def log(msg):
    try:
        with open(LOG_PATH, "a") as f:
            f.write(f"{datetime.datetime.now().isoformat(timespec='seconds')} {msg}\n")
    except OSError:
        pass


def write_payload(payload: bytes, timeout=TAP_TIMEOUT_S) -> dict:
    """Write payload as a seedhammer.com:curves NDEF record, reusing the
    bench-proven flow. Returns a structured result rather than exiting."""
    record = ndef.Record(RECORD_TYPE, "", payload)
    result = {}

    def on_connect(tag):
        if tag.ndef is None:
            result.update(status="error", detail="target is not NDEF-formatted")
            return True
        if not tag.ndef.is_writeable:
            result.update(status="error", detail="target is read-only")
            return True
        try:
            tag.ndef.records = [record]
            result.update(status="written", bytes=tag.ndef.length)
        except Exception as e:
            msg = str(e)
            # The known tail-commit race: every data chunk lands, only the
            # final ack is lost. The device usually has the full payload.
            if "timeout" in msg.lower():
                result.update(status="delivered_unconfirmed", detail=msg)
            else:
                result.update(status="error", detail=msg)
        return True

    try:
        with nfc.ContactlessFrontend("usb") as clf:
            deadline = time.monotonic() + timeout
            clf.connect(
                rdwr={"on-connect": on_connect},
                terminate=lambda: time.monotonic() > deadline,
            )
    except OSError as e:
        return {"status": "no_reader", "detail": str(e)}
    if not result:
        return {"status": "no_target"}
    return result


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k):
        super().__init__(*a, directory=EDITOR_DIR, **k)

    # --- CORS + Private Network Access ---
    def _cors(self):
        origin = self.headers.get("Origin", "")
        if origin in ALLOWED_ORIGINS:
            self.send_header("Access-Control-Allow-Origin", origin)
            self.send_header("Access-Control-Allow-Private-Network", "true")
            self.send_header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.send_header("Vary", "Origin")

    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self._cors()
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_OPTIONS(self):
        self.send_response(204)
        self._cors()
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        if self.path.split("?")[0].rstrip("/") == "/bridge/health":
            return self._json(200, {"ok": True, "name": "seedhammer-nfc-bridge", "version": 1})
        return super().do_GET()

    def do_POST(self):
        if self.path.split("?")[0].rstrip("/") != "/bridge/send":
            return self._json(404, {"error": "not found"})
        # CORS only guards browsers; a present-but-unlisted Origin is a
        # cross-site page and is refused. Absent Origin is a local
        # non-browser client (curl), which 127.0.0.1 binding already bounds.
        origin = self.headers.get("Origin")
        if origin and origin not in ALLOWED_ORIGINS:
            return self._json(403, {"error": "origin not allowed"})
        try:
            n = int(self.headers.get("Content-Length", "0"))
            data = json.loads(self.rfile.read(n) or b"{}")
            payload = data["payload"]
            if not isinstance(payload, str) or not payload:
                raise ValueError("empty payload")
            payload = payload.encode()
        except (ValueError, KeyError, json.JSONDecodeError) as e:
            return self._json(400, {"error": f"bad request: {e}"})
        if not _write_lock.acquire(blocking=False):
            return self._json(409, {"status": "busy"})
        try:
            log(f"send {len(payload)} bytes from {origin or 'local'}")
            result = write_payload(payload)
            log(f"outcome {result.get('status')} {result.get('detail','')}")
        finally:
            _write_lock.release()
        return self._json(200, result)

    def log_message(self, *a):
        pass  # quiet; real events go to LOG_PATH


def main():
    server = http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    log(f"bridge up on 127.0.0.1:{PORT}, serving {EDITOR_DIR}")
    print(f"seedhammer-nfc-bridge on http://127.0.0.1:{PORT}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
