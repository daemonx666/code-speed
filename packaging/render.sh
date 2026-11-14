#!/bin/sh
# Fills the package manifests in packaging/ with a release's version and
# checksums: render.sh v1.2.3 path/to/SHA256SUMS out-dir
set -eu
tag=$1 sums=$2 out=$3
version=${tag#v}
here=$(cd "$(dirname "$0")" && pwd)

sum() {
  s=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")
  if [ -z "$s" ]; then echo "render.sh: $1 is not in $sums" >&2; exit 1; fi
  echo "$s"
}
p=code-speed-$tag
mac=$(sum "$p-macos-universal.zip")
wza=$(sum "$p-windows-amd64-portable.zip")
wzr=$(sum "$p-windows-arm64-portable.zip")
wsa=$(sum "$p-windows-amd64-setup.exe")
wsr=$(sum "$p-windows-arm64-setup.exe")
la=$(sum "$p-linux-amd64.tar.gz")
lr=$(sum "$p-linux-arm64.tar.gz")

mkdir -p "$out/homebrew" "$out/scoop" "$out/winget" "$out/aur"
for f in homebrew/code-speed.rb scoop/code-speed.json winget/daemonx666.code-speed.yaml \
  winget/daemonx666.code-speed.installer.yaml winget/daemonx666.code-speed.locale.en-US.yaml \
  aur/PKGBUILD aur/.SRCINFO; do
  sed -e "s/@VERSION@/$version/g" -e "s/@SHA_MAC@/$mac/g" \
    -e "s/@SHA_WIN_AMD64_ZIP@/$wza/g" -e "s/@SHA_WIN_ARM64_ZIP@/$wzr/g" \
    -e "s/@SHA_WIN_AMD64_SETUP@/$wsa/g" -e "s/@SHA_WIN_ARM64_SETUP@/$wsr/g" \
    -e "s/@SHA_LINUX_AMD64@/$la/g" -e "s/@SHA_LINUX_ARM64@/$lr/g" \
    "$here/$f" > "$out/$f"
done
if grep -rq "@[A-Z_0-9]*@" "$out"; then echo "render.sh: a placeholder was left" >&2; exit 1; fi
echo "rendered $tag into $out"
