#!/usr/bin/env bash
set -euo pipefail

: "${PROFILE:?PROFILE is required}"

version=5.0.1
prefix=/var/lib/envinit/check-runtime/mpich-${version}
source_archive=/input/mpich-${version}.tar.gz
source_sha256=8c1832a13ddacf071685069f5fadfd1f2877a29e1a628652892c65211b1f3327
jobs=${JOBS:-4}

test "$(uname -m)" = x86_64
test "$(sha256sum "${source_archive}" | awk '{print $1}')" = "${source_sha256}"

workdir=$(mktemp -d /tmp/envinit-mpich-build.XXXXXX)
trap 'rm -rf "${workdir}"' EXIT

tar -xzf "${source_archive}" -C "${workdir}"
mkdir -p "${workdir}/build"
cd "${workdir}/build"

"${workdir}/mpich-${version}/configure" \
  --prefix="${prefix}" \
  --with-device=ch3:sock \
  --with-pm=hydra \
  --disable-fortran \
  --disable-cxx \
  --enable-shared \
  --disable-static

make -j"${jobs}"
make install

libdir="${prefix}/lib"
mpi_target=$(basename "$(readlink -f "${libdir}/libmpi.so")")
ln -sfn "${mpi_target}" "${libdir}/libmpi.so.0"

cat >"${prefix}/env.sh" <<EOF
export ENVINIT_MPICH_HOME=${prefix}
export PATH=${prefix}/bin:\${PATH}
export LD_LIBRARY_PATH=${prefix}/lib:\${LD_LIBRARY_PATH:-}
EOF

cat >"${prefix}/BUILD-MANIFEST.txt" <<EOF
profile=${PROFILE}
architecture=$(uname -m)
mpich_version=${version}
source_url=https://www.mpich.org/static/downloads/${version}/mpich-${version}.tar.gz
source_sha256=${source_sha256}
install_prefix=${prefix}
device=ch3:sock
process_manager=hydra
fortran=disabled
cxx=disabled
build_os_pretty_name=$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release | tr -d '"')
build_os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
build_os_version_id=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
libmpi_so_0_target=${mpi_target}
EOF

"${prefix}/bin/mpicc" /input/hello.c -o "${workdir}/hello"
HYDRA_IFACE=lo "${prefix}/bin/mpiexec" -n 2 "${workdir}/hello" | tee "${prefix}/SMOKE-TEST.txt"
grep -q 'rank=0 size=2' "${prefix}/SMOKE-TEST.txt"
grep -q 'rank=1 size=2' "${prefix}/SMOKE-TEST.txt"

archive="mpich-${version}-${PROFILE}-x86_64.tar.gz"
tar --sort=name --mtime='UTC 2026-07-16' --owner=0 --group=0 --numeric-owner \
  -C /var/lib/envinit/check-runtime -czf "/output/${archive}" "mpich-${version}"
(
  cd /output
  sha256sum "${archive}" >"${archive}.sha256"
)

"${prefix}/bin/mpichversion" | tee "/output/mpich-${version}-${PROFILE}-x86_64.version.txt"
cp "${prefix}/BUILD-MANIFEST.txt" "/output/mpich-${version}-${PROFILE}-x86_64.manifest.txt"
