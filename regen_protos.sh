#!/bin/bash

# There is probably an easier way to do this.

set -euo pipefail

if [ ! -d "google" ]; then
    git clone https://github.com/googleapis/googleapis.git
    cp -r googleapis/google ./
]; fi

rm -rf genproto
mkdir -p proto-out
protoc --go_out=./proto-out proto/*.proto
protoc --go-grpc_out=./proto-out proto/*.proto
mv proto-out/github.com/buildbuddy-io/gin/genproto ./
rm -rf proto-out
