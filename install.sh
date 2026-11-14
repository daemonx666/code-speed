#!/bin/sh
# Install code-speed: curl -fsSL https://raw.githubusercontent.com/daemonx666/code-speed/main/install.sh | sh
#
# macOS     the app (one universal build for Apple silicon and Intel) into
#           /Applications, or ~/Applications when that isn't writable
# Linux     the desktop app: the .deb with apt, the .rpm with dnf, zypper or
#           yum, else the binary into ~/.local/bin; without a desktop
#           session, the terminal build
# Windows   not here: winget install daemonx666.code-speed, or the setup
#           from the releases page
#
# Options:
#   --cli             the terminal build only (no window, no tray)
#   --gui             the desktop app even without a desktop session
#   --version vX.Y.Z  that release instead of the newest
#   --no-open         don't start the app after installing it (macOS)
#   --uninstall       remove what this script installed; --purge also
#                     removes code-speed's own settings and history
#
# Every download is checked against the release's SHA256SUMS, and that file
# against its signature (with OpenSSL 3) where the system has it.
set -eu

REPO=daemonx666/code-speed
BASE=${CODE_SPEED_RELEASES:-https://github.com/$REPO/releases}
BUNDLE_ID=io.github.daemonx666.code-speed
# the release key's public half (internal/update/signed.go), as PEM
PUBKEY='MCowBQYDK2VwAyEAIUE+njZOASGPIdCkKI+Mzk1/SWFMwPh7Xm5hhuA1b/o='

usage() {
  cat <<'EOF'
Install code-speed.  curl -fsSL https://raw.githubusercontent.com/daemonx666/code-speed/main/install.sh | sh -s -- [options]

  --cli             the terminal build only (no window, no tray)
  --gui             the desktop app even without a desktop session (Linux)
  --version vX.Y.Z  that release instead of the newest
  --no-open         don't start the app after installing it (macOS)
  --uninstall       remove code-speed; --purge also removes its settings and history
EOF
}

mode=auto version='' open_app=1 uninstall=0 purge=0
while [ $# -gt 0 ]; do
  case $1 in
    --cli) mode=cli ;;
    --gui) mode=gui ;;
    --version) [ $# -ge 2 ] || { echo "--version needs a value, e.g. v0.8.0" >&2; exit 2; }; version=$2; shift ;;
    --version=*) version=${1#--version=} ;;
    --no-open) open_app=0 ;;
    --uninstall) uninstall=1 ;;
    --purge) uninstall=1 purge=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '%s\n' "$*"; }
die() { printf 'code-speed install: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# run as root where needed: directly when we are, through sudo otherwise
asroot() {
  if [ "$(id -u)" = 0 ]; then "$@"
  elif has sudo; then sudo "$@"
  else return 1
  fi
}

fetch() { # url file
  if has curl; then curl -fsSL --retry 2 -o "$2" "$1"
  elif has wget; then wget -q -O "$2" "$1"
  else die "needs curl or wget"
  fi
}

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW*|MSYS*|CYGWIN*) die "on Windows, run: winget install daemonx666.code-speed (or the setup from $BASE)" ;;
  *) die "$(uname -s) isn't supported" ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "$(uname -m) isn't supported: macOS, Linux and Windows on x64 or arm64 are" ;;
esac

bindir() {
  if [ "$(id -u)" = 0 ]; then echo /usr/local/bin; else echo "$HOME/.local/bin"; fi
}

# --- uninstall -----------------------------------------------------------------

