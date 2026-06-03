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
COS_RELEASE_UPLOAD_ENDPOINT="${COS_RELEASE_UPLOAD_ENDPOINT:-cos.accelerate.myqcloud.com}"
COS_DATA_PREFIX="${COS_DATA_PREFIX:-env_init/data}"
COS_RELEASE_PREFIX="${COS_RELEASE_PREFIX:-env_init/releases}"
ALIST_BASE_URL="${ALIST_BASE_URL:-https://alt.corpa.me}"
ALIST_RELEASE_PREFIX="${ALIST_RELEASE_PREFIX:-/releases}"

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
PACKAGE_SHA256="$(sha256sum "$PACKAGE_PATH" | awk '{print $1}')"
printf '%s  %s\n' "$PACKAGE_SHA256" "$PACKAGE_NAME" > "${RELEASE_DIR}/SHA256SUMS"

echo "==> Uploading complete package to cos://${COS_BUCKET}/${COS_RELEASE_OBJECT}"
upload_release_package() {
  local attempt

  for attempt in 1 2 3; do
    if "${TOOLS_DIR}/coscli" cp \
      "$PACKAGE_PATH" \
      "cos://${COS_BUCKET}/${COS_RELEASE_OBJECT}" \
      -e "$COS_RELEASE_UPLOAD_ENDPOINT" \
      "${COSCLI_AUTH_ARGS[@]}"; then
      return 0
    fi

    echo "COS release upload failed (${attempt}/3)." >&2
    if [[ -d "${REPO_ROOT}/coscli_output" ]]; then
      find "${REPO_ROOT}/coscli_output" -type f -maxdepth 2 -print -exec sed -n '1,240p' {} \; >&2
    fi
    sleep 5
  done

  return 1
}
upload_release_package

echo "==> Getting permanent AList download link"
ALIST_BASE_URL="${ALIST_BASE_URL%/}"
ALIST_FILE_PATH="${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}/${PACKAGE_NAME}"
ALIST_LOGIN_RESPONSE="$(
  curl --fail --silent --show-error --retry 3 --retry-delay 2 \
    --request POST \
    --header 'Content-Type: application/json' \
    --data "$(jq -nc --arg username "$ALIST_USERNAME" --arg password "$ALIST_PASSWORD" \
      '{username: $username, password: $password}')" \
    "${ALIST_BASE_URL}/api/auth/login"
)"
ALIST_TOKEN="$(jq -er 'select(.code == 200) | .data.token' <<<"$ALIST_LOGIN_RESPONSE")"
refresh_alist_parent_dirs() {
  local current_path=""
  local part
  local response

  IFS='/' read -r -a parts <<<"${ALIST_FILE_PATH%/*}"
  for part in "${parts[@]}"; do
    [[ -z "$part" ]] && continue
    current_path="${current_path}/${part}"
    response="$(
      curl --fail --silent --show-error --retry 3 --retry-delay 2 \
        --request POST \
        --header 'Content-Type: application/json' \
        --header "Authorization: ${ALIST_TOKEN}" \
        --data "$(jq -nc --arg path "$current_path" \
          '{path: $path, password: "", page: 1, per_page: 500, refresh: true}')" \
        "${ALIST_BASE_URL}/api/fs/list"
    )"
    echo "AList refresh ${current_path}: $(jq -r '"code=\(.code // "unknown") message=\(.message // "unknown")"' <<<"$response")"
  done
}

ALIST_RAW_URL=""
for attempt in {1..24}; do
  refresh_alist_parent_dirs
  ALIST_FILE_RESPONSE="$(
    curl --fail --silent --show-error --retry 3 --retry-delay 2 \
      --request POST \
      --header 'Content-Type: application/json' \
      --header "Authorization: ${ALIST_TOKEN}" \
      --data "$(jq -nc --arg path "$ALIST_FILE_PATH" '{path: $path, password: "", refresh: true}')" \
      "${ALIST_BASE_URL}/api/fs/get"
  )"
  if ALIST_RAW_URL="$(jq -er 'select(.code == 200) | .data.raw_url | select(length > 0)' <<<"$ALIST_FILE_RESPONSE")"; then
    break
  fi
  ALIST_ERROR="$(jq -r '"code=\(.code // "unknown") message=\(.message // "unknown")"' <<<"$ALIST_FILE_RESPONSE" 2>/dev/null || printf '%s' "$ALIST_FILE_RESPONSE")"
  echo "AList has not exposed the uploaded object yet (${attempt}/24): ${ALIST_ERROR}"
  sleep 5
done
: "${ALIST_RAW_URL:?AList did not return a raw download URL}"

ALIST_USERNAME_B64="$(printf '%s' "$ALIST_USERNAME" | base64 | tr -d '\n')"
ALIST_PASSWORD_B64="$(printf '%s' "$ALIST_PASSWORD" | base64 | tr -d '\n')"

echo "==> Building cross-platform downloaders"
build_downloader() {
  local goos="$1"
  local goarch="$2"
  local output="$3"

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.releaseVersion=${RELEASE_TAG} -X main.packageName=${PACKAGE_NAME} -X main.packageSHA256=${PACKAGE_SHA256} -X main.alistBaseURL=${ALIST_BASE_URL} -X main.alistFilePath=${ALIST_FILE_PATH} -X main.alistUserB64=${ALIST_USERNAME_B64} -X main.alistPassB64=${ALIST_PASSWORD_B64}" \
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
