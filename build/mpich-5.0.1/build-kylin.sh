#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
build_dir="${repo_root}/build/mpich-5.0.1"
kylin_repo="${repo_root}/dist/kylin-v10-sp3-2403-x86_64-rpm-repo"
output_dir="${repo_root}/dist/mpich-runtimes/kylin10sp3-x86_64"

mkdir -p "${output_dir}"

podman run --rm --platform linux/amd64 \
  -v "${kylin_repo}:/repo:ro" \
  -v "${build_dir}/source/mpich-5.0.1.tar.gz:/input/mpich-5.0.1.tar.gz:ro" \
  -v "${build_dir}/hello.c:/input/hello.c:ro" \
  -v "${build_dir}/container-build.sh:/input/container-build.sh:ro" \
  -v "${output_dir}:/output" \
  docker.io/library/rockylinux:8 \
  bash -lc '
    dnf -y --installroot=/kylin --releasever=10 \
      --disablerepo="*" --repofrompath=kylin,file:///repo --enablerepo=kylin \
      --nogpgcheck --setopt=install_weak_deps=False install \
      bash coreutils diffutils file findutils gcc gcc-c++ glibc-devel \
      gzip hostname kernel-headers make perl python3 sed tar which &&
    cp /input/mpich-5.0.1.tar.gz /kylin/tmp/ &&
    cp /input/hello.c /kylin/tmp/ &&
    cp /input/container-build.sh /kylin/tmp/ &&
    mkdir -p /kylin/output &&
    chroot /kylin /usr/bin/env PROFILE=kylin10sp3 \
      bash -lc "mkdir -p /input /output && cp /tmp/mpich-5.0.1.tar.gz /input/ && cp /tmp/hello.c /input/ && cp /tmp/container-build.sh /input/ && bash /input/container-build.sh" &&
    cp -a /kylin/output/. /output/
  '
