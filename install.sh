#!/usr/bin/env bash
# SeedHammer operator install: the NFC send stack, the CLI tools, and
# the pinned flashing tool.
#
#   ./install.sh [send]        install the send stack (default profile)
#   ./install.sh dev           firmware development pointers (nix); changes nothing
#   ./install.sh --verify      check everything; writes nothing, but the reader
#                              probe claims the device for a moment
#   ./install.sh --uninstall   undo exactly what a previous run recorded
#   ./install.sh --yes ...     answer yes to this script's own prompts
#                              (shared bits are still kept on uninstall)
#
# Every file this script creates or replaces is recorded in a manifest
# under ~/.local/state/seedhammer-install/; a file it replaces is backed
# up first and restored on uninstall. Reruns are no-ops unless something
# drifted. It never touches key files or ~/bench, never removes a
# ~/.nfc-venv it did not create, and never edits shell rc files (PATH
# advice is printed instead). Root is used per step, with the exact
# files and commands shown first.
set -euo pipefail

REPO=$(cd "$(dirname "$0")" && pwd)
USER=$(id -un)   # real uid's name; an inherited USER can be wrong under su / sudo -E
VENV="$HOME/.nfc-venv"
STATE="$HOME/.local/state/seedhammer-install"
MANIFEST="$STATE/manifest.tsv"
BACKUPS="$STATE/backups"
REQS="$REPO/cmd/nfc-bridge/requirements.txt"
BRIDGE="$REPO/cmd/nfc-bridge/bridge.py"

# The bridge allow-list REPLACES its built-in default, so the full set
# must always be listed: hosted Studio, the bridge's own loopback
# origins, and the local Studio serve (seedhammer-studio/serve.sh).
ORIGINS="https://gangleri42.github.io,http://127.0.0.1:8787,http://localhost:8787,http://127.0.0.1:8788,http://localhost:8788"

GO_VERSION=go1.25.10
GO_SHA_LINUX_AMD64=42d4f7a32316aa66591eca7e89867256057a4264451aca10570a715b3637ba70
GO_SHA_LINUX_ARM64=654da1f9b50a5d1c2a85ccf8ed405aa89c06e94d18384628bf186f7712677b08
GO_SHA_DARWIN_AMD64=52321165a3146cd91865ef98371506a846ed4dc4f9f1c9323e5ad90d2a411e06
GO_SHA_DARWIN_ARM64=795691a425de7e7cdba3544f354dcd2cebcf52e87dc6898193878f34eb6d634f

PICOTOOL_VERSION=2.3.0
PICOTOOL_BASE=https://github.com/raspberrypi/pico-sdk-tools/releases/download/v2.3.0-0
PICOTOOL_SHA_AMD64=d8222dbb04e83427bcaef8466fe6e76b0e0193c3a140029934bd365dae49f61f
PICOTOOL_SHA_ARM64=90fc7e939a68f33b286f9fc4eaa58c11b49c4251c472d541c73d211ec28b8922
PICOTOOL_DEST="$HOME/.local/opt/picotool-$PICOTOOL_VERSION"
PICOTOOL_LINK="$HOME/.local/bin/picotool"

UNIT_NAME=seedhammer-nfc-bridge.service
UNIT_PATH="$HOME/.config/systemd/user/$UNIT_NAME"
PLIST_LABEL=com.seedhammer.nfc-bridge
PLIST_PATH="$HOME/Library/LaunchAgents/$PLIST_LABEL.plist"

UDEV_PATH=/etc/udev/rules.d/99-seedhammer-nfc.rules
UDEV_RULE='# ACS ACR122U NFC reader (072f:2200) accessible without sudo for the
# SeedHammer send tooling (nfcpy). uaccess grants the seated user;
# plugdev is the fallback for headless and lingered sessions.
# Installed by seedhammer/install.sh.
SUBSYSTEM=="usb", ATTRS{idVendor}=="072f", ATTRS{idProduct}=="2200", TAG+="uaccess", MODE="0660", GROUP="plugdev"'

MODPROBE_PATH=/etc/modprobe.d/seedhammer-nfc-blacklist.conf
MODPROBE_CONF='# The kernel pn533 stack claims the NFC reader and nfcpy cannot detach
# it (EBUSY). Installed by seedhammer/install.sh only on systems where
# the modules were seen loaded. System-wide: disables kernel NFC.
blacklist pn533_usb
blacklist pn533
blacklist nfc'

LOGROTATE_PATH=/etc/logrotate.d/seedhammer-nfc-bridge
NEWSYSLOG_PATH=/etc/newsyslog.d/seedhammer-nfc-bridge.conf
PICOTOOL_UDEV=/etc/udev/rules.d/99-picotool.rules

YES=0
FAILS=0
PENDINGS=0
GO=
KEPT_PATHS=

say()  { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
act()  { printf '  %s\n' "$*"; }                       # "+ installed x" / "= unchanged x"
report() {                                              # OK|PENDING|FAIL "text"
  printf '%-8s %s\n' "$1" "$2"
  case $1 in FAIL) FAILS=$((FAILS + 1)) ;; PENDING) PENDINGS=$((PENDINGS + 1)) ;; esac
}

confirm() { # hard gate: declining aborts the run
  [ "$YES" = 1 ] && return 0
  [ -t 0 ] || fail "'$1' needs a terminal to confirm, or pass --yes"
  printf '%s [y/N] ' "$1"
  read -r r
  case $r in y|Y) return 0 ;; *) fail "declined" ;; esac
}

# Soft prompts read the tty directly (stdin may be a process
# substitution in the uninstall loop). The fd dance keeps stderr open so
# the prompt is actually visible; only the tty-open error is discarded.
tty_ask() { # prompt -> 0 yes, 1 no/no-tty
  local r
  { exec 3</dev/tty; } 2>/dev/null || return 1
  printf '%s [y/N] ' "$1" > /dev/tty
  read -r r <&3
  exec 3<&-
  case $r in y|Y) return 0 ;; *) return 1 ;; esac
}

ask_go() {     # forward action: --yes means yes
  [ "$YES" = 1 ] && return 0
  tty_ask "$1"
}

ask_remove() { # shared-bit removal: --yes and no-tty keep
  [ "$YES" = 1 ] && return 1
  tty_ask "$1"
}

as_root() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

check_sha() { # file want label
  local got; got=$(sha256_of "$1")
  [ "$got" = "$2" ] || fail "checksum mismatch for $3: got $got want $2"
}

# ---------------------------------------------------------------- manifest

