#!/usr/bin/env bash
set -euo pipefail

GO_VERSION="${GO_VERSION:-1.26.2}"
GO_ARCHIVE="go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="${GO_DOWNLOAD_URL:-https://mirrors.aliyun.com/golang/${GO_ARCHIVE}}"
GO_FALLBACK_DOWNLOAD_URL="${GO_FALLBACK_DOWNLOAD_URL:-https://golang.google.cn/dl/${GO_ARCHIVE}}"
COSCLI_DOWNLOAD_URL="${COSCLI_DOWNLOAD_URL:-https://cosbrowser.cloud.tencent.com/software/coscli/coscli-linux-amd64}"

COS_BUCKET="${COS_BUCKET:-wxq-1318169049}"
COS_REGION="${COS_REGION:-ap-guangzhou}"
COS_ENDPOINT="${COS_ENDPOINT:-cos.${COS_REGION}.myqcloud.com}"
COS_DATA_PREFIX="${COS_DATA_PREFIX:-env_init/data}"

: "${COS_SECRET_ID:?Please configure COS_SECRET_ID in GitHub Actions repository secrets}"
: "${COS_SECRET_KEY:?Please configure COS_SECRET_KEY in GitHub Actions repository secrets}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

RELEASE_TAG="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
if [[ "$RELEASE_TAG" != v* ]]; then
  echo "error: release assembly must run on a v* tag, got '${RELEASE_TAG:-<none>}'" >&2
  exit 1
fi

WORK_PARENT="${RUNNER_TEMP:-${REPO_ROOT}/.release-work}"
mkdir -p "$WORK_PARENT"
WORK_ROOT="$(mktemp -d "${WORK_PARENT%/}/envinit-release.XXXXXX")"
trap 'rm -rf "$WORK_ROOT"' EXIT

TOOLS_DIR="${WORK_ROOT}/tools"
STAGE_DIR="${WORK_ROOT}/stage"
RELEASE_DIR="${REPO_ROOT}/release"
mkdir -p "$TOOLS_DIR" "$STAGE_DIR/env_tool" "$STAGE_DIR/env_tool/data"
rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

download() {
  local url="$1"
  local output="$2"
  curl --fail --location --retry 3 --retry-delay 2 "$url" --output "$output"
}

echo "==> Installing Go ${GO_VERSION}"
if ! download "$GO_DOWNLOAD_URL" "${WORK_ROOT}/${GO_ARCHIVE}"; then
  echo "Primary Go mirror failed, falling back to golang.google.cn"
  download "$GO_FALLBACK_DOWNLOAD_URL" "${WORK_ROOT}/${GO_ARCHIVE}"
fi
tar -C "$TOOLS_DIR" -xzf "${WORK_ROOT}/${GO_ARCHIVE}"
export PATH="${TOOLS_DIR}/go/bin:${PATH}"
go version

echo "==> Downloading COSCLI"
download "$COSCLI_DOWNLOAD_URL" "${TOOLS_DIR}/coscli"
chmod +x "${TOOLS_DIR}/coscli"

COSCLI_ARGS=(
  -r
  -e "$COS_ENDPOINT"
  -i "$COS_SECRET_ID"
  -k "$COS_SECRET_KEY"
  --init-skip=true
)
if [[ -n "${COS_SESSION_TOKEN:-}" ]]; then
  COSCLI_ARGS+=(--token "$COS_SESSION_TOKEN")
fi

echo "==> Copying repository env_tool files"
tar \
  --exclude='env_tool/data' \
  --exclude='env_tool/env_init' \
  --exclude='env_tool/env_init_arch' \
  --exclude='.DS_Store' \
  -cf - env_tool | tar -C "$STAGE_DIR" -xf -

echo "==> Downloading COS directory cos://${COS_BUCKET}/${COS_DATA_PREFIX}/"
"${TOOLS_DIR}/coscli" cp \
  "cos://${COS_BUCKET}/${COS_DATA_PREFIX}/" \
  "${STAGE_DIR}/env_tool/data/" \
  "${COSCLI_ARGS[@]}"

echo "==> Running tests"
go test ./...

echo "==> Building Linux binaries"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init" ./cmd/envinit
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init_arch" ./cmd/envinit

echo "==> Creating complete env_tool package"
PACKAGE_NAME="env_tool-${RELEASE_TAG}.tar"
tar -C "$STAGE_DIR" -cf - env_tool |
  split -b 1900m -d -a 3 - "${RELEASE_DIR}/${PACKAGE_NAME}.part-"
cp "${STAGE_DIR}/env_tool/env_init" "${RELEASE_DIR}/env_init"
cp "${STAGE_DIR}/env_tool/env_init_arch" "${RELEASE_DIR}/env_init_arch"
(
  cd "$RELEASE_DIR"
  sha256sum "${PACKAGE_NAME}.part-"* env_init env_init_arch > SHA256SUMS
)

cat > "${RELEASE_DIR}/ASSEMBLY.txt" <<EOF
Download all ${PACKAGE_NAME}.part-* files into the same directory, then run:

  cat ${PACKAGE_NAME}.part-* > ${PACKAGE_NAME}
  sha256sum -c SHA256SUMS
  tar -xf ${PACKAGE_NAME}

The split files keep every GitHub Release asset below the 2 GiB limit.
EOF

echo "==> Release files"
ls -lh "$RELEASE_DIR"
