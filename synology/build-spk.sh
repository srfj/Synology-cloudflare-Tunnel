#!/bin/sh
# Build a Synology DSM 6.2.4 .spk package for cloudflared from this source tree.
#
# Usage:
#   ./synology/build-spk.sh                      # x86_64 (amd64) build
#   GOARCH_TARGET=arm64 OUT_NAME=armv8 \
#     SPK_ARCH="rtd1296 armv8" ./synology/build-spk.sh
#
# Requires: go toolchain, tar, gzip, md5sum.

set -e

PKG_NAME="cloudflared"
PKG_VERSION="${PKG_VERSION:-2026.9.29}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"
SPK_ARCH="${SPK_ARCH:-apollolake avoton braswell broadwell broadwellnk bromolow cedarview denverton grantley purley v1000 geminilake x86_64}"
OUT_NAME="${OUT_NAME:-x86_64}"
OS_MIN_VER="${OS_MIN_VER:-6.2-00000}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${BUILD_DIR:-/tmp/cloudflared-spk-${OUT_NAME}}"
STAGING_DIR="${BUILD_DIR}/staging"
SPK_ROOT="${BUILD_DIR}/spk"
OUT_FILE="${REPO_DIR}/${PKG_NAME}-${PKG_VERSION}-dsm6.2.4-${OUT_NAME}.spk"

rm -rf "${BUILD_DIR}"
mkdir -p "${STAGING_DIR}/bin" "${SPK_ROOT}/scripts"

echo "==> Building cloudflared ${PKG_VERSION} for linux/${GOARCH_TARGET} (CGO disabled)"
(
    cd "${REPO_DIR}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH_TARGET}" \
        go build -mod=readonly -trimpath -tags "osusergo netgo" \
        -ldflags="-s -w -X main.Version=${PKG_VERSION} -X main.BuildTime=2026-10-04-00:00-UTC" \
        -o "${STAGING_DIR}/bin/cloudflared" ./cmd/cloudflared
)
chmod 755 "${STAGING_DIR}/bin/cloudflared"

echo "==> Creating package.tgz"
( cd "${STAGING_DIR}" && tar -cf - . | gzip -n > "${SPK_ROOT}/package.tgz" )
chmod 644 "${SPK_ROOT}/package.tgz"

echo "==> Copying installer scripts"
for f in preinst postinst preuninst postuninst start-stop-status; do
    if [ -f "${SCRIPT_DIR}/scripts/${f}" ]; then
        cp "${SCRIPT_DIR}/scripts/${f}" "${SPK_ROOT}/scripts/${f}"
        chmod 755 "${SPK_ROOT}/scripts/${f}"
    fi
done

echo "==> Writing INFO"
CHECKSUM="$(md5sum "${SPK_ROOT}/package.tgz" | cut -d' ' -f1)"
cat > "${SPK_ROOT}/INFO" <<EOF
package="${PKG_NAME}"
version="${PKG_VERSION}"
displayname="Cloudflare Tunnel"
maintainer="Cloudflare"
maintainer_url="https://github.com/cloudflare/cloudflared"
distributor="Cloudflare"
distributor_url="https://github.com/cloudflare/cloudflared"
description="cloudflared is the Cloudflare Tunnel client. It creates secure, outbound-only connections from this NAS to Cloudflare's edge so local services can be published without opening inbound firewall ports. Static build for DSM 6.2.4 (${OUT_NAME})."
arch="${SPK_ARCH}"
os_min_ver="${OS_MIN_VER}"
thirdparty="yes"
silent_install="no"
silent_uninstall="no"
silent_upgrade="yes"
dsmappname="com.cloudflare.cloudflared"
checksum="${CHECKSUM}"
EOF
chmod 644 "${SPK_ROOT}/INFO"

echo "==> Assembling $(basename "${OUT_FILE}")"
rm -f "${OUT_FILE}"
( cd "${SPK_ROOT}" && tar -cf "${OUT_FILE}" INFO package.tgz scripts )

echo "==> Done"
ls -lh "${OUT_FILE}"
