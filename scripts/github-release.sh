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
COS_RELEASE_PREFIX="${COS_RELEASE_PREFIX:-env_init/releases}"
COS_RELEASE_KEEP="${COS_RELEASE_KEEP:-2}"
RELEASE_PROFILES="${RELEASE_PROFILES:-ubuntu22.04-x86_64:kylin10sp3-x86_64}"
ALIST_BASE_URL="${ALIST_BASE_URL:-https://alt.corpa.me}"
ALIST_RELEASE_PREFIX="${ALIST_RELEASE_PREFIX:-/releases}"
ALIST_PROFILE_PREFIX="${ALIST_PROFILE_PREFIX:-/data/profiles}"

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
mkdir -p "$TOOLS_DIR" "$STAGE_DIR/env_tool" "$RELEASE_DIR"
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

profile_name() {
	case "$1" in
		ubuntu22.04-x86_64) printf 'Ubuntu 22.04 x86_64' ;;
		kylin10sp3-x86_64) printf 'Kylin V10 SP3 x86_64' ;;
		*) printf '%s' "$1" ;;
	esac
}

profile_description() {
	case "$1" in
		ubuntu22.04-x86_64) printf 'Ubuntu apt/deb material profile' ;;
		kylin10sp3-x86_64) printf 'Kylin yum/rpm material profile' ;;
		*) printf 'Custom material profile' ;;
	esac
}

profile_bundle_template() {
	case "$1" in
		ubuntu22.04-x86_64) printf 'examples/bundle.ubuntu22.sample.json' ;;
		kylin10sp3-x86_64) printf 'examples/bundle.kylin10sp3.sample.json' ;;
		*) return 1 ;;
	esac
}

upload_file() {
  local local_path="$1"
  local cos_object="$2"
  local attempt

  for attempt in 1 2 3; do
    if "${TOOLS_DIR}/coscli" cp \
      "$local_path" \
      "cos://${COS_BUCKET}/${cos_object}" \
      -e "$COS_RELEASE_UPLOAD_ENDPOINT" \
      "${COSCLI_AUTH_ARGS[@]}"; then
      return 0
    fi

    echo "COS release upload failed (${attempt}/3): ${cos_object}" >&2
    if [[ -d "${REPO_ROOT}/coscli_output" ]]; then
      find "${REPO_ROOT}/coscli_output" -maxdepth 2 -type f -print -exec sed -n '1,240p' {} \; >&2
    fi
    sleep 5
  done

  return 1
}

echo "==> Assembling env_tool base files"
cp README.md "${STAGE_DIR}/env_tool/README.md"
cat > "${STAGE_DIR}/env_tool/run1.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

sudo ./env_init apply \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --stages software ofed
EOF
cat > "${STAGE_DIR}/env_tool/run2.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

sudo ./env_init apply \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --stages network xre xdr firmware container mlxconfig sysctl kernel post
EOF
chmod +x "${STAGE_DIR}/env_tool/run1.sh" "${STAGE_DIR}/env_tool/run2.sh"

echo "==> Running tests"
go test ./...

echo "==> Building Linux binaries"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init" ./cmd/envinit
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o "${STAGE_DIR}/env_tool/env_init_arch" ./cmd/envinit

echo "==> Creating base env_tool package"
BASE_PACKAGE_NAME="env_tool-base-${RELEASE_TAG}.tar"
BASE_PACKAGE_PATH="${WORK_ROOT}/${BASE_PACKAGE_NAME}"
BASE_ALIST_PATH="${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}/${BASE_PACKAGE_NAME}"
tar -C "$STAGE_DIR" -cf "$BASE_PACKAGE_PATH" env_tool
BASE_PACKAGE_SHA256="$(sha256sum "$BASE_PACKAGE_PATH" | awk '{print $1}')"
printf '%s  %s\n' "$BASE_PACKAGE_SHA256" "$BASE_PACKAGE_NAME" > "${RELEASE_DIR}/SHA256SUMS"
upload_file "$BASE_PACKAGE_PATH" "${COS_RELEASE_PREFIX}/${RELEASE_TAG}/${BASE_PACKAGE_NAME}"

INVENTORY_RELEASE_PATH="${WORK_ROOT}/inventory.csv"
cp examples/inventory.sample.csv "$INVENTORY_RELEASE_PATH"
INVENTORY_SHA256="$(sha256sum "$INVENTORY_RELEASE_PATH" | awk '{print $1}')"
INVENTORY_ALIST_PATH="${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}/inventory.csv"
printf '%s  %s\n' "$INVENTORY_SHA256" "inventory.csv" >> "${RELEASE_DIR}/SHA256SUMS"
upload_file "$INVENTORY_RELEASE_PATH" "${COS_RELEASE_PREFIX}/${RELEASE_TAG}/inventory.csv"