if [ $uninstall = 1 ]; then
  # put Claude Code's own status line back first, while code-speed is here
  for b in "$(command -v code-speed 2>/dev/null || true)" "${CODE_SPEED_APPS:-/Applications}/code-speed.app/Contents/MacOS/code-speed" \
    "$HOME/Applications/code-speed.app/Contents/MacOS/code-speed"; do
    if [ -n "$b" ] && [ -x "$b" ]; then "$b" claude-quota off >/dev/null 2>&1 || true; break; fi
  done
  if [ $os = darwin ]; then
    osascript -e "tell application id \"$BUNDLE_ID\" to quit" >/dev/null 2>&1 || true
    rm -f "$HOME/Library/LaunchAgents/$BUNDLE_ID.plist"
    for d in "${CODE_SPEED_APPS:-/Applications}/code-speed.app" "$HOME/Applications/code-speed.app"; do
      [ -d "$d" ] && { rm -rf "$d" 2>/dev/null || asroot rm -rf "$d"; } && say "removed $d"
    done
    rm -f "$(bindir)/code-speed" "$HOME/.local/bin/code-speed" # the terminal build
    [ $purge = 1 ] && rm -rf "$HOME/Library/Application Support/code-speed" \
      "$HOME/Library/Caches/$BUNDLE_ID" "$HOME/Library/WebKit/$BUNDLE_ID" "$HOME/Library/HTTPStorages/$BUNDLE_ID"
  else
    pkill -x code-speed >/dev/null 2>&1 || true
    if has dpkg && dpkg -s code-speed >/dev/null 2>&1; then asroot apt-get remove -y code-speed || asroot dpkg -r code-speed
    elif has rpm && rpm -q code-speed >/dev/null 2>&1; then
      if has dnf; then asroot dnf remove -y code-speed; elif has zypper; then asroot zypper -n remove code-speed; else asroot rpm -e code-speed; fi
    fi
    rm -f "$(bindir)/code-speed" "$HOME/.local/bin/code-speed" "$HOME/.local/share/applications/code-speed.desktop" \
      "$HOME/.local/share/icons/hicolor/1024x1024/apps/code-speed.png" "${XDG_CONFIG_HOME:-$HOME/.config}/autostart/code-speed.desktop"
    [ $purge = 1 ] && rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/code-speed"
  fi
  if [ $purge = 1 ]; then say "code-speed is removed, with its settings and history."
  else say "code-speed is removed. Its settings and history stay (--purge removes them)."
  fi
  exit 0
fi

# --- which build ---------------------------------------------------------------

if [ $mode = auto ]; then
  mode=gui
  if [ $os = linux ] && [ -z "${DISPLAY:-}" ] && [ -z "${WAYLAND_DISPLAY:-}" ]; then
    mode=cli
    say "No desktop session here: installing the terminal build (--gui for the app)."
  fi
fi

if [ -z "$version" ]; then
  # the newest release: where /releases/latest redirects to
  if has curl; then
    url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE/latest") || die "couldn't reach $BASE"
  else
    url=$(wget -S --spider "$BASE/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -1)
  fi
  version=${url##*/}
fi
case $version in
  v[0-9]*) ;;
  [0-9]*) version=v$version ;;
  *) die "no release found (got \"$version\")" ;;
esac
num=${version#v}
say "code-speed $version for $os/$arch"

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t code-speed)
trap 'rm -rf "$tmp"' EXIT INT TERM

sha256() {
  if has sha256sum; then sha256sum "$1" | cut -d' ' -f1
  elif has shasum; then shasum -a 256 "$1" | cut -d' ' -f1
  elif has openssl; then openssl dgst -sha256 "$1" | sed 's/.*= *//'
  else die "needs sha256sum, shasum or openssl to check the download"
  fi
}

fetch "$BASE/download/$version/SHA256SUMS" "$tmp/SHA256SUMS" || die "couldn't download SHA256SUMS of $version"
# the signature: checked where OpenSSL 3 can (LibreSSL, macOS's own, can't)
if fetch "$BASE/download/$version/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig" 2>/dev/null; then
  if has openssl && openssl version 2>/dev/null | grep -q '^OpenSSL [3-9]'; then
    printf -- '-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----\n' "$PUBKEY" > "$tmp/key.pem"
    openssl base64 -d -A -in "$tmp/SHA256SUMS.sig" -out "$tmp/sig.bin"
    openssl pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$tmp/SHA256SUMS" -sigfile "$tmp/sig.bin" >/dev/null 2>&1 ||
      die "SHA256SUMS isn't signed by the release key: not installing"
    say "  SHA256SUMS: signature checked"
  else
    say "  SHA256SUMS: signature not checked (needs OpenSSL 3); checksums still are"
  fi
else
  say "  SHA256SUMS: this release has no signature; checksums still are checked"
fi

get() { # file → $tmp/file, checked
  fetch "$BASE/download/$version/$1" "$tmp/$1" || die "couldn't download $1"
  want=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
  [ -n "$want" ] || die "$1 is not in SHA256SUMS"
  [ "$(sha256 "$tmp/$1")" = "$want" ] || die "$1 doesn't match its checksum: not installing"
}

in_path() {
  case ":$PATH:" in *":$1:"*) return 0 ;; esac
  say "  $1 is not on your PATH; add it: export PATH=\"$1:\$PATH\""
}

# --- the terminal build ---------------------------------------------------------