record() { # kind path extra — one entry per kind+path; byte-stable when unchanged
  local kind=$1 path=$2 extra=${3:-} line
  line=$(printf '%s\t%s\t%s' "$kind" "$path" "$extra")
  mkdir -p "$STATE"
  touch "$MANIFEST"
  grep -qxF -- "$line" "$MANIFEST" 2>/dev/null && return 0
  k="$kind" p="$path" awk -F'\t' '!($1 == ENVIRON["k"] && $2 == ENVIRON["p"])' "$MANIFEST" > "$MANIFEST.tmp"
  printf '%s\n' "$line" >> "$MANIFEST.tmp"
  mv "$MANIFEST.tmp" "$MANIFEST"
}

recorded() { # kind path
  [ -f "$MANIFEST" ] || return 1
  k="$1" p="$2" awk -F'\t' 'BEGIN{r=1} $1==ENVIRON["k"] && $2==ENVIRON["p"] {r=0} END{exit r}' "$MANIFEST"
}

recorded_extra() { # kind path -> prints extra
  [ -f "$MANIFEST" ] || return 0
  k="$1" p="$2" awk -F'\t' '$1==ENVIRON["k"] && $2==ENVIRON["p"] {print $3}' "$MANIFEST"
}

# install_file target mode need_root [kind]; desired content on stdin.
# CALL CONVENTION: feed stdin via `< <(producer)`, NEVER as a pipeline
# element (`producer | install_file`): a pipeline runs it in a subshell
# where its internal hard-fail exit would be swallowed by the call
# site's `|| true` / `if`. In the parent shell, a hard failure aborts
# the whole run; the return code stays 0 wrote / 1 already current.
# Refuses symlinked and non-regular targets (a link could route reads
# or writes into key material). Adopts a byte-identical pre-existing
# file with a marker so uninstall can ask instead of deleting it; backs
# up a divergent file we did not author, and the operator's local edits
# to a file we did, before replacing it.
install_file() {
  local target=$1 mode=$2 need_root=$3 kind=${4:-file} tmp prior_extra on_disk
  if [ -L "$target" ]; then
    fail "$target is a symlink; refusing to read or write through it (move it aside)"
  fi
  if [ -e "$target" ] && [ ! -f "$target" ]; then
    fail "$target exists and is not a regular file; refusing to write it"
  fi
  tmp=$(mktemp "${TMPDIR:-/tmp}/shinstall.XXXXXX") || fail "mktemp failed"
  ifail() { rm -f "$tmp"; fail "$@"; }
  cat > "$tmp" || ifail "spooling content for $target failed"
  if [ -f "$target" ] && cmp -s "$tmp" "$target"; then
    rm -f "$tmp"
    act "= unchanged $target"
    if ! recorded "$kind" "$target"; then
      record "$kind" "$target" "adopted:$(sha256_of "$target")"
    fi
    return 1
  fi
  prior_extra=$(recorded_extra "$kind" "$target")
  if [ -f "$target" ] && ! recorded replaced "$target"; then
    on_disk=$(sha256_of "$target")
    local why=''
    case ":$prior_extra" in
      :|:adopted:*) why="prior" ;;                     # not ours: preserve the original
      *) if [ "$on_disk" != "$prior_extra" ]; then why="your local edits to"; fi ;;
    esac
    if [ -n "$why" ]; then
      local bk; bk=$(printf '%s' "$target" | tr '/' '_')
      mkdir -p "$BACKUPS" || ifail "mkdir $BACKUPS failed"
      cp "$target" "$BACKUPS/$bk" || ifail "backup of $target failed"
      record replaced "$target" "$bk"
      act "~ backed up $why $target"
    fi
  fi
  if [ "$need_root" = 1 ]; then
    as_root mkdir -p "$(dirname "$target")" || ifail "mkdir for $target failed"
    as_root install -m "$mode" "$tmp" "$target" || ifail "install of $target failed"
  else
    mkdir -p "$(dirname "$target")" || ifail "mkdir for $target failed"
    install -m "$mode" "$tmp" "$target" || ifail "install of $target failed"
  fi
  rm -f "$tmp"
  [ -f "$target" ] || fail "$target did not appear after install"
  record "$kind" "$target" "$(sha256_of "$target")"
  act "+ installed $target"
  return 0
}

# ------------------------------------------------------------------- env

OS=
WSL=0
detect_os() {
  case "$(uname -s)" in
    Linux)
      OS=linux
      if grep -qi microsoft /proc/version 2>/dev/null; then WSL=1; fi
      ;;
    Darwin) OS=darwin ;;
    MINGW*|MSYS*|CYGWIN*) windows_recipe; exit 0 ;;
    *) fail "unsupported OS: $(uname -s)" ;;
  esac
}

windows_recipe() {
  cat <<'EOF'
Native Windows is not supported (nfcpy needs a WinUSB driver swap and
sh2key does not build there). The supported Windows path is WSL2:

  1. In elevated PowerShell, once:   winget install usbipd
  2. Plug the NFC reader, then:      usbipd list
                                     usbipd bind --busid <BUSID>
  3. Per WSL session (or a logon task):
                                     usbipd attach --wsl --auto-attach --busid <BUSID>
  4. Inside your WSL distro (systemd on in /etc/wsl.conf):
                                     git clone https://github.com/Gangleri42/seedhammer
                                     cd seedhammer && ./install.sh send

No changes were made.
EOF
}

systemd_ok() {
  [ -d /run/systemd/system ] || return 1
  command -v systemctl >/dev/null 2>&1 || return 1
  systemctl --user show-environment >/dev/null 2>&1
}

have_active() { systemctl is-active --quiet "$1" 2>/dev/null; }

# ------------------------------------------------------------- send steps

PY=python3
pick_python() {
  if command -v python3.12 >/dev/null 2>&1; then PY=python3.12; fi
  command -v "$PY" >/dev/null 2>&1 || fail "python3 not found (packages step should have installed it)"
  "$PY" -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 9) else 1)' \
    || fail "$PY is older than 3.9 (write-nfc.py floor)"
  if ! "$PY" -c 'import sys; raise SystemExit(0 if sys.version_info < (3, 15) else 1)'; then
    say "note: $PY is beyond the verified 3.9-3.14 range; if the venv import check fails, install python 3.12"
  fi
}

