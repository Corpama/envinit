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
COS_RELEASE_UPLOAD_ENDPOINT="${COS_RELEASE_UPLOAD_ENDPOINT:-${COS_BUCKET}.cos.accelerate.myqcloud.com}"
COS_DATA_PREFIX="${COS_DATA_PREFIX:-env_init/data}"
COS_RELEASE_PREFIX="${COS_RELEASE_PREFIX:-env_init/releases}"
ALIST_BASE_URL="${ALIST_BASE_URL:-https://alt.corpa.me}"
ALIST_STORAGE_MOUNT="${ALIST_STORAGE_MOUNT:-YZ_COS}"

: "${COS_SECRET_ID:?Please configure COS_SECRET_ID in GitHub Actions repository secrets}"
: "${COS_SECRET_KEY:?Please configure COS_SECRET_KEY in GitHub Actions repository secrets}"
: "${ALIST_USERNAME:?Please configure ALIST_USERNAME in GitHub Actions repository secrets}"
: "${ALIST_PASSWORD:?Please configure ALIST_PASSWORD in GitHub Actions repository secrets}"

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
  -e "$COS_ENDPOINT" \
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
  -e "$COS_RELEASE_UPLOAD_ENDPOINT" \
  "${COSCLI_AUTH_ARGS[@]}"

echo "==> Getting permanent AList download link"
ALIST_BASE_URL="${ALIST_BASE_URL%/}"
ALIST_FILE_PATH="/${ALIST_STORAGE_MOUNT#/}/${COS_RELEASE_OBJECT}"
ALIST_LOGIN_RESPONSE="$(
  curl --fail --silent --show-error --retry 3 --retry-delay 2 \
    --request POST \
    --header 'Content-Type: application/json' \
    --data "$(jq -nc --arg username "$ALIST_USERNAME" --arg password "$ALIST_PASSWORD" \
      '{username: $username, password: $password}')" \
    "${ALIST_BASE_URL}/api/auth/login"
)"
ALIST_TOKEN="$(jq -er 'select(.code == 200) | .data.token' <<<"$ALIST_LOGIN_RESPONSE")"
ALIST_SIGN=""
for attempt in {1..10}; do
  ALIST_FILE_RESPONSE="$(
    curl --fail --silent --show-error --retry 3 --retry-delay 2 \
      --request POST \
      --header 'Content-Type: application/json' \
      --header "Authorization: ${ALIST_TOKEN}" \
      --data "$(jq -nc --arg path "$ALIST_FILE_PATH" '{path: $path, password: ""}')" \
      "${ALIST_BASE_URL}/api/fs/get"
  )"
  if ALIST_SIGN="$(jq -er 'select(.code == 200) | .data.sign | select(length > 0)' <<<"$ALIST_FILE_RESPONSE")"; then
    break
  fi
  echo "AList has not exposed the uploaded object yet, retrying (${attempt}/10)"
  sleep 3
done
: "${ALIST_SIGN:?AList did not return a signed download link}"
if [[ "$ALIST_SIGN" != *:0 ]]; then
  echo "error: AList returned an expiring sign; configure a permanent sign ending in :0" >&2
  exit 1
fi
ALIST_SIGN_ENCODED="$(jq -rn --arg sign "$ALIST_SIGN" '$sign | @uri')"
ALIST_DOWNLOAD_URL="${ALIST_BASE_URL}/d${ALIST_FILE_PATH}?sign=${ALIST_SIGN_ENCODED}"

echo "==> Building cross-platform downloaders"
build_downloader() {
  local goos="$1"
  local goarch="$2"
  local output="$3"

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.releaseVersion=${RELEASE_TAG} -X main.packageName=${PACKAGE_NAME} -X main.packageSHA256=${PACKAGE_SHA256} -X main.downloadURL=${ALIST_DOWNLOAD_URL}" \
      -o "${RELEASE_DIR}/${output}" \
      ./cmd/downloader
}

build_downloader linux amd64 env_tool_downloader-linux-amd64
build_downloader linux arm64 env_tool_downloader-linux-arm64
build_downloader darwin amd64 env_tool_downloader-darwin-amd64
build_downloader darwin arm64 env_tool_downloader-darwin-arm64
build_downloader windows amd64 env_tool_downloader-windows-amd64.exe
build_downloader windows arm64 env_tool_downloader-windows-arm64.exe

(
  cd "$RELEASE_DIR"
  sha256sum env_tool_downloader-* >> SHA256SUMS
)

echo "==> Release files"
ls -lh "$RELEASE_DIR"
