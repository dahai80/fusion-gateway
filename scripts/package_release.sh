#!/bin/bash
# package_release.sh — cross-compile fusion-gateway and build native release
# packages (#647): macOS .dmg (arm64), Windows .exe (amd64), Debian .deb
# (amd64 + arm64). Outputs land in dist/release-pkg/ alongside sha256 checksums.
#
# Usage: scripts/package_release.sh vX.Y.Z [output-dir]
# Requires: go, hdiutil (macOS), dpkg-deb (brew install dpkg), ar/gzip.
set -euo pipefail

VERSION="${1:?usage: package_release.sh vX.Y.Z [output-dir]}"
OUT="${2:-dist/release-pkg}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

cd "$REPO_ROOT"
mkdir -p "$OUT"
LDFLAGS="-s -w -X main.version=${VERSION}"

log() { printf '[package] %s\n' "$*"; }

# ---------------------------------------------------------------- binaries
log "cross-compiling binaries"
for target in darwin/arm64 windows/amd64 linux/amd64 linux/arm64; do
    goos="${target%%/*}"; goarch="${target##*/}"
    bin="fusion-gateway"
    [ "$goos" = "windows" ] && bin="fusion-gateway.exe"
    log "  building $goos/$goarch"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/$goos-$goarch-$bin" ./cmd/gateway
done

# ---------------------------------------------------------------- DMG (macOS)
dmg_dir="$STAGE/dmg-root"
mkdir -p "$dmg_dir"
cp "$STAGE/darwin-arm64-fusion-gateway" "$dmg_dir/fusion-gateway"
{
    echo "fusion-gateway ${VERSION}"
    echo ""
    echo "Hybrid inference gateway for Apple Silicon local inference + cloud LLMs."
    echo ""
    echo "Run: ./fusion-gateway --config config.yaml   (listens on :11432)"
    echo "Docs: https://github.com/dahai80/fusion-gateway"
} > "$dmg_dir/README.txt"
log "building DMG"
hdiutil create -volname "fusion-gateway ${VERSION}" -srcfolder "$dmg_dir" \
    -format UDZO -ov -quiet "$OUT/fusion-gateway-${VERSION}-darwin-arm64.dmg"

# ---------------------------------------------------------------- exe (Windows)
log "packaging windows exe"
cp "$STAGE/windows-amd64-fusion-gateway.exe" \
    "$OUT/fusion-gateway-${VERSION}-windows-amd64.exe"

# ---------------------------------------------------------------- deb (Ubuntu)
build_deb() {
    local goarch="$1" debarch="$2"
    local root="$STAGE/deb-$debarch"
    mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/etc/fusion-gateway" \
        "$root/usr/share/doc/fusion-gateway" "$root/lib/systemd/system"
    cp "$STAGE/linux-$goarch-fusion-gateway" "$root/usr/bin/fusion-gateway"
    cp config.container.yaml "$root/etc/fusion-gateway/config.yaml.example"
    {
        echo "Package: fusion-gateway"
        echo "Version: ${VERSION#v}"
        echo "Section: net"
        echo "Priority: optional"
        echo "Architecture: $debarch"
        echo "Maintainer: Fusion <dahai@fusion.local>"
        echo "Description: Hybrid inference gateway for Apple Silicon local inference + cloud LLMs"
        echo " Unified OpenAI v1-compatible gateway (:11432) routing between local"
        echo " MLX engines and cloud LLM backends with hardware-aware routing."
    } > "$root/DEBIAN/control"
    cat > "$root/lib/systemd/system/fusion-gateway.service" <<'UNIT'
[Unit]
Description=Fusion Gateway (hybrid inference gateway)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/fusion-gateway --config /etc/fusion-gateway/config.yaml
Restart=on-failure
RestartSec=5
User=fusion
StateDirectory=fusion-gateway

[Install]
WantedBy=multi-user.target
UNIT
    log "  building deb ($debarch)"
    dpkg-deb --build --root-owner-group "$root" \
        "$OUT/fusion-gateway_${VERSION#v}_linux-$debarch.deb"
}
build_deb amd64 amd64
build_deb arm64 arm64

# ---------------------------------------------------------------- checksums
log "checksums"
cd "$OUT"
shasum -a 256 fusion-gateway-"${VERSION}"-darwin-arm64.dmg \
    fusion-gateway-"${VERSION}"-windows-amd64.exe \
    fusion-gateway_"${VERSION#v}"_linux-amd64.deb \
    fusion-gateway_"${VERSION#v}"_linux-arm64.deb \
    > native-checksums.txt

log "done:"
ls -lh "$REPO_ROOT/$OUT" | awk 'NR>1 {print "  " $5 "  " $9}'