linux_packages() {
  local pkgs_debian='' pkgs_fedora='' pkgs_arch='' missing=''
  if ! command -v python3 >/dev/null 2>&1; then
    missing="$missing python3"
    pkgs_debian="$pkgs_debian python3 python3-venv"
    pkgs_fedora="$pkgs_fedora python3"
    pkgs_arch="$pkgs_arch python"
  fi
  if ! { ldconfig -p 2>/dev/null || /sbin/ldconfig -p 2>/dev/null; } | grep libusb-1.0 >/dev/null; then
    missing="$missing libusb-1.0"
    pkgs_debian="$pkgs_debian libusb-1.0-0"
    pkgs_fedora="$pkgs_fedora libusb1"
    pkgs_arch="$pkgs_arch libusb"
  fi
  if ! command -v curl >/dev/null 2>&1; then
    missing="$missing curl"
    pkgs_debian="$pkgs_debian curl ca-certificates"
    pkgs_fedora="$pkgs_fedora curl"
    pkgs_arch="$pkgs_arch curl"
  fi
  if [ -z "$missing" ]; then act "= system packages present"; return 0; fi

  local id like pm='' pkgs=''
  id=$(. /etc/os-release 2>/dev/null && printf '%s' "${ID:-}") || true
  like=$(. /etc/os-release 2>/dev/null && printf '%s' "${ID_LIKE:-}") || true
  case " $id $like " in
    *debian*|*ubuntu*) pm=apt;    pkgs=$pkgs_debian ;;
    *fedora*|*rhel*)   pm=dnf;    pkgs=$pkgs_fedora ;;
    *arch*)            pm=pacman; pkgs=$pkgs_arch ;;
  esac
  [ -n "$pm" ] || fail "missing:$missing — unknown package manager, install them yourself and rerun"
  say "system packages needed:$missing"
  case $pm in
    apt)    say "  will run: apt-get update && apt-get install -y$pkgs" ;;
    dnf)    say "  will run: dnf install -y$pkgs" ;;
    pacman) say "  will run: pacman -S --needed --noconfirm$pkgs" ;;
  esac
  confirm "install system packages with root?"
  # shellcheck disable=SC2086  # $pkgs is a deliberately split package list
  case $pm in
    apt)    as_root apt-get update; as_root apt-get install -y $pkgs ;;
    dnf)    as_root dnf install -y $pkgs ;;
    pacman) as_root pacman -S --needed --noconfirm $pkgs ;;
  esac
  record pkg "$pm" "$pkgs"
  act "+ installed system packages:$pkgs"
}

pcscd_check() {
  systemd_ok || return 0
  if have_active pcscd || have_active pcscd.socket; then
    fail "pcscd is active and will hold the ACR122U away from nfcpy.
Stop and disable it first:  sudo systemctl disable --now pcscd.socket pcscd
then rerun. (This script does not disable other software by itself.)"
  fi
  act "= pcscd not active"
}

go_new_enough() { # 1.21+ can GOTOOLCHAIN-fetch the pinned go1.25.10
  local v minor
  v=$("$1" version 2>/dev/null | awk '{print $3}')
  case $v in
    go1.[0-9]) return 1 ;;
    go1.*) minor=${v#go1.}; minor=${minor%%[!0-9]*}
           [ -n "$minor" ] && [ "$minor" -ge 21 ] ;;
    go[2-9]*) return 0 ;;
    *) return 0 ;;  # unparseable (devel builds): let go.mod enforcement decide
  esac
}

ensure_go() {
  if command -v go >/dev/null 2>&1 && go_new_enough "$(command -v go)"; then
    GO=$(command -v go)
    act "= go on PATH: $("$GO" version | awk '{print $3}')"
    return 0
  fi
  # The same version gate as PATH go: a stranded pre-1.21 toolchain
  # here would fail on GOTOOLCHAIN later, and the move-it-aside
  # message below is the honest answer.
  if [ -x "$HOME/.local/go/bin/go" ] && go_new_enough "$HOME/.local/go/bin/go"; then GO="$HOME/.local/go/bin/go"; act "= go at ~/.local/go"; return 0; fi
  if command -v go >/dev/null 2>&1; then
    say "go on PATH is older than 1.21 and cannot fetch the pinned toolchain"
  fi
  local sha file
  case "$OS-$(uname -m)" in
    linux-x86_64)                 sha=$GO_SHA_LINUX_AMD64;  file=$GO_VERSION.linux-amd64.tar.gz ;;
    linux-aarch64|linux-arm64)    sha=$GO_SHA_LINUX_ARM64;  file=$GO_VERSION.linux-arm64.tar.gz ;;
    darwin-arm64)                 sha=$GO_SHA_DARWIN_ARM64; file=$GO_VERSION.darwin-arm64.tar.gz ;;
    darwin-x86_64)                sha=$GO_SHA_DARWIN_AMD64; file=$GO_VERSION.darwin-amd64.tar.gz ;;
    *) fail "no pinned Go archive for $OS/$(uname -m); install Go >= 1.21 yourself and rerun" ;;
  esac
  if [ -e "$HOME/.local/go" ]; then
    fail "$HOME/.local/go exists but has no usable go binary; move it aside and rerun (not deleting it for you)"
  fi
  say "Go toolchain not found; will download $file (~57 MB) to ~/.local/go"
  confirm "download and install the pinned Go toolchain?"
  mkdir -p "$STATE/dl" "$HOME/.local"
  curl -fL -o "$STATE/dl/$file" "https://go.dev/dl/$file"
  check_sha "$STATE/dl/$file" "$sha" "$file"
  tar -C "$HOME/.local" -xzf "$STATE/dl/$file"
  record dir "$HOME/.local/go" go-toolchain
  GO="$HOME/.local/go/bin/go"
  act "+ installed $GO_VERSION to ~/.local/go (add ~/.local/go/bin to PATH for direct use)"
}

ensure_venv() {
  if [ -x "$VENV/bin/python3" ] && "$VENV/bin/python3" -c 'import nfc, ndef, usb1, serial' >/dev/null 2>&1; then
    act "= venv $VENV imports the NFC stack"
    return 0
  fi
  if [ -e "$VENV" ]; then
    if [ ! -x "$VENV/bin/pip" ]; then
      if recorded venv "$VENV"; then
        say "venv $VENV is a broken partial creation of this script; recreating"
        rm -rf "$VENV"
      else
        fail "$VENV exists but is not a usable venv and was not created by this script; move it aside and rerun"
      fi
    else
      if recorded venv "$VENV"; then
        say "venv $VENV lost its imports; repairing with the pinned set"
      else
        ask_go "repair the existing $VENV in place (pip install the hash-pinned NFC set into it)?" \
          || fail "left $VENV untouched; move it aside or repair it yourself, then rerun"
      fi
      "$VENV/bin/pip" install --quiet --require-hashes -r "$REQS"
      act "+ repaired venv packages (hash-pinned)"
      return 0
    fi
  fi
  "$PY" -c 'import ensurepip' >/dev/null 2>&1 \
    || fail "$PY cannot create venvs (no ensurepip); on Debian/Ubuntu: sudo apt-get install ${PY#python}-venv or python3-venv"
  record venv "$VENV" created   # recorded first, so a failed creation is still uninstallable
  "$PY" -m venv "$VENV"
  "$VENV/bin/pip" install --quiet --require-hashes -r "$REQS"
  act "+ created venv $VENV (hash-pinned nfcpy stack)"
}

