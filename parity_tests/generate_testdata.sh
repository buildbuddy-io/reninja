#!/usr/bin/env bash
#
# Generates test data for parity tests.
# Downloads project source archives and the C++ ninja binary.
# Safe to run repeatedly -- skips downloads if files already exist.
#
# Usage: ./generate_testdata.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTDATA_DIR="${SCRIPT_DIR}/testdata"

mkdir -p "${TESTDATA_DIR}"

# --- C++ ninja binary ---
if [ -x "${TESTDATA_DIR}/ninja" ]; then
    echo "--- ninja binary already exists, skipping ---"
else
    echo "--- Fetching C++ ninja binary ---"
    WORK_DIR="$(mktemp -d)"
    curl -fsSL "https://github.com/ninja-build/ninja/releases/download/v1.12.1/ninja-linux.zip" -o "${WORK_DIR}/ninja.zip"
    unzip -q "${WORK_DIR}/ninja.zip" -d "${WORK_DIR}"
    cp "${WORK_DIR}/ninja" "${TESTDATA_DIR}/ninja"
    chmod +x "${TESTDATA_DIR}/ninja"
    rm -rf "${WORK_DIR}"
fi

# --- Source archives ---
# Each entry: "output_filename url"
ARCHIVES=(
    "ninja-src.tar.gz https://github.com/ninja-build/ninja/archive/refs/tags/v1.12.1.tar.gz"
    "fmt-src.tar.gz https://github.com/fmtlib/fmt/archive/refs/tags/11.0.2.tar.gz"
    "zlib-src.tar.gz https://github.com/madler/zlib/releases/download/v1.3.1/zlib-1.3.1.tar.gz"
)

for entry in "${ARCHIVES[@]}"; do
    name="${entry%% *}"
    url="${entry#* }"
    dest="${TESTDATA_DIR}/${name}"

    if [ -f "${dest}" ]; then
        echo "--- ${name} already exists, skipping ---"
    else
        echo "--- Fetching ${name} ---"
        curl -fsSL "${url}" -o "${dest}"
    fi
done

echo ""
echo "=== Done ==="
ls -lh "${TESTDATA_DIR}/"