BASE_ASSET_JSON="$(
  jq -nc \
    --arg name "$BASE_PACKAGE_NAME" \
    --arg path "$BASE_ALIST_PATH" \
    --arg sha256 "$BASE_PACKAGE_SHA256" \
    '{name: $name, path: $path, sha256: $sha256}'
)"
INVENTORY_ASSET_JSON="$(
  jq -nc \
    --arg name "planning/inventory.csv" \
    --arg path "$INVENTORY_ALIST_PATH" \
    --arg sha256 "$INVENTORY_SHA256" \
    '{name: $name, path: $path, sha256: $sha256}'
)"

IFS=':' read -r -a PROFILE_IDS <<<"$RELEASE_PROFILES"
PROFILE_ENTRIES=()
ALIST_VERIFY_PATHS=("$BASE_ALIST_PATH" "$INVENTORY_ALIST_PATH")
for profile_id in "${PROFILE_IDS[@]}"; do
  [[ -n "$profile_id" ]] || continue

  bundle_template="$(profile_bundle_template "$profile_id")"
  if [[ ! -f "$bundle_template" ]]; then
    echo "error: missing bundle template for ${profile_id}: ${bundle_template}" >&2
    exit 1
  fi

  BUNDLE_RELEASE_PATH="${WORK_ROOT}/bundle-${profile_id}.json"
  cp "$bundle_template" "$BUNDLE_RELEASE_PATH"
  BUNDLE_SHA256="$(sha256sum "$BUNDLE_RELEASE_PATH" | awk '{print $1}')"
  BUNDLE_ALIST_PATH="${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}/${profile_id}/bundle.json"
  printf '%s  %s\n' "$BUNDLE_SHA256" "${profile_id}/bundle.json" >> "${RELEASE_DIR}/SHA256SUMS"
  upload_file "$BUNDLE_RELEASE_PATH" "${COS_RELEASE_PREFIX}/${RELEASE_TAG}/${profile_id}/bundle.json"

  PROFILE_ENTRIES+=("$(
    jq -nc \
      --arg id "$profile_id" \
      --arg name "$(profile_name "$profile_id")" \
      --arg description "$(profile_description "$profile_id")" \
      --arg material_root "${ALIST_PROFILE_PREFIX%/}/${profile_id}" \
      --arg bundle_path "$BUNDLE_ALIST_PATH" \
      --arg bundle_sha256 "$BUNDLE_SHA256" \
      --argjson inventory "$INVENTORY_ASSET_JSON" \
      '{
        id: $id,
        name: $name,
        description: $description,
        material_root: $material_root,
        assets: [],
        bundle: {name: "planning/bundle.json", path: $bundle_path, sha256: $bundle_sha256},
        inventory: $inventory
      }'
  )")
  ALIST_VERIFY_PATHS+=("$BUNDLE_ALIST_PATH")

  # Profile material is intentionally not read or repackaged during release.
  # The downloader consumes material_root and assembles data/ on demand.
  rm -f "$BUNDLE_RELEASE_PATH"
done

MANIFEST_PATH="${RELEASE_DIR}/manifest.json"
printf '%s\n' "${PROFILE_ENTRIES[@]}" | jq -s \
  --arg version "$RELEASE_TAG" \
  --argjson base "$BASE_ASSET_JSON" \
  '{version: $version, base: $base, profiles: .}' \
  > "$MANIFEST_PATH"
MANIFEST_JSON_B64="$(base64 < "$MANIFEST_PATH" | tr -d '\n')"

ALIST_BASE_URL="${ALIST_BASE_URL%/}"
ALIST_USERNAME_B64="$(printf '%s' "$ALIST_USERNAME" | base64 | tr -d '\n')"
ALIST_PASSWORD_B64="$(printf '%s' "$ALIST_PASSWORD" | base64 | tr -d '\n')"