linux_udev() {
  local changed=0
  say "reader access rule for headless and lingered sessions:"
  printf '%s\n' "$UDEV_RULE" | sed 's/^/    /'
  if printf '%s\n' "$UDEV_RULE" | cmp -s - "$UDEV_PATH" 2>/dev/null; then
    install_file "$UDEV_PATH" 0644 1 < <(printf '%s\n' "$UDEV_RULE") || true
  else
    say "  will run: install -m 0644 <rule> $UDEV_PATH; udevadm control --reload-rules; udevadm trigger (072f only)"
    confirm "install $UDEV_PATH with root?"
    if install_file "$UDEV_PATH" 0644 1 < <(printf '%s\n' "$UDEV_RULE"); then changed=1; fi
  fi
  if [ "$changed" = 1 ] && command -v udevadm >/dev/null 2>&1; then
    as_root udevadm control --reload-rules
    as_root udevadm trigger --subsystem-match=usb --attr-match=idVendor=072f
    act "+ reloaded udev rules (replug the reader once if it was plugged in)"
  fi
  if ! getent group plugdev >/dev/null 2>&1; then
    say "  will run: groupadd plugdev"
    confirm "create the plugdev group with root?"
    as_root groupadd plugdev
    record groupadd plugdev ""
    act "+ created group plugdev"
  fi
  if id -nG "$USER" | tr ' ' '\n' | grep -qx plugdev; then
    act "= $USER already in plugdev"
  elif getent group plugdev | awk -F: '{print $4}' | tr ',' '\n' | grep -qx "$USER"; then
    act "= $USER in plugdev (pending re-login)"
  else
    say "  will run: usermod -aG plugdev $USER"
    confirm "add $USER to plugdev with root?"
    as_root usermod -aG plugdev "$USER"
    record group-member plugdev "$USER"
    act "+ added $USER to plugdev (takes effect at next login)"
  fi
}

linux_modprobe() {
  if lsmod 2>/dev/null | grep -qE '^pn533(_usb)? '; then
    say "kernel pn533 driver is bound; it must be blacklisted for nfcpy (system-wide: disables kernel NFC):"
    printf '%s\n' "$MODPROBE_CONF" | sed 's/^/    /'
    say "  will run: install -m 0644 <conf> $MODPROBE_PATH; modprobe -r pn533_usb pn533 nfc"
    confirm "install $MODPROBE_PATH and unload the modules with root?"
    install_file "$MODPROBE_PATH" 0644 1 modprobe < <(printf '%s\n' "$MODPROBE_CONF") || true
    if ! as_root modprobe -r pn533_usb pn533 nfc 2>/dev/null; then
      act "~ modules busy; they release on reader replug or reboot"
    else
      act "+ unloaded pn533 modules"
    fi
  else
    if printf '%s\n' "$MODPROBE_CONF" | cmp -s - "$MODPROBE_PATH" 2>/dev/null; then
      act "= pn533 blacklist installed; modules not bound"
    else
      act "= kernel pn533 driver not bound (no blacklist needed on this machine)"
    fi
  fi
}

build_tools() {
  say "building CLI tools into the checkout root (first build fetches Go modules over the network)"
  (cd "$REPO" && "$GO" build -o sh2key ./cmd/sh2key && "$GO" build -o svgplate ./cmd/svgplate)
  record file "$REPO/sh2key" built
  record file "$REPO/svgplate" built
  act "= built $REPO/{sh2key,svgplate}"
}

picotool_ok() {
  command -v picotool >/dev/null 2>&1 || return 1
  local major
  major=$(picotool version 2>/dev/null | sed -n 's/^picotool v\{0,1\}\([0-9]*\).*/\1/p' | head -1)
  [ -n "$major" ] && [ "$major" -ge 2 ]
}

ensure_picotool() {
  if picotool_ok; then
    act "= picotool on PATH: $(picotool version 2>/dev/null | head -1)"
    return 0
  fi
  if [ -x "$PICOTOOL_DEST/picotool" ] && [ "$(readlink "$PICOTOOL_LINK" 2>/dev/null)" = "$PICOTOOL_DEST/picotool" ]; then
    recorded dir "$PICOTOOL_DEST" || record dir "$PICOTOOL_DEST" adopted:picotool
    record symlink "$PICOTOOL_LINK" "$PICOTOOL_DEST/picotool"
    act "= picotool $PICOTOOL_VERSION installed at ~/.local/bin (not on PATH yet)"
    path_note_local_bin
    return 0
  fi
  if [ "$OS" = darwin ]; then
    command -v brew >/dev/null 2>&1 || fail "picotool needs brew on macOS: brew install picotool (or use nix develop)"
    confirm "brew install picotool?"
    brew install picotool
    record brew picotool ""
    act "+ installed picotool via brew"
    return 0
  fi
  local sha file
  case "$(uname -m)" in
    x86_64)        sha=$PICOTOOL_SHA_AMD64; file=picotool-$PICOTOOL_VERSION-x86_64-lin.tar.gz ;;
    aarch64|arm64) sha=$PICOTOOL_SHA_ARM64; file=picotool-$PICOTOOL_VERSION-aarch64-lin.tar.gz ;;
    *) fail "no pinned picotool for linux/$(uname -m); use nix develop or build it yourself" ;;
  esac
  if [ -e "$PICOTOOL_LINK" ] || [ -L "$PICOTOOL_LINK" ]; then
    case "$(readlink "$PICOTOOL_LINK" 2>/dev/null)" in
      "$HOME"/.local/opt/picotool-*/picotool) : ;;  # ours from an older version; fine to retarget
      *) fail "$PICOTOOL_LINK exists and is not this script's symlink; move it aside and rerun" ;;
    esac
  fi
  if [ ! -x "$PICOTOOL_DEST/picotool" ]; then
    say "will install picotool $PICOTOOL_VERSION (pinned, checksummed) to $PICOTOOL_DEST"
    confirm "download picotool?"
    mkdir -p "$STATE/dl"
    curl -fL -o "$STATE/dl/$file" "$PICOTOOL_BASE/$file"
    check_sha "$STATE/dl/$file" "$sha" "$file"
    rm -rf "$STATE/dl/picotool-extract"
    mkdir -p "$STATE/dl/picotool-extract"
    tar -C "$STATE/dl/picotool-extract" -xzf "$STATE/dl/$file"
    mkdir -p "$(dirname "$PICOTOOL_DEST")"
    rm -rf "$PICOTOOL_DEST"
    mv "$STATE/dl/picotool-extract/picotool" "$PICOTOOL_DEST"
    record dir "$PICOTOOL_DEST" picotool
  fi
  mkdir -p "$HOME/.local/bin"
  ln -sfn "$PICOTOOL_DEST/picotool" "$PICOTOOL_LINK"
  record symlink "$PICOTOOL_LINK" "$PICOTOOL_DEST/picotool"
  act "+ installed picotool $PICOTOOL_VERSION -> ~/.local/bin/picotool"
  path_note_local_bin
}

