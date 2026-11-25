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
COS_PROFILE_PREFIX="${COS_PROFILE_PREFIX:-env_init/data/profiles}"
RELEASE_ROOT="${COS_RELEASE_PREFIX%/}"

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
BASE_KEY="${RELEASE_ROOT}/${RELEASE_TAG}/${BASE_PACKAGE_NAME}"
tar -C "$STAGE_DIR" -cf "$BASE_PACKAGE_PATH" env_tool
BASE_PACKAGE_SHA256="$(sha256sum "$BASE_PACKAGE_PATH" | awk '{print $1}')"
printf '%s  %s\n' "$BASE_PACKAGE_SHA256" "$BASE_PACKAGE_NAME" > "${RELEASE_DIR}/SHA256SUMS"
upload_file "$BASE_PACKAGE_PATH" "$BASE_KEY"

INVENTORY_RELEASE_PATH="${WORK_ROOT}/inventory.csv"
cp examples/inventory.sample.csv "$INVENTORY_RELEASE_PATH"
INVENTORY_SHA256="$(sha256sum "$INVENTORY_RELEASE_PATH" | awk '{print $1}')"
INVENTORY_KEY="${RELEASE_ROOT}/${RELEASE_TAG}/inventory.csv"
printf '%s  %s\n' "$INVENTORY_SHA256" "inventory.csv" >> "${RELEASE_DIR}/SHA256SUMS"
upload_file "$INVENTORY_RELEASE_PATH" "$INVENTORY_KEY"

BASE_ASSET_JSON="$(
  jq -nc \
    --arg name "$BASE_PACKAGE_NAME" \
    --arg path "$BASE_KEY" \
    --arg sha256 "$BASE_PACKAGE_SHA256" \
    '{name: $name, path: $path, sha256: $sha256}'
)"
INVENTORY_ASSET_JSON="$(
  jq -nc \
    --arg name "planning/inventory.csv" \
    --arg path "$INVENTORY_KEY" \
    --arg sha256 "$INVENTORY_SHA256" \
    '{name: $name, path: $path, sha256: $sha256}'
)"

IFS=':' read -r -a PROFILE_IDS <<<"$RELEASE_PROFILES"
PROFILE_ENTRIES=()
VERIFY_KEYS=("$BASE_KEY" "$INVENTORY_KEY")
go build -o "${TOOLS_DIR}/cosindex" ./cmd/cosindex
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
  PROFILE_RELEASE="${RELEASE_ROOT}/${RELEASE_TAG}/${profile_id}"
  BUNDLE_KEY="${PROFILE_RELEASE}/bundle.json"
  INDEX_KEY="${PROFILE_RELEASE}/materials.json"
  MATERIAL_ROOT="${COS_PROFILE_PREFIX%/}/${profile_id}"
  INDEX_PATH="${WORK_ROOT}/materials-${profile_id}.json"
  "${TOOLS_DIR}/cosindex" -bucket "$COS_BUCKET" -region "$COS_REGION" -endpoint "$COS_ENDPOINT" -prefix "$MATERIAL_ROOT" > "$INDEX_PATH"
  INDEX_SHA256="$(sha256sum "$INDEX_PATH" | awk '{print $1}')"
  printf '%s  %s\n' "$INDEX_SHA256" "${profile_id}/materials.json" >> "${RELEASE_DIR}/SHA256SUMS"
  upload_file "$INDEX_PATH" "$INDEX_KEY"
  printf '%s  %s\n' "$BUNDLE_SHA256" "${profile_id}/bundle.json" >> "${RELEASE_DIR}/SHA256SUMS"
  upload_file "$BUNDLE_RELEASE_PATH" "$BUNDLE_KEY"

  PROFILE_ENTRIES+=("$(
    jq -nc \
      --arg id "$profile_id" \
      --arg name "$(profile_name "$profile_id")" \
      --arg description "$(profile_description "$profile_id")" \
      --arg material_root "$MATERIAL_ROOT" \
      --arg index_path "$INDEX_KEY" --arg index_sha256 "$INDEX_SHA256" \
      --arg bundle_path "$BUNDLE_KEY" \
      --arg bundle_sha256 "$BUNDLE_SHA256" \
      --argjson inventory "$INVENTORY_ASSET_JSON" \
      '{
        id: $id,
        name: $name,
        description: $description,
        material_root: $material_root,
        material_index: {path: $index_path, sha256: $index_sha256},
        assets: [],
        bundle: {name: "planning/bundle.json", path: $bundle_path, sha256: $bundle_sha256},
        inventory: $inventory
      }'
  )")
  VERIFY_KEYS+=("$BUNDLE_KEY" "$INDEX_KEY")

  # Profile material is intentionally not read or repackaged during release.
  # The downloader consumes material_root and assembles data/ on demand.
  rm -f "$BUNDLE_RELEASE_PATH"
done

MANIFEST_PATH="${RELEASE_DIR}/manifest.json"
printf '%s\n' "${PROFILE_ENTRIES[@]}" | jq -s \
  --arg version "$RELEASE_TAG" \
  --argjson base "$BASE_ASSET_JSON" \
  --arg bucket "$COS_BUCKET" --arg region "$COS_REGION" --arg endpoint "$COS_ENDPOINT" \
  '{version: $version, store: {provider: "cos", bucket: $bucket, region: $region, endpoint: $endpoint}, base: $base, profiles: .}' \
  > "$MANIFEST_PATH"
MANIFEST_JSON_B64="$(base64 < "$MANIFEST_PATH" | tr -d '\n')"

echo "==> Verifying anonymous access to COS release objects"
for key in "${VERIFY_KEYS[@]}"; do
  curl --fail --silent --show-error --head --retry 3 --connect-timeout 15 --max-time 60 \
    "https://${COS_BUCKET}.${COS_ENDPOINT}/${key}" -o /dev/null
done

echo "==> Building cross-platform downloaders"
build_downloader() {
  local goos="$1"
  local goarch="$2"
  local output="$3"
  local ldflags

  ldflags="-s -w -X main.releaseVersion=${RELEASE_TAG} -X main.manifestJSONB64=${MANIFEST_JSON_B64}"
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
