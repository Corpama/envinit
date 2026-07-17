#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
build_dir="${repo_root}/build/mpich-5.0.1"
output_dir="${repo_root}/dist/mpich-runtimes/ubuntu22.04-x86_64"

mkdir -p "${output_dir}"

podman run --rm --platform linux/amd64 \
  -e PROFILE=ubuntu22.04 \
  -v "${build_dir}/source/mpich-5.0.1.tar.gz:/input/mpich-5.0.1.tar.gz:ro" \
  -v "${build_dir}/hello.c:/input/hello.c:ro" \
  -v "${build_dir}/container-build.sh:/input/container-build.sh:ro" \
  -v "${output_dir}:/output" \
  docker.io/library/ubuntu:22.04 \
  bash -lc 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends build-essential ca-certificates file perl python3 && bash /input/container-build.sh'