path_note_local_bin() {
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) : ;;
    *) act "~ ~/.local/bin is not on PATH; add it (export PATH=\"\$HOME/.local/bin:\$PATH\")" ;;
  esac
}

setup_udev_advice() {
  [ "$OS" = linux ] || return 0
  if [ -f "$PICOTOOL_UDEV" ]; then
    act "= picotool udev rule present (sh2key setup-udev)"
    return 0
  fi
  if [ "$YES" = 0 ] && [ -t 0 ]; then
    say "picotool needs its own udev rule; sh2key owns that step (it asks for consent itself):"
    if "$REPO/sh2key" setup-udev; then
      if [ -f "$PICOTOOL_UDEV" ] && [ ! -L "$PICOTOOL_UDEV" ]; then
        record sh2key-udev "$PICOTOOL_UDEV" "$(sha256_of "$PICOTOOL_UDEV")"
      fi
    else
      act "~ sh2key setup-udev declined or failed; run it before first board use"
    fi
  else
    act "~ run '$REPO/sh2key setup-udev' before first board use (interactive)"
  fi
}

bridge_unit_content() {
  cat <<EOF
# Generated by seedhammer/install.sh; the in-repo copy at
# cmd/nfc-bridge/seedhammer-nfc-bridge.service is the reference.
[Unit]
Description=SeedHammer NFC bridge (local USB reader -> web editor)
After=network.target

[Service]
Environment=SH_BRIDGE_ORIGINS=$ORIGINS
ExecStart="$VENV/bin/python3" "$BRIDGE"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
}

bridge_plist_content() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$PLIST_LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$VENV/bin/python3</string>
    <string>$BRIDGE</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SH_BRIDGE_ORIGINS</key><string>$ORIGINS</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/seedhammer-nfc-bridge.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/seedhammer-nfc-bridge.log</string>
</dict>
</plist>
EOF
}

install_service() {
  if [ "$OS" = darwin ]; then
    local changed=0
    if install_file "$PLIST_PATH" 0644 0 < <(bridge_plist_content); then changed=1; fi
    if [ "$changed" = 1 ] && launchctl print "gui/$(id -u)/$PLIST_LABEL" >/dev/null 2>&1; then
      if ask_go "reload the bridge now? an in-flight tap (30s window) would be interrupted"; then
        launchctl bootout "gui/$(id -u)/$PLIST_LABEL" 2>/dev/null || true
        launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
        rm -f "$STATE/restart-pending"
        act "+ reloaded LaunchAgent $PLIST_LABEL"
      else
        mkdir -p "$STATE"
        printf '%s\n' "$PLIST_LABEL" > "$STATE/restart-pending"
        act "~ agent updated on disk; reload when idle: launchctl bootout gui/\$(id -u)/$PLIST_LABEL && launchctl bootstrap gui/\$(id -u) $PLIST_PATH && rm -f $STATE/restart-pending"
      fi
    elif ! launchctl print "gui/$(id -u)/$PLIST_LABEL" >/dev/null 2>&1; then
      launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
      rm -f "$STATE/restart-pending"
      act "+ loaded LaunchAgent $PLIST_LABEL"
    fi
    return 0
  fi
  local changed=0
  if install_file "$UNIT_PATH" 0644 0 < <(bridge_unit_content); then changed=1; fi
  if ! systemd_ok; then
    act "~ systemd user manager unavailable; unit written, enable it once systemd runs"
    if [ "$WSL" = 1 ]; then
      act "~ WSL: set [boot] systemd=true in /etc/wsl.conf, then wsl --shutdown and rerun"
    fi
    return 0
  fi
  systemctl --user daemon-reload
  if ! systemctl --user is-enabled --quiet "$UNIT_NAME" 2>/dev/null; then
    systemctl --user enable "$UNIT_NAME"
    act "+ enabled $UNIT_NAME"
  fi
  if systemctl --user is-active --quiet "$UNIT_NAME" 2>/dev/null; then
    if [ "$changed" = 1 ]; then
      if ask_go "restart the bridge now? an in-flight tap (30s window) would be interrupted"; then
        systemctl --user restart "$UNIT_NAME"
        rm -f "$STATE/restart-pending"
        act "+ restarted $UNIT_NAME with the new unit"
      else
        mkdir -p "$STATE"
        printf '%s\n' "$UNIT_NAME" > "$STATE/restart-pending"
        act "~ unit updated on disk; restart when idle: systemctl --user restart $UNIT_NAME && rm -f $STATE/restart-pending"
      fi
    elif [ -f "$STATE/restart-pending" ]; then
      # Unit unchanged and service running: clear the marker if the
      # service (re)started after it was written (manual restart, reboot).
      local started
      started=$(systemctl --user show "$UNIT_NAME" --property=ActiveEnterTimestamp --value 2>/dev/null)
      if [ -n "$started" ] \
        && [ "$(date -d "$started" +%s 2>/dev/null || echo 0)" -ge "$(stat -c %Y "$STATE/restart-pending")" ]; then
        rm -f "$STATE/restart-pending"
        act "= bridge restarted since the last unit update; cleared the pending marker"
      fi
    fi
  else
    systemctl --user start "$UNIT_NAME"
    rm -f "$STATE/restart-pending"
    act "+ started $UNIT_NAME"
  fi
  if [ "$(loginctl show-user "$USER" --property=Linger --value 2>/dev/null)" != yes ]; then
    loginctl enable-linger "$USER"
    record linger "$USER" ""
    act "+ enabled linger (bridge starts at boot, no login needed)"
  else
    act "= linger already enabled"
  fi
}

linux_logrotate() {
  command -v logrotate >/dev/null 2>&1 || { act "= logrotate not installed; skipping rotation config"; return 0; }
  local content
  content="# Installed by seedhammer/install.sh: the bridge log is append-only.
\"$HOME/.local/state/nfc-bridge.log\" {
    size 1M
    rotate 2
    missingok
    notifempty
    copytruncate
    su $USER $(id -gn)
}"
  if printf '%s\n' "$content" | cmp -s - "$LOGROTATE_PATH" 2>/dev/null; then
    install_file "$LOGROTATE_PATH" 0644 1 < <(printf '%s\n' "$content") || true
  else
    say "  will run: install -m 0644 <logrotate stanza> $LOGROTATE_PATH"
    confirm "install $LOGROTATE_PATH with root?"
    install_file "$LOGROTATE_PATH" 0644 1 < <(printf '%s\n' "$content") || true
  fi
}