echo "==> Refreshing AList release directories"
ALIST_LOGIN_RESPONSE="$(
  curl --fail --silent --show-error --retry 3 --retry-delay 2 \
    --request POST \
    --header 'Content-Type: application/json' \
    --data "$(jq -nc --arg username "$ALIST_USERNAME" --arg password "$ALIST_PASSWORD" \
      '{username: $username, password: $password}')" \
    "${ALIST_BASE_URL}/api/auth/login"
)"
ALIST_TOKEN="$(jq -er 'select(.code == 200) | .data.token' <<<"$ALIST_LOGIN_RESPONSE")"
refresh_alist_dir() {
  local path="$1"
  local response
  response="$(
    curl --fail --silent --show-error --retry 3 --retry-delay 2 \
      --request POST \
      --header 'Content-Type: application/json' \
      --header "Authorization: ${ALIST_TOKEN}" \
      --data "$(jq -nc --arg path "$path" \
        '{path: $path, password: "", page: 1, per_page: 500, refresh: true}')" \
      "${ALIST_BASE_URL}/api/fs/list"
  )"
  if ! jq -e 'select(.code == 200)' >/dev/null <<<"$response"; then
    echo "error: AList refresh ${path} failed: $(jq -r '"code=\(.code // "unknown") message=\(.message // "unknown")"' <<<"$response")" >&2
    return 1
  fi
  echo "AList refresh ${path}: code=200"
}
refresh_alist_dir "${ALIST_RELEASE_PREFIX%/}"
refresh_alist_dir "${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}"
for profile_id in "${PROFILE_IDS[@]}"; do
  [[ -n "$profile_id" ]] || continue
  refresh_alist_dir "${ALIST_RELEASE_PREFIX%/}/${RELEASE_TAG}/${profile_id}"
done

verify_alist_file() {
  local path="$1"
  local attempt
  local response

  for attempt in {1..24}; do
    response="$(
      curl --fail --silent --show-error --retry 3 --retry-delay 2 \
        --request POST \
        --header 'Content-Type: application/json' \
        --header "Authorization: ${ALIST_TOKEN}" \
        --data "$(jq -nc --arg path "$path" \
          '{path: $path, password: "", refresh: true}')" \
        "${ALIST_BASE_URL}/api/fs/get"
    )"
    if jq -e 'select(.code == 200) | .data.raw_url | select(length > 0)' >/dev/null <<<"$response"; then
      echo "AList verified ${path}"
      return 0
    fi
    echo "AList file is not ready (${attempt}/24): ${path}: $(jq -r '"code=\(.code // "unknown") message=\(.message // "unknown")"' <<<"$response")"
    sleep 5
  done

  echo "error: AList did not expose release file: ${path}" >&2
  return 1
}

for alist_path in "${ALIST_VERIFY_PATHS[@]}"; do
  verify_alist_file "$alist_path"
done

echo "==> Building cross-platform downloaders"
build_downloader() {
  local goos="$1"
  local goarch="$2"
  local output="$3"
  local ldflags

  ldflags="-s -w -X main.releaseVersion=${RELEASE_TAG} -X main.manifestJSONB64=${MANIFEST_JSON_B64} -X main.alistBaseURL=${ALIST_BASE_URL} -X main.alistUserB64=${ALIST_USERNAME_B64} -X main.alistPassB64=${ALIST_PASSWORD_B64}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build \
      -trimpath \
      -ldflags "$ldflags" \
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
  sha256sum manifest.json >> SHA256SUMS
)

cleanup_old_cos_releases() {
  if ! [[ "$COS_RELEASE_KEEP" =~ ^[0-9]+$ ]]; then
    echo "warning: COS_RELEASE_KEEP must be a non-negative integer, got ${COS_RELEASE_KEEP}; skip cleanup" >&2
    return 0
  fi
  if [[ "$COS_RELEASE_KEEP" -eq 0 ]]; then
    echo "warning: COS_RELEASE_KEEP=0 would delete every release; skip cleanup" >&2
    return 0
  fi

  local listing
  if ! listing="$("${TOOLS_DIR}/coscli" ls "cos://${COS_BUCKET}/${COS_RELEASE_PREFIX}/" -e "$COS_ENDPOINT" "${COSCLI_AUTH_ARGS[@]}" 2>/dev/null)"; then
    echo "warning: could not list COS releases for cleanup; skip cleanup" >&2
    return 0
  fi

  mapfile -t releases < <(
    awk '{print $NF}' <<<"$listing" |
      sed -E 's#/$##; s#.*/##' |
      grep -E '^v[0-9A-Za-z._-]+$' |
      sort -V
  )
  local total="${#releases[@]}"
  if (( total <= COS_RELEASE_KEEP )); then
    echo "COS release cleanup: ${total} release(s), keep ${COS_RELEASE_KEEP}; nothing to delete"
    return 0
  fi

  local delete_count=$((total - COS_RELEASE_KEEP))
  local idx
  for ((idx = 0; idx < delete_count; idx++)); do
    echo "Deleting old COS release cos://${COS_BUCKET}/${COS_RELEASE_PREFIX}/${releases[$idx]}/"
    "${TOOLS_DIR}/coscli" rm \
      "cos://${COS_BUCKET}/${COS_RELEASE_PREFIX}/${releases[$idx]}/" \
      -r \
      -e "$COS_RELEASE_UPLOAD_ENDPOINT" \
      "${COSCLI_AUTH_ARGS[@]}"
  done
}

echo "==> Cleaning old COS release packages"
cleanup_old_cos_releases

echo "==> Release files"
ls -lh "$RELEASE_DIR"
