#!/bin/bash

: ${BUILDBUDDY_API_KEY:?}

sudo cp reninja /usr/local/bin/reninja

WORK_DIR="$(pwd)/duckdb-work"
mkdir -p "$WORK_DIR"
pushd "$WORK_DIR"

# Clone or update duckdb repo
if [ -d duckdb/.git ]; then
    git -C duckdb pull
else
    git clone https://github.com/duckdb/duckdb.git --depth=1
fi
pushd duckdb
git submodule update --init

SRC=$(pwd)
BUILD_DIR=$SRC/build-rbe
mkdir -p "$BUILD_DIR"
docker run --rm \
       --user "$(id -u):$(id -g)" \
       -v "$SRC:$SRC" \
       -v "$(command -v reninja):/usr/local/bin/ninja:ro" \
       -w "$BUILD_DIR" \
       gcr.io/flame-public/rbe-ubuntu22-04:ninja \
       cmake -G Ninja \
       -DCMAKE_SUPPRESS_REGENERATION=ON \
       "$SRC"
reninja -C $BUILD_DIR -t clean
reninja -C $BUILD_DIR --bes_backend=reninja.buildbuddy.io \
	--results_url=https://reninja.buildbuddy.io/invocation \
	--remote_executor=reninja.buildbuddy.io \
	--container_image=gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	--remote_header=x-buildbuddy-api-key=$BUILDBUDDY_API_KEY \
	--remote_instance_name=$(date +%s) \
	-j 500
popd
popd