darwin_newsyslog() {
  local content
  content="# Installed by seedhammer/install.sh: caps the bridge logs.
$HOME/Library/Logs/seedhammer-nfc-bridge.log    644  2  1024  *  J
$HOME/.local/state/nfc-bridge.log               644  2  1024  *  J"
  if printf '%s\n' "$content" | cmp -s - "$NEWSYSLOG_PATH" 2>/dev/null; then
    install_file "$NEWSYSLOG_PATH" 0644 1 < <(printf '%s\n' "$content") || true
  else
    say "  will run: install -m 0644 <newsyslog stanza> $NEWSYSLOG_PATH"
    confirm "install $NEWSYSLOG_PATH with root (log size cap)?"
    install_file "$NEWSYSLOG_PATH" 0644 1 < <(printf '%s\n' "$content") || true
  fi
}

# ---------------------------------------------------------------- verify

vpy() { # venv python for probes; falls back so --verify degrades honestly
  if [ -x "$VENV/bin/python3" ]; then echo "$VENV/bin/python3"; else echo python3; fi
}

verify_venv() {
  if [ -x "$VENV/bin/python3" ] && "$VENV/bin/python3" -c 'import nfc, ndef, usb1, serial' >/dev/null 2>&1; then
    report OK "venv $VENV imports nfc/ndef/usb1/serial"
  else
    report FAIL "venv $VENV missing or broken (run: ./install.sh send)"
  fi
}

verify_reader() {
  [ -x "$VENV/bin/python3" ] || return 0
  local rc=0
  "$VENV/bin/python3" - <<'PY' >/dev/null 2>&1 || rc=$?
import sys
import usb1
with usb1.USBContext() as c:
    for d in c.getDeviceList():
        if d.getVendorID() == 0x072F and d.getProductID() == 0x2200:
            sys.exit(0)
sys.exit(3)
PY
  if [ "$rc" = 3 ]; then
    if [ "$WSL" = 1 ]; then
      report PENDING "reader not visible; attach it to WSL: usbipd attach --wsl --busid <BUSID>"
    else
      report PENDING "ACR122U not plugged in; plug it and rerun --verify"
    fi
    return 0
  elif [ "$rc" != 0 ]; then
    report FAIL "usb enumeration failed (libusb missing?)"
    return 0
  fi
  rc=0
  "$VENV/bin/python3" - <<'PY' >/dev/null 2>&1 || rc=$?
import errno
import sys
import nfc
try:
    clf = nfc.ContactlessFrontend('usb')
except IOError as e:
    sys.exit(4 if e.errno == errno.EACCES else 5)
clf.close()
PY
  case $rc in
    0) report OK "reader present and opens (nfcpy)" ;;
    4) report PENDING "reader present but access denied: re-login (plugdev) or replug after the udev rule" ;;
    *) if [ "$OS" = linux ] && lsmod 2>/dev/null | grep -qE '^pn533(_usb)? '; then
         if printf '%s\n' "$MODPROBE_CONF" | cmp -s - "$MODPROBE_PATH" 2>/dev/null; then
           report PENDING "reader busy: pn533 still loaded, blacklist installed; replug the reader (or reboot)"
         else
           report FAIL "reader busy: kernel pn533 bound (run ./install.sh send to install the blacklist, then replug)"
         fi
       elif [ "$OS" = darwin ]; then
         report FAIL "reader present but won't open; a CCID daemon may hold it (see doc: ifdreader)"
       else
         report FAIL "reader present but won't open (another process holding it? a send in flight?)"
       fi ;;
  esac
}

verify_linux_system() {
  if printf '%s\n' "$UDEV_RULE" | cmp -s - "$UDEV_PATH" 2>/dev/null; then
    report OK "udev rule current: $UDEV_PATH"
  else
    report FAIL "udev rule missing or stale (headless/lingered sends will fail): ./install.sh send"
  fi
  if id -nG "$USER" | tr ' ' '\n' | grep -qx plugdev; then
    report OK "$USER in plugdev"
  elif getent group plugdev 2>/dev/null | awk -F: '{print $4}' | tr ',' '\n' | grep -qx "$USER"; then
    report PENDING "$USER added to plugdev; takes effect at next login"
  else
    report FAIL "$USER not in plugdev: ./install.sh send"
  fi
  if have_active pcscd || have_active pcscd.socket; then
    report FAIL "pcscd active; it claims the ACR122U (sudo systemctl disable --now pcscd.socket pcscd)"
  fi
  if recorded modprobe "$MODPROBE_PATH" && [ ! -f "$MODPROBE_PATH" ]; then
    report FAIL "pn533 blacklist recorded but missing: ./install.sh send"
  fi
  if command -v logrotate >/dev/null 2>&1 && recorded file "$LOGROTATE_PATH" && [ ! -f "$LOGROTATE_PATH" ]; then
    report FAIL "logrotate config recorded but missing: ./install.sh send"
  fi
  if ! systemd_ok; then
    report PENDING "systemd user manager unavailable; bridge unit cannot run yet"
    return 0
  fi
  if bridge_unit_content | cmp -s - "$UNIT_PATH" 2>/dev/null; then
    report OK "bridge unit current: $UNIT_PATH"
  else
    report PENDING "bridge unit missing or differs from this script's target: ./install.sh send"
  fi
  if systemctl --user is-active --quiet "$UNIT_NAME" 2>/dev/null; then
    report OK "bridge service active"
  else
    report PENDING "bridge service not active: systemctl --user start $UNIT_NAME"
  fi
  if [ -f "$STATE/restart-pending" ]; then
    report PENDING "bridge unit updated but the running process predates it: systemctl --user restart $UNIT_NAME"
  fi
  if [ "$(loginctl show-user "$USER" --property=Linger --value 2>/dev/null)" = yes ]; then
    report OK "linger enabled (bridge survives logout)"
  else
    report PENDING "linger off; bridge only runs while logged in (loginctl enable-linger)"
  fi
}

