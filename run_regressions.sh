#!/bin/bash

set -euo pipefail

if [ ! -f reninja ]; then
    echo "error: reninja binary not found. Build it with:"
    echo "  go build -ldflags=\"-s -w\" cmd/reninja/reninja.go"
    exit 1
fi

touch MODULE.bazel  # trick bb remote into running here.

UBUNTU_2404="docker://gcr.io/flame-public/rbe-ubuntu24-04@sha256:f7db0d4791247f032fdb4451b7c3ba90e567923a341cc6dc43abfc283436791a"
BB_REMOTE_COMMON_FLAGS="--runner_exec_properties=init-dockerd=true"

if [ $# -eq 0 ]; then
    RECIPES=(regressions/*.sh)
else
    RECIPES=("$@")
fi

for RECIPE in "${RECIPES[@]}"; do
    echo "============ BUILDING $RECIPE ============"
    bb remote --container_image=$UBUNTU_2404 $BB_REMOTE_COMMON_FLAGS --runner_exec_properties=salt=$RECIPE --script=$RECIPE
done