install_cli() {
  name=code-speed-cli-$version-$os-$arch
  get "$name.tar.gz"
  tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
  dir=$(bindir)
  mkdir -p "$dir"
  install -m 755 "$tmp/$name/code-speed" "$dir/code-speed"
  say "Installed $dir/code-speed. Try: code-speed today"
  in_path "$dir"
}

# --- macOS ------------------------------------------------------------------------

install_mac() {
  file=code-speed-$version-macos-universal.zip
  get "$file"
  ditto -x -k "$tmp/$file" "$tmp/app"
  [ -d "$tmp/app/code-speed.app" ] || die "the download has no code-speed.app"
  dest=${CODE_SPEED_APPS:-/Applications}
  [ -w "$dest" ] || { dest=$HOME/Applications; mkdir -p "$dest"; }
  osascript -e "tell application id \"$BUNDLE_ID\" to quit" >/dev/null 2>&1 || true
  rm -rf "$dest/code-speed.app"
  ditto "$tmp/app/code-speed.app" "$dest/code-speed.app"
  # downloaded by this script, not a browser: nothing to unquarantine, but
  # an earlier copy may have carried the flag over
  xattr -dr com.apple.quarantine "$dest/code-speed.app" 2>/dev/null || true
  say "Installed $dest/code-speed.app (Apple silicon and Intel)."
  say "  In a terminal: $dest/code-speed.app/Contents/MacOS/code-speed today"
  [ $open_app = 1 ] && open "$dest/code-speed.app" && say "  Started: look for it in the menu bar."
  return 0
}

# --- Linux desktop app -------------------------------------------------------------

webkit_missing() {
  ! { has ldconfig && ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4.1'; } &&
    ! ls /usr/lib*/libwebkit2gtk-4.1.so* /usr/lib/*/libwebkit2gtk-4.1.so* >/dev/null 2>&1
}

install_linux_gui() {
  if has apt-get && has dpkg; then
    file=code-speed_${num}_$arch.deb
    get "$file"
    if asroot apt-get install -y "$tmp/$file"; then say "Installed code-speed (with apt). Start it from your apps, or run: code-speed"; return 0; fi
    say "  apt couldn't install it; installing into your home folder instead"
  elif has rpm && { has dnf || has zypper || has yum; }; then
    case $arch in amd64) rarch=x86_64 ;; arm64) rarch=aarch64 ;; esac
    file=code-speed-$num-1.$rarch.rpm
    get "$file"
    ok=1
    if has dnf; then asroot dnf install -y "$tmp/$file" || ok=0
    elif has zypper; then asroot zypper -n install --allow-unsigned-rpm "$tmp/$file" || ok=0
    else asroot yum install -y "$tmp/$file" || ok=0
    fi
    if [ $ok = 1 ]; then say "Installed code-speed. Start it from your apps, or run: code-speed"; return 0; fi
    say "  the package manager couldn't install it; installing into your home folder instead"
  fi
  # the binary in the home folder: no root, but the libraries are the user's to add
  file=code-speed-$version-linux-$arch.tar.gz
  get "$file"
  mkdir -p "$tmp/gui"
  tar -xzf "$tmp/$file" -C "$tmp/gui"
  dir=$HOME/.local/bin
  mkdir -p "$dir" "$HOME/.local/share/applications" "$HOME/.local/share/icons/hicolor/1024x1024/apps"
  install -m 755 "$tmp/gui/code-speed" "$dir/code-speed"
  install -m 644 "$tmp/gui/code-speed.png" "$HOME/.local/share/icons/hicolor/1024x1024/apps/code-speed.png"
  sed "s|^Exec=.*|Exec=$dir/code-speed|" "$tmp/gui/build/linux/code-speed.desktop" > "$HOME/.local/share/applications/code-speed.desktop"
  say "Installed $dir/code-speed and its menu entry."
  in_path "$dir"
  if webkit_missing; then
    say "  It needs GTK 3 and WebKitGTK 4.1, which this system doesn't seem to have:"
    say "  Debian/Ubuntu: sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0"
    say "  Fedora: sudo dnf install gtk3 webkit2gtk4.1    Arch: sudo pacman -S webkit2gtk-4.1"
    say "  Or use the terminal build: rerun with --cli"
  fi
}

case $mode/$os in
  cli/*) install_cli ;;
  gui/darwin) install_mac ;;
  gui/linux) install_linux_gui ;;
esac