verify_darwin_system() {
  if bridge_plist_content | cmp -s - "$PLIST_PATH" 2>/dev/null; then
    report OK "LaunchAgent current: $PLIST_PATH"
  else
    report PENDING "LaunchAgent missing or differs: ./install.sh send"
  fi
  if launchctl print "gui/$(id -u)/$PLIST_LABEL" >/dev/null 2>&1; then
    report OK "LaunchAgent loaded"
  else
    report PENDING "LaunchAgent not loaded: launchctl bootstrap gui/\$(id -u) $PLIST_PATH"
  fi
  if [ -f "$STATE/restart-pending" ]; then
    report PENDING "agent updated but the running process predates it: launchctl bootout gui/\$(id -u)/$PLIST_LABEL && launchctl bootstrap gui/\$(id -u) $PLIST_PATH"
  fi
  if recorded file "$NEWSYSLOG_PATH" && [ ! -f "$NEWSYSLOG_PATH" ]; then
    report FAIL "newsyslog config recorded but missing: ./install.sh send"
  fi
}

verify_bridge_http() {
  local out rc=0
  out=$(curl -fsS --max-time 3 http://127.0.0.1:8787/bridge/health 2>/dev/null) || rc=$?
  if [ "$rc" != 0 ]; then
    report PENDING "bridge not answering on 127.0.0.1:8787 (service not running yet?)"
    return 0
  fi
  # All bridge responses are HTTP 200; only the JSON status field is truth.
  if printf '%s' "$out" | "$(vpy)" -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("ok") else 1)' 2>/dev/null; then
    report OK "bridge health ok on 127.0.0.1:8787"
  else
    report FAIL "bridge answered but not ok: $out"
  fi
}

verify_tools() {
  local t
  for t in sh2key svgplate; do
    if [ -x "$REPO/$t" ]; then report OK "$REPO/$t built"; else report FAIL "$REPO/$t missing: ./install.sh send"; fi
  done
  if picotool_ok; then
    report OK "picotool: $(picotool version 2>/dev/null | head -1)"
  elif [ -x "$PICOTOOL_LINK" ]; then
    report PENDING "picotool installed at ~/.local/bin but not on PATH"
  else
    report FAIL "picotool >= 2.0 not found: ./install.sh send"
  fi
  if [ "$OS" = linux ] && [ ! -f "$PICOTOOL_UDEV" ]; then
    report PENDING "picotool udev rule not installed: ./sh2key setup-udev (before first board use)"
  fi
}

verify_all() {
  say ""
  say "verify (note: the reader probe claims the device for a moment; avoid it during an active send):"
  verify_venv
  verify_reader
  if [ "$OS" = linux ]; then verify_linux_system; else verify_darwin_system; fi
  verify_bridge_http
  verify_tools
  say ""
  if [ "$FAILS" -gt 0 ]; then
    say "verify: $FAILS failure(s), $PENDINGS pending. Fix the FAIL lines above."
    exit 1
  fi
  if [ "$PENDINGS" -gt 0 ]; then
    say "verify: no failures; $PENDINGS pending item(s) need a replug, re-login, or the missing hardware."
  else
    say "verify: everything checks out."
  fi
  say "final check (stage 2): send one real payload (Studio Send or cmd/textplate/write-nfc.py) after any replug/re-login."
}

# --------------------------------------------------------------- profiles

profile_send() {
  say "SeedHammer send-stack install (repo: $REPO)"
  [ "$WSL" = 1 ] && say "WSL detected: the reader must be attached with usbipd (see --verify output)."
  say ""
  if [ "$OS" = linux ]; then
    linux_packages
    pcscd_check
  else
    command -v python3 >/dev/null 2>&1 || fail "python3 not found; install the Xcode command line tools or brew python"
  fi
  pick_python
  ensure_go
  ensure_venv
  if [ "$OS" = linux ]; then
    linux_udev
    linux_modprobe
  fi
  build_tools
  ensure_picotool
  setup_udev_advice
  install_service
  if [ "$OS" = linux ]; then linux_logrotate; else darwin_newsyslog; fi
  verify_all
}

profile_dev() {
  cat <<EOF
Firmware development uses Nix; this script deliberately does not
reimplement it (flake.nix is the pinned toolchain: tinygo, gcc-arm,
openocd for rp2350, pioasm, picotool):

  nix develop                    the dev shell
  nix run .#build-firmware       reproducible UF2 (VERSION=<sha> matches CI)
  nix run .#flash-firmware       flash over cmsis-dap — UNSIGNED image: a board
                                 with secure boot enforced (since 2026-07-12)
                                 will refuse to boot it. The signed path is
                                 build-firmware, then sh2key sign + picotool
                                 load -x; docs/manual-sh2key.md owns the ceremony.
  ./sh2key setup-udev            picotool device access on Linux (once)

The send stack is separate: ./install.sh send
No changes were made.
EOF
}

# -------------------------------------------------------------- uninstall

uninstall_service_stop() {
  if [ "$OS" = darwin ]; then
    if recorded file "$PLIST_PATH"; then
      launchctl bootout "gui/$(id -u)/$PLIST_LABEL" 2>/dev/null || true
    fi
  elif systemd_ok && recorded file "$UNIT_PATH"; then
    systemctl --user disable --now "$UNIT_NAME" 2>/dev/null || true
    act "+ stopped and disabled $UNIT_NAME"
  fi
}

safe_path() {
  case $1 in
    */../*|*/..|*/./*|*/.|*/|*//*|'') return 1 ;;  # no traversal, trailing slash, //, empty
  esac
  case $1 in
    "$UDEV_PATH"|"$MODPROBE_PATH"|"$LOGROTATE_PATH"|"$NEWSYSLOG_PATH"|"$PICOTOOL_UDEV") return 0 ;;
    "$UNIT_PATH"|"$PLIST_PATH"|"$PICOTOOL_LINK"|"$HOME"/.local/go|"$VENV") return 0 ;;
    "$REPO"/sh2key|"$REPO"/svgplate) return 0 ;;
    "$HOME"/.local/opt/picotool-*)
      case ${1#"$HOME"/.local/opt/} in */*) return 1 ;; *) return 0 ;; esac ;;
    *) return 1 ;;
  esac
}

