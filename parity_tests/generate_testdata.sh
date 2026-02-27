#!/usr/bin/env bash
#
# Generates test data for parity tests.
# Downloads project source archives and the C++ ninja binary.
#
# Usage: ./generate_testdata.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTDATA_DIR="${SCRIPT_DIR}/testdata"
WORK_DIR="$(mktemp -d)"

cleanup() {
    rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

mkdir -p "${TESTDATA_DIR}"

echo "=== Working in ${WORK_DIR} ==="

# --- C++ ninja binary ---
NINJA_VERSION="1.12.1"
echo "--- Fetching C++ ninja ${NINJA_VERSION} ---"
cd "${WORK_DIR}"
curl -fsSL "https://github.com/ninja-build/ninja/releases/download/v${NINJA_VERSION}/ninja-linux.zip" -o ninja-linux.zip
unzip -q ninja-linux.zip -d ninja-bin
cp ninja-bin/ninja "${TESTDATA_DIR}/ninja"
chmod +x "${TESTDATA_DIR}/ninja"
echo "  C++ ninja $(${TESTDATA_DIR}/ninja --version) saved to testdata/ninja"

# --- Ninja source ---
echo "--- Fetching ninja source v${NINJA_VERSION} ---"
cd "${WORK_DIR}"
curl -fsSL "https://github.com/ninja-build/ninja/archive/refs/tags/v${NINJA_VERSION}.tar.gz" -o ninja-src.tar.gz
cp ninja-src.tar.gz "${TESTDATA_DIR}/ninja-src.tar.gz"
echo "  ninja source archive: $(du -h "${TESTDATA_DIR}/ninja-src.tar.gz" | cut -f1)"

# --- fmt (fmtlib) source ---
FMT_VERSION="11.0.2"
echo "--- Fetching fmt source ${FMT_VERSION} ---"
cd "${WORK_DIR}"
curl -fsSL "https://github.com/fmtlib/fmt/archive/refs/tags/${FMT_VERSION}.tar.gz" -o fmt-src.tar.gz
cp fmt-src.tar.gz "${TESTDATA_DIR}/fmt-src.tar.gz"
echo "  fmt source archive: $(du -h "${TESTDATA_DIR}/fmt-src.tar.gz" | cut -f1)"

# --- zlib source ---
ZLIB_VERSION="1.3.1"
echo "--- Fetching zlib source ${ZLIB_VERSION} ---"
cd "${WORK_DIR}"
curl -fsSL "https://github.com/madler/zlib/releases/download/v${ZLIB_VERSION}/zlib-${ZLIB_VERSION}.tar.gz" -o zlib-src.tar.gz
cp zlib-src.tar.gz "${TESTDATA_DIR}/zlib-src.tar.gz"
echo "  zlib source archive: $(du -h "${TESTDATA_DIR}/zlib-src.tar.gz" | cut -f1)"

echo ""
echo "=== Done ==="
echo "Test data in ${TESTDATA_DIR}:"
ls -lh "${TESTDATA_DIR}/"
