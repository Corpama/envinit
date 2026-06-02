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
COS_RELEASE_PREFIX="${COS_RELEASE_PREFIX:-env_init/releases}"

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

COSCLI_AUTH_ARGS=(
  -e "$COS_ENDPOINT"
  -i "$COS_SECRET_ID"
  -k "$COS_SECRET_KEY"
  --init-skip=true
  --disable-log
)
if [[ -n "${COS_SESSION_TOKEN:-}" ]]; then
  COSCLI_AUTH_ARGS+=(--token "$COS_SESSION_TOKEN")
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
  -r \
  "${COSCLI_AUTH_ARGS[@]}"

echo "==> Running tests"
go test ./...

echo "==> Building Linux binaries"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init" ./cmd/envinit
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init_arch" ./cmd/envinit

echo "==> Creating complete env_tool package"
PACKAGE_NAME="env_tool-${RELEASE_TAG}.tar"
PACKAGE_PATH="${WORK_ROOT}/${PACKAGE_NAME}"
COS_RELEASE_OBJECT="${COS_RELEASE_PREFIX}/${RELEASE_TAG}/${PACKAGE_NAME}"
tar -C "$STAGE_DIR" -cf "$PACKAGE_PATH" env_tool
cp "${STAGE_DIR}/env_tool/env_init" "${RELEASE_DIR}/env_init"
cp "${STAGE_DIR}/env_tool/env_init_arch" "${RELEASE_DIR}/env_init_arch"
PACKAGE_SHA256="$(sha256sum "$PACKAGE_PATH" | awk '{print $1}')"
(
  cd "$RELEASE_DIR"
  sha256sum env_init env_init_arch > SHA256SUMS
)
printf '%s  %s\n' "$PACKAGE_SHA256" "$PACKAGE_NAME" >> "${RELEASE_DIR}/SHA256SUMS"

echo "==> Uploading complete package to cos://${COS_BUCKET}/${COS_RELEASE_OBJECT}"
"${TOOLS_DIR}/coscli" cp \
  "$PACKAGE_PATH" \
  "cos://${COS_BUCKET}/${COS_RELEASE_OBJECT}" \
  "${COSCLI_AUTH_ARGS[@]}"

cat > "${RELEASE_DIR}/download.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail

: "\${COS_SECRET_ID:?Please set COS_SECRET_ID to a read-only COS credential}"
: "\${COS_SECRET_KEY:?Please set COS_SECRET_KEY to a read-only COS credential}"

PACKAGE_NAME="${PACKAGE_NAME}"
PACKAGE_SHA256="${PACKAGE_SHA256}"
COS_BUCKET="${COS_BUCKET}"
COS_ENDPOINT="${COS_ENDPOINT}"
COS_OBJECT="${COS_RELEASE_OBJECT}"
COSCLI_DOWNLOAD_BASE="${COSCLI_DOWNLOAD_URL%-amd64}"

case "\$(uname -m)" in
  x86_64|amd64) COSCLI_ARCH="amd64" ;;
  aarch64|arm64) COSCLI_ARCH="arm64" ;;
  *)
    echo "error: unsupported Linux architecture: \$(uname -m)" >&2
    exit 1
    ;;
esac

WORK_DIR="\$(mktemp -d)"
trap 'rm -rf "\$WORK_DIR"' EXIT
COSCLI="\${WORK_DIR}/coscli"

curl --fail --location --retry 3 --retry-delay 2 \
  "\${COSCLI_DOWNLOAD_BASE}-\${COSCLI_ARCH}" \
  --output "\$COSCLI"
chmod +x "\$COSCLI"

COSCLI_ARGS=(
  -e "\$COS_ENDPOINT"
  -i "\$COS_SECRET_ID"
  -k "\$COS_SECRET_KEY"
  --init-skip=true
  --disable-log
)
if [[ -n "\${COS_SESSION_TOKEN:-}" ]]; then
  COSCLI_ARGS+=(--token "\$COS_SESSION_TOKEN")
fi

"\$COSCLI" cp \
  "cos://\${COS_BUCKET}/\${COS_OBJECT}" \
  "\$PACKAGE_NAME" \
  "\${COSCLI_ARGS[@]}"

printf '%s  %s\n' "\$PACKAGE_SHA256" "\$PACKAGE_NAME" | sha256sum -c -
echo "Downloaded and verified: \$PACKAGE_NAME"
echo "Extract with: tar -xf \$PACKAGE_NAME"
EOF
chmod +x "${RELEASE_DIR}/download.sh"

echo "==> Release files"
ls -lh "$RELEASE_DIR"
