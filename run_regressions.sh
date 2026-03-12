#!/bin/bash

touch MODULE.bazel

UBUNTU_2404="docker://gcr.io/flame-public/rbe-ubuntu24-04@sha256:f7db0d4791247f032fdb4451b7c3ba90e567923a341cc6dc43abfc283436791a"
BB_REMOTE_COMMON_FLAGS="--runner_exec_properties=init-dockerd=true"


for RECIPE in recipes/*.sh; do
  bb remote --container_image=$UBUNTU_2404 $BB_REMOTE_COMMON_FLAGS --runner_exec_properties=salt=$RECIPE --script=$RECIPE
done

