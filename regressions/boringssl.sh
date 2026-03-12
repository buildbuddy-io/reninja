#!/bin/bash

: ${BUILDBUDDY_API_KEY:?}

if ! reninja --version 2>/dev/null | grep -qF "1.14.0.git"; then
    curl -fSL "https://github.com/buildbuddy-io/reninja/releases/download/v0.1.4/reninja-linux-amd64" -o reninja
    chmod +x reninja
    sudo mv reninja /usr/local/bin/reninja
fi

WORK_DIR="$(pwd)/boringssl-work"
mkdir -p "$WORK_DIR"
pushd "$WORK_DIR"

# Clone or update boringssl repo
if [ -d boringssl/.git ]; then
    git -C boringssl pull
else
    git clone https://github.com/google/boringssl.git --depth=1
fi
pushd boringssl
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

reninja -C $BUILD_DIR --bes_backend=remote.buildbuddy.io \
	--results_url=https://app.buildbuddy.io/invocation \
	--remote_executor=remote.buildbuddy.io \
	--container_image=gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	--remote_header=x-buildbuddy-api-key=$BUILDBUDDY_API_KEY \
	-j 500
popd
popd
