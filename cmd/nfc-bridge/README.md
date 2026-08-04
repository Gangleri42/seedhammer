# nfc-bridge

A localhost daemon that lets **SeedHammer Studio**'s Send button write
to a USB NFC reader on the desktop, where Web NFC (`NDEFReader`) does
not exist. It reuses the same nfcpy write path as
`cmd/textplate/write-nfc.py`.

Studio is hosted at `https://gangleri42.github.io/studio/`; its Send
button calls this daemon cross-origin. Only allow-listed origins receive
the CORS + Private Network Access headers a public HTTPS page needs to
reach a loopback server. A plain GET to `http://127.0.0.1:8787/`
redirects to the hosted app.

## Install

`../../install.sh send` at the repo root sets all of this up: the
`~/.nfc-venv` python environment, the reader's udev rule, and the
bridge as a user service that starts at boot, allow-listing the hosted
Studio and the local serve at `http://localhost:8788`. The sections
below are the by-hand path and the reference for what the installer
does.

## Endpoints

- `GET  /bridge/health` returns `{"ok":true,...}`; Studio probes it to
  decide whether to route Send through the bridge.
- `POST /bridge/send` with `{"payload":"<text envelope>"}` or
  `{"payloadB64":"<binary curves payload, base64>"}` writes the payload
  as a `seedhammer.com:curves` NDEF record and returns
  `{"status": ...}`: `written`, `delivered_unconfirmed` (the known
  tail-commit race; check the device screen), `no_target`, `no_reader`,
  `busy`, or `error`.
- Anything else is a 404. The bridge serves no app files.

## Requirements

- Python 3 with `nfcpy` and `ndeflib` (`pip install nfcpy ndeflib`);
  a venv keeps it tidy.
- A USB NFC reader: an **ACS ACR122U**, which is what this bridge, the
  senders and the repo's udev rule are written against. Others that nfcpy
  supports generally work; the rule below is then yours to adapt.
- Reader access without sudo: a udev rule for it (`install.sh send` writes
  one for the ACR122U).

## Run once

    python3 cmd/nfc-bridge/bridge.py

using the Python that has nfcpy installed. Then open
`http://127.0.0.1:8787/`. The Send button lights up.

## Run on boot

    mkdir -p ~/.config/systemd/user
    cp cmd/nfc-bridge/seedhammer-nfc-bridge.service ~/.config/systemd/user/
    loginctl enable-linger "$USER"
    systemctl --user daemon-reload
    systemctl --user enable --now seedhammer-nfc-bridge

The unit assumes the venv at `~/.nfc-venv` and the checkout at
`~/seedhammer`; edit the `ExecStart` paths if yours differ. The
installer writes an equivalent unit with absolute paths and an
`SH_BRIDGE_ORIGINS` line carrying the full allow-list (the variable
replaces the built-in default, so every origin must be re-listed);
this file stays the reference.

Logs: `journalctl --user -u seedhammer-nfc-bridge -f` and the file named
by `SH_BRIDGE_LOG` (default `~/.local/state/nfc-bridge.log`).

## Security

- Binds `127.0.0.1` only.
- Validates the `Host` header against the loopback, so DNS rebinding
  (a public site re-pointing its hostname to 127.0.0.1) cannot reach
  any endpoint.
- Refuses `/bridge/send` from origins not on the allow-list. The
  allow-list is `SH_BRIDGE_ORIGINS`-overridable: point it at a
  project-controlled origin, or leave only the loopback entries so
  nothing cross-origin can call it.
- Caps the request body, so an oversized payload can't exhaust memory.
- The strong backstop is physical: nothing is written until you tap
  the reader, and the device shows a confirm screen before engraving.
  The bridge only *writes* engraving payloads; it never reads secrets.

An adversarial audit (2026-07-14) found no serious vulnerability: only
low-severity items (DNS-rebinding fingerprinting, trust in the
allow-listed origin, a bounded local DoS), all addressed above or
bounded by the physical confirm gate.