rm_path() { # file or dir, root when needed
  case $1 in
    /etc/*) as_root rm -rf "$1" ;;
    *) rm -rf "$1" ;;
  esac
}

mark_kept() { KEPT_PATHS="$KEPT_PATHS
$1"; }

was_kept() { printf '%s\n' "$KEPT_PATHS" | grep -qxF -- "$1"; }

uninstall_file_record() { # path extra shared(0/1)
  local path=$1 extra=$2 shared=$3
  local sha=$extra
  safe_path "$path" || { report FAIL "refusing unexpected manifest path: $path"; return; }
  was_kept "$path" && { act "~ kept $path (already declined this run)"; return; }
  if [ -L "$path" ]; then
    report FAIL "$path is now a symlink; refusing to touch it"
    mark_kept "$path"
    return
  fi
  [ -e "$path" ] || { act "= already gone $path"; return; }
  case $extra in adopted:*) sha=${extra#adopted:} ;; esac
  if [ "$shared" = 1 ]; then
    if ! ask_remove "remove $path (shared system config other software may rely on)?"; then
      act "~ kept $path"
      mark_kept "$path"
      return
    fi
  elif [ "${extra%%:*}" = adopted ]; then
    if ! ask_remove "$path predates this install (adopted); remove it?"; then
      act "~ kept (pre-existing) $path"
      mark_kept "$path"
      return
    fi
  elif [ "$extra" != built ] && [ -n "$sha" ] && [ "$(sha256_of "$path")" != "$sha" ]; then
    if ! ask_remove "$path was modified since install; remove anyway?"; then
      act "~ kept (modified) $path"
      mark_kept "$path"
      return
    fi
  fi
  rm_path "$path"
  act "+ removed $path"
  case $path in
    /etc/udev/rules.d/*) as_root udevadm control --reload-rules 2>/dev/null || true ;;
  esac
}

do_uninstall() {
  [ -f "$MANIFEST" ] || fail "no manifest at $MANIFEST; nothing recorded to uninstall"
  say "uninstall will act only on what the manifest records:"
  sed 's/^/    /' "$MANIFEST"
  confirm "proceed with uninstall?"
  uninstall_service_stop
  local kind path extra
  while IFS="$(printf '\t')" read -r kind path extra; do
    case $kind in
      file)
        uninstall_file_record "$path" "$extra" 0
        ;;
      modprobe)
        uninstall_file_record "$path" "$extra" 1
        ;;
      sh2key-udev)
        if ask_remove "remove $path (installed via sh2key setup-udev; sh2key can reinstall it)?"; then
          uninstall_file_record "$path" "$extra" 0
        else
          act "~ kept $path"
          mark_kept "$path"
        fi
        ;;
      replaced)
        safe_path "$path" || { report FAIL "refusing unexpected manifest path: $path"; continue; }
        was_kept "$path" && { act "~ not restoring over kept $path"; continue; }
        [ "$extra" = "$(printf '%s' "$path" | tr '/' '_')" ] \
          || { report FAIL "backup name mismatch for $path; not restoring"; continue; }
        [ -f "$BACKUPS/$extra" ] || { report FAIL "backup missing for $path"; continue; }
        if [ -L "$path" ]; then report FAIL "$path is now a symlink; not restoring over it"; continue; fi
        case $path in
          /etc/*) as_root install -m 0644 "$BACKUPS/$extra" "$path" ;;
          *) install -m 0644 "$BACKUPS/$extra" "$path" ;;
        esac
        act "+ restored prior $path"
        ;;
      symlink)
        safe_path "$path" || { report FAIL "refusing unexpected manifest path: $path"; continue; }
        was_kept "$path" && { act "~ kept $path (already declined this run)"; continue; }
        if [ -L "$path" ]; then
          if [ -z "$extra" ] || [ "$(readlink "$path")" = "$extra" ]; then
            rm -f "$path"; act "+ removed $path"
          else
            report FAIL "$path now points at $(readlink "$path"), not $extra; leaving it"
            mark_kept "$path"
          fi
        elif [ -e "$path" ]; then report FAIL "$path is not a symlink anymore; leaving it"; mark_kept "$path"
        else act "= already gone $path"; fi
        ;;
      dir)
        safe_path "$path" || { report FAIL "refusing unexpected manifest path: $path"; continue; }
        was_kept "$path" && { act "~ kept $path (already declined this run)"; continue; }
        [ -e "$path" ] || { act "= already gone $path"; continue; }
        if [ -L "$path" ]; then report FAIL "$path is now a symlink; leaving it"; mark_kept "$path"; continue; fi
        case $extra in
          adopted:*)
            if ! ask_remove "$path predates this install (adopted); remove it?"; then
              act "~ kept (pre-existing) $path"; mark_kept "$path"; continue
            fi ;;
        esac
        rm -rf "$path"; act "+ removed $path"
        ;;
      pkg)
        act "~ kept system packages via $path:$extra (shared; remove yourself if unwanted)"
        ;;
      venv)
        [ "$path" = "$VENV" ] || { report FAIL "unexpected venv path: $path"; continue; }
        [ -e "$VENV" ] || { act "= already gone $VENV"; continue; }
        rm -rf "$VENV"; act "+ removed venv $VENV"
        ;;
      group-member)
        if ask_remove "remove $extra from group $path (shared with other software)?"; then
          as_root gpasswd -d "$extra" "$path"; act "+ removed $extra from $path"
        else
          act "~ kept $extra in $path"
        fi
        ;;
      groupadd)
        act "~ kept group $path (shared; remove yourself with: sudo groupdel $path)"
        ;;
      linger)
        if ask_remove "disable linger for $path (other user services would stop surviving logout)?"; then
          loginctl disable-linger "$path"; act "+ disabled linger"
        else
          act "~ kept linger enabled"
        fi
        ;;
      brew)
        act "~ kept brew formula $path (shared; remove yourself with: brew uninstall $path)"
        ;;
      *)
        report FAIL "unknown manifest kind: $kind $path"
        ;;
    esac
  done < <(awk -F'\t' '$1 != "replaced" {a[NR]=$0} $1 == "replaced" {b[NR]=$0}
           END { for (i = NR; i > 0; i--) if (i in a) print a[i]
                 for (i = NR; i > 0; i--) if (i in b) print b[i] }' "$MANIFEST")
  if [ "$FAILS" -gt 0 ]; then
    say "uninstall finished with $FAILS problem(s); manifest kept at $MANIFEST"
    exit 1
  fi
  if [ -n "$(printf '%s' "$KEPT_PATHS" | tr -d '\n')" ]; then
    say "uninstall complete; some paths were kept, so the manifest and backups stay at $STATE"
  else
    rm -rf "$STATE"
    say "uninstall complete; manifest and backups removed."
  fi
}

# ------------------------------------------------------------------ main

usage() { awk 'NR == 1 {next} /^set -euo/ {exit} {sub(/^# ?/, ""); print}' "$0"; }

PROFILE=send
MODE=install
for arg in "$@"; do
  case $arg in
    send|dev) PROFILE=$arg ;;
    --verify) MODE=verify ;;
    --uninstall) MODE=uninstall ;;
    --yes) YES=1 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $arg (see --help)" ;;
  esac
done

detect_os
command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 \
  || fail "need sha256sum or shasum on PATH"

case $MODE in
  verify) verify_all ;;
  uninstall) do_uninstall ;;
  install)
    case $PROFILE in
      send) profile_send ;;
      dev) profile_dev ;;
    esac ;;
esac
