# env_init 与 downloader 编译、发版操作手册

核对日期：2026-10-23。依据当前工作区的 [发布脚本](../scripts/github-release.sh)、[GitHub Actions](../.github/workflows/release.yml)、[downloader 入口](../cmd/downloader/main.go) 和 [env_init 入口](../cmd/envinit/main.go)。这是操作流程，不表示当前工作区已经提交、发布或完成实机验收。

## 1. 先分清三种操作

| 操作 | 产物和用途 | 是否访问或修改发布服务 |
| --- | --- | --- |
| 手动编译 env_init | 目标 Linux 服务器上的执行程序 | 不发布；缺少 Go 依赖缓存时会下载依赖 |
| 手动编译 downloader | 无元数据版本用于构建/入口检查；嵌入元数据版本用于正式下载 | 编译本身不发布；正式下载运行时访问 AList/COS |
| 完整发版 | base、规划模板、manifest、六个平台的 downloader、GitHub Release | 上传 COS、刷新 AList、发布 GitHub 资产；默认还会删除旧 COS 版本 |

**只想更新一个测试二进制，执行第 3 或第 4 节即可，不要运行发布脚本。** 完整发版优先走第 6 节的 tag → Actions 流程；第 7 节是受控的人工替代流程，不能与 Actions 同时执行。

## 2. 环境和源码准备

- 在仓库根目录执行命令。本文使用 Bash 命令；Windows 可在 Bash 环境交叉编译，或直接使用发布好的 `.exe`。
- `go.mod` 当前要求 Go 1.26；发布脚本默认下载 **Go 1.26.2**。为了与 CI 一致，手动构建也建议使用 1.26.2。
- 普通编译需要 Go、Git；首次构建需要模块下载网络，离线构建要事先准备相同版本的 module cache。
- 完整发布脚本要求 **Linux x86_64、Bash 4+**，以及 `curl`、`tar`、`jq`、`base64`、`sha256sum`、GNU `sort -V` 等工具。脚本下载的是 Linux amd64 Go/COSCLI，不能直接在 macOS 或 ARM Linux 上执行。GitHub 发布还需要 `gh` 及相应认证。
- 不把下载器运行电脑的架构与目标服务器的 profile 混淆。例如 Apple Silicon Mac 使用 darwin-arm64 downloader，仍可以下载 Kylin x86_64 服务器的材料。

先检查，不自动丢弃本地修改：

```bash
pwd
git status --short
git diff --stat
git remote -v
git branch --show-current
git rev-parse HEAD
go version
mkdir -p dist
```

当前仓库的 GitHub remote 名为 `origin`，地址为 `git@github.com:Corpama/envinit.git`；另有 `gitee` 镜像。操作前以本地 `git remote -v` 为准。推送到 Gitee 不会触发本仓库 GitHub Actions。

本文统一输出到被 Git 忽略的 `dist/`，避免覆盖仓库根目录已有的 `env_init` 等文件。正式版本应从干净、已审核的提交构建；本地修改版只能作为开发测试产物交付。

## 3. 手动编译 env_init

### 3.1 Linux x86_64 与 ARM64

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o dist/env_init ./cmd/envinit

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o dist/env_init_arch ./cmd/envinit

file dist/env_init dist/env_init_arch
go version -m dist/env_init
go version -m dist/env_init_arch
```

`env_init` 应是 Linux x86-64 ELF，`env_init_arch` 应是 Linux ARM aarch64 ELF。macOS/Windows 可以交叉编译，但不能在本机直接运行 Linux ELF。

当前 env_init **没有 `--version` 子命令，也没有发布版本字符串的 `-X` 注入接口**；不要照搬 downloader 的 ldflags。使用构建时的 Git commit、tag、`go version -m` 中的 VCS 信息和交付记录核对来源；本地脏工作区的构建信息不能当作干净 tag 产物。

### 3.2 测试与交付

开发阶段先做影响范围内的定向回归；正式发布前执行：

```bash
go test ./...
go vet ./...
```

将对应架构二进制复制到测试交付目录，在匹配架构的 Linux 服务器上先运行 `./env_init --help`。正式 `plan/apply` 参数见 [现场手册](../README.md)，不要为了验证编译就直接执行 apply。

ARM64 二进制可构建不等于 ARM64 物料可用：当前正式 profile 只有 Ubuntu/Kylin **x86_64**，不能把其中的 OFED/XRE/XDR 包用于 ARM 服务器。

## 4. 手动编译 downloader

### 4.1 不注入元数据：构建检查

在本机编译 downloader 做入口和构建检查。不注入 release manifest、AList 地址和凭据也可使用离线 `config` 子命令；profile 下载才需要发布元数据：

```bash
CGO_ENABLED=0 go build -trimpath -o dist/downloader-check ./cmd/downloader
go test ./cmd/downloader
```

注入可用 release manifest 后，才使用 profile 参数执行下载：

```bash
./dist/downloader-release --profile ubuntu22.04-x86_64 \
  --output-dir ./dist/my-delivery
```

下载完成后，用 `./dist/downloader-release config --project ./dist/my-delivery` 在本机初始化或编辑规划，参数与目标机上的 `env_init config` 相同，见 [config 模板准备](envinit-config.md)。

普通 `go build ./cmd/downloader` **不会自动带入下载地址、认证和 manifest**，不能当正式 downloader 直接下载。旧版 `packageName/packageSHA256/alistFilePath` 单归档参数属于兼容入口，新发行流程不要使用。

### 4.2 注入已有 release 元数据：构建可下载版本

适用于补构建或内部验证。前提是已有经过确认的 `manifest.json`，其 base/bundle/inventory 已在 AList 发布并可下载；**仅构建 downloader 不会创建或上传这些文件**。新版本完整发行使用第 6/7 节，避免复制旧 manifest 后只改版本号。

manifest 含 `version`、`base` 和 `profiles`；每个 profile 包含 `material_root`、`bundle`、`inventory`、`assets`。文件资产包含 `name/path/sha256`。直接使用发布脚本生成的 manifest，不手写或省略摘要。

下面在 Bash 中运行。先把 `MANIFEST_FILE`、`RELEASE_TAG`、`ALIST_BASE_URL` 改成已确认值；不要把实际账号密码写入脚本、仓库、终端录屏或命令历史：

```bash
MANIFEST_FILE=/absolute/path/to/manifest.json
RELEASE_TAG=vX.Y.Z
ALIST_BASE_URL=https://your-alist.example.com

test -f "$MANIFEST_FILE"
jq -e --arg version "$RELEASE_TAG" \
  '.version == $version and (.profiles | length > 0)' "$MANIFEST_FILE"

read -r -p 'AList download username: ' ALIST_USERNAME
read -r -s -p 'AList download password: ' ALIST_PASSWORD
printf '\n'

MANIFEST_JSON_B64="$(base64 < "$MANIFEST_FILE" | tr -d '\n')"
ALIST_USERNAME_B64="$(printf '%s' "$ALIST_USERNAME" | base64 | tr -d '\n')"
ALIST_PASSWORD_B64="$(printf '%s' "$ALIST_PASSWORD" | base64 | tr -d '\n')"
DOWNLOADER_LDFLAGS="-s -w -X main.releaseVersion=${RELEASE_TAG} -X main.manifestJSONB64=${MANIFEST_JSON_B64} -X main.alistBaseURL=${ALIST_BASE_URL} -X main.alistUserB64=${ALIST_USERNAME_B64} -X main.alistPassB64=${ALIST_PASSWORD_B64}"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "$DOWNLOADER_LDFLAGS" \
  -o dist/env_tool_downloader-linux-amd64 ./cmd/downloader

unset ALIST_USERNAME ALIST_PASSWORD ALIST_USERNAME_B64 ALIST_PASSWORD_B64 DOWNLOADER_LDFLAGS
```

逐条确认 `test/jq` 成功再继续；若保存为构建脚本，在开头加 `set -euo pipefail`。其他平台在最后一条构建命令中替换 `GOOS/GOARCH` 和输出文件名；在 `unset` 前完成所有需要的构建：

| GOOS | GOARCH | 正式资产名 |
| --- | --- | --- |
| linux | amd64 | env_tool_downloader-linux-amd64 |
| linux | arm64 | env_tool_downloader-linux-arm64 |
| darwin | amd64 | env_tool_downloader-darwin-amd64 |
| darwin | arm64 | env_tool_downloader-darwin-arm64 |
| windows | amd64 | env_tool_downloader-windows-amd64.exe |
| windows | arm64 | env_tool_downloader-windows-arm64.exe |

**凭据边界：Base64 不是加密。** AList 账号密码会嵌入二进制，也可能出现在 Go 构建信息/进程参数中，拿到 downloader 的人应视为能拿到该账号。必须使用只读、最小目录权限的专用下载账号，禁止管理员账号；不要公开 `-ldflags`、`go version -m downloader` 或包含凭据的诊断输出。不要开启 `set -x`。COS Secret 只用于上传，不能注入 downloader。

## 5. 正式发布的前置条件与产物

### 5.1 凭据与路径

在 GitHub 仓库的 Actions secrets 中配置下列名称。手动发布时使用同名环境变量，由安全环境注入：

| 名称 | 要求/用途 |
| --- | --- |
| `COS_SECRET_ID`、`COS_SECRET_KEY` | 必填，上传发布文件；默认清理流程还需列出、删除发布前缀内对象的权限 |
| `COS_SESSION_TOKEN` | 可选；使用 COS 临时凭据时，与对应 ID/Key 配套提供 |
| `ALIST_USERNAME`、`ALIST_PASSWORD` | 必填，发布时验证路径，随后嵌入 downloader；应限制为下载/列目录所需权限 |
| `GH_TOKEN` | Actions 发布步骤使用 `github.token` 自动提供；人工 `gh` 操作需自行认证，拥有该仓库发布权限 |

发布脚本的当前默认值：

| 环境变量 | 默认值/说明 |
| --- | --- |
| `GO_VERSION` | `1.26.2` |
| `GO_DOWNLOAD_URL`、`GO_FALLBACK_DOWNLOAD_URL` | 阿里云 Go 镜像、golang.google.cn 对应版本的 Linux amd64 包 |
| `COSCLI_DOWNLOAD_URL` | 脚本中指定的 Linux amd64 COSCLI 下载地址 |
| `COS_BUCKET` | `wxq-1318169049` |
| `COS_REGION` / `COS_ENDPOINT` | `ap-guangzhou` / `cos.ap-guangzhou.myqcloud.com` |
| `COS_RELEASE_UPLOAD_ENDPOINT` | `cos.accelerate.myqcloud.com`，上传和删除使用；桶需支持该访问方式 |
| `COS_RELEASE_PREFIX` | `env_init/releases` |
| `COS_RELEASE_KEEP` | `2`，按 `sort -V` 排序，只保留最新两个可识别版本目录，不按发布时间 |
| `ALIST_BASE_URL` | `https://alt.corpa.me` |
| `ALIST_RELEASE_PREFIX` | `/releases` |
| `ALIST_PROFILE_PREFIX` | `/data/profiles` |
| `RELEASE_PROFILES` | `ubuntu22.04-x86_64:kylin10sp3-x86_64`，冒号分隔；当前模板映射仅实现这两种 |
| `RUNNER_TEMP` | 临时工作目录的父目录；未设置时使用仓库 `.release-work/` |

只在 GitHub Settings 新建同名 Variables **不会自动覆盖**这些默认值；当前 workflow 仅显式传入五项 COS/AList secrets。要改变 CI 行为，需在 workflow 的组装步骤 `env` 中显式传值，并在打 tag 前提交。人工流程则 `export` 对应变量。

**清理风险必须提前确认：** `COS_RELEASE_KEEP=2` 会真实删除旧版本对象，导致对应旧 downloader 无法再取得其 base/模板；不会同步删除 GitHub Release。当前脚本将 `COS_RELEASE_KEEP=0` 解释为跳过清理（会打印 warning），不是删除全部。若需验收前保留历史版本，在发布前将组装步骤的 `COS_RELEASE_KEEP` 明确设为 `'0'`。不应为补构建旧版本贸然执行默认清理。

AList `/releases/<tag>/...` 必须映射到 COS `env_init/releases/<tag>/...`。两个 profile 的长期物料目录也必须已就绪，并与各自 bundle 中 `data/` 路径一致。脚本只校验轻量发布文件可解析到下载地址，**不会验证 profile 大材料是否完整，也不实际下载校验这些大文件**。

### 5.2 发布产物分布

| 位置 | 内容 |
| --- | --- |
| COS `env_init/releases/<tag>/env_tool-base-<tag>.tar` | `env_tool/` 内的 README、`env_init`、`env_init_arch`、`run1.sh`、`run2.sh` |
| COS `env_init/releases/<tag>/inventory.csv` | 来自 `examples/inventory.sample.csv` 的公共规划模板 |
| COS `env_init/releases/<tag>/<profile>/bundle.json` | 来自 Ubuntu/Kylin 各自 sample JSON 的规划模板 |
| 本地 `release/`，随后上传 GitHub Release | 六个平台的 downloader、`manifest.json`、`SHA256SUMS`，正常为 8 个资产 |
| AList `/data/profiles/<profile>/` | 长期维护的大材料，发布脚本不下载、不重打包、不自动同步 |

base 包中的 `run1.sh/run2.sh` 是发布脚本生成的快捷脚本，规划路径硬编码为 `/mnt/usb/env_tool/planning/`，且用相对路径 `./env_init`。现场应在对应交付目录执行；换挂载路径或使用 ARM64 时必须调整调用。`run1.sh` 执行 software/ofed，`run2.sh` 执行 network/xre/xdr/firmware/container/mlxconfig/sysctl/kernel/post；它们是会修改目标机的 apply，不是下载验收命令。

base/模板通过 tag 和摘要绑定；`material_root` 则是可变的长期目录。**只保存 GitHub tag/downloader 不能完整重现历史交付**，应另行归档验收通过的整个交付目录及材料记录。

## 6. 推荐流程：tag 触发 GitHub Actions

### 6.1 确定发布范围、版本和中文说明

先查看本地/远端状态，再选一个未使用的版本。下列网络命令属于发布操作时的检查，本手册编写时不代表已执行：

```bash
git fetch origin --tags
git status --short
git log -5 --oneline
git tag --sort=-version:refname
gh release list --limit 10
```

检查计划提交与目标远端分支的差异，不假定当前分支叫 main。不要 `git add -A` 一次收进材料、用户二进制或无关改动；按实际文件逐项暂存、`git diff --cached` 审核并提交。正式验证以最终提交为准。

为新版本添加 `release-notes/<tag>.md`，**在打 tag 前提交**。文件名须与 tag 完全一致。建议结构：

```markdown
# vX.Y.Z

## 新增与修复
- 本版实际交付的变化及适用系统。

## 使用方式与兼容性
- 参数/规划表的变化、旧交付目录迁移方法、是否需要新建输出目录。

## 验证结果
- 实际运行的测试、构建平台、通过情况。
- 实机环境及结果；未做的 Ubuntu/Kylin/网络重启/硬件验收明确标为未验证。

## 已知限制与回退
- 材料要求、待处理风险、保留旧交付目录等措施。
```

不要将“模拟测试通过”“成功交叉编译”写成“生产部署或双系统实机验收通过”。本项目重跑专项的验收要求见 [重复执行专项](session-rerun-acceptance.md)。缺少验证时应阻止正式验收，或明确标为受限测试版；当前 workflow 不会替你检查实机验收结果。

### 6.2 发布前检查

```bash
go test ./...
go vet ./...
bash -n scripts/github-release.sh
jq empty examples/bundle.ubuntu22.sample.json
jq empty examples/bundle.kylin10sp3.sample.json
git diff --check
git status --short
```

另按第 3/4 节验证需要的平台构建，并核对样例规划、profile 物料、secrets、COS 保留策略。当前 CI 只内置 `go test ./...` 和交叉编译，不包含 `go vet` 或实机验收。

### 6.3 打 tag 并推送

把下例 `vX.Y.Z` 替换为实际新版本；不要原样执行，不要对已发布 tag 强制改指向。以下命令逐条执行并确认成功：

```bash
RELEASE_TAG=vX.Y.Z
test -f "release-notes/${RELEASE_TAG}.md"
git status --short
git rev-parse HEAD
git ls-remote --tags origin "refs/tags/${RELEASE_TAG}" "refs/tags/${RELEASE_TAG}^{}"
```

确认工作区干净、中文说明在 HEAD 中、远端没有同名 tag 后：

```bash
git tag -a "$RELEASE_TAG" -m "Release ${RELEASE_TAG}"
git show --no-patch "$RELEASE_TAG"
git push origin HEAD
git push origin "refs/tags/${RELEASE_TAG}"
```

若处于 detached HEAD 或分支名/远端策略不同，先明确目标分支再推送，不盲目使用 `git push origin HEAD`。触发发版的是**推送 `v*` tag**；普通分支 push 不触发。当前 workflow 没有 `workflow_dispatch`，不能用 `gh workflow run release.yml` 手动启动新 run。

### 6.4 跟踪并核对产物

```bash
gh run list --workflow release.yml --limit 10
```

在列表中确认 tag 和提交，再填入对应 run ID：

```bash
RELEASE_RUN_ID=123456789
gh run view "$RELEASE_RUN_ID"
gh run watch "$RELEASE_RUN_ID" --exit-status
gh release view "$RELEASE_TAG" --json tagName,isDraft,isPrerelease,url,assets
git rev-parse "${RELEASE_TAG}^{commit}"
git ls-remote --tags origin "refs/tags/${RELEASE_TAG}" "refs/tags/${RELEASE_TAG}^{}"
```

核对 Actions 的提交等于 tag 解引用后的 commit；annotated tag 本身的对象 SHA 不等于 commit，不能直接拿两者比较。核对 Release 中文说明和 8 个资产，再执行第 8 节验收。

当前 workflow 直接 `gh release create`，未显式设置 draft/prerelease/latest 策略。名称含 rc/beta 不能替代预发布标记；如需测试版，发布前明确调整流程。需要核对 GitHub 的 latest 指向可运行 `gh api repos/Corpama/envinit/releases/latest --jq .tag_name`，不能仅根据本地最新 tag 推断。

## 7. 人工完整发布：仅用于受控替代或故障补救

**与 Actions 二选一。** 推送 tag 默认会启动 Actions；人工流程前必须确认该 tag 的 run 已停止/结束，并在仓库发布流程中协调停用自动发布或等待故障处置。不要边运行本地脚本边让 CI 上传相同 COS 路径。

在 Linux x86_64 的独立干净 checkout 中，检出已审核的精确 tag；不要在有重要 `release/` 内容的工作区运行。脚本会直接删除并重建仓库 `release/`，成功或失败退出时清理自己的临时工作目录。

```bash
RELEASE_TAG=vX.Y.Z
git switch --detach "$RELEASE_TAG"
git status --short
git describe --tags --exact-match HEAD
test -f "release-notes/${RELEASE_TAG}.md"
```

确认 `git describe` 输出就是预期版本。多个 tag 指向同一提交时，脚本不接受通过 `RELEASE_TAG` 环境变量指定版本，而是自行 `git describe`；选到其他 tag 时必须先解决版本选择问题。

通过安全凭据管理工具给当前 shell 注入第 5 节的 COS/AList 环境变量，不在文档或仓库中保存凭据，然后执行：

```bash
export COS_RELEASE_KEEP=0
bash scripts/github-release.sh
ls -lh release/
jq '.version, [.profiles[].id]' release/manifest.json
```

这里显式跳过历史清理；如需清理，另行审核范围和保留版本，不在补发时顺手删除。**脚本没有 dry-run**，这条命令会真实上传、刷新并验证 AList，然后构建 downloader。它不会创建 GitHub Release。

确认脚本成功且 tag/manifest 一致、远端 tag 正确、Release 尚不存在后，人工完成最后一步：

```bash
gh auth status
gh release create "$RELEASE_TAG" release/* \
  --verify-tag \
  --title "$RELEASE_TAG" \
  --notes-file "release-notes/${RELEASE_TAG}.md"
```

`release/*` 在这里应只有脚本生成的 8 个文件。若 Release 已存在，不重复 create，不直接 `--clobber` 替换已被用户下载的资产；先按第 9 节确认已有内容和失败位置。

## 8. 下载与交付验收

### 8.1 从 GitHub 重新下载，不能只测本地构建文件

以下以 Linux x86_64 下载电脑为例，其他系统替换资产名。在仓库根目录，用新目录避免混入旧版本：

```bash
RELEASE_TAG=vX.Y.Z
VERIFY_DIR="$(mktemp -d /tmp/envinit-release-check.XXXXXX)"
gh release download "$RELEASE_TAG" --dir "$VERIFY_DIR" \
  --pattern env_tool_downloader-linux-amd64 \
  --pattern manifest.json \
  --pattern SHA256SUMS

(
  cd "$VERIFY_DIR"
  sha256sum env_tool_downloader-linux-amd64 manifest.json
  awk '$2 == "env_tool_downloader-linux-amd64" || $2 == "manifest.json"' SHA256SUMS
)
chmod +x "$VERIFY_DIR/env_tool_downloader-linux-amd64"
"$VERIFY_DIR/env_tool_downloader-linux-amd64" --list-profiles
```

确认计算结果与清单中两项一致、版本和两个 profile 正确。macOS 用 `shasum -a 256`，Windows PowerShell 用 `Get-FileHash -Algorithm SHA256`。`SHA256SUMS` 同时记录 COS base/模板和 GitHub 资产，下载部分资产后直接 `sha256sum -c SHA256SUMS` 会因缺文件报错，应仅核对实际下载项。

### 8.2 两个 profile 分别组装

此步骤会下载真实大材料，预留网络与磁盘空间：

```bash
"$VERIFY_DIR/env_tool_downloader-linux-amd64" \
  --profile ubuntu22.04-x86_64 --output-dir "$VERIFY_DIR/ubuntu"
"$VERIFY_DIR/env_tool_downloader-linux-amd64" \
  --profile kylin10sp3-x86_64 --output-dir "$VERIFY_DIR/kylin"
```

每个目录应包含 `env_init`、`env_init_arch`、README、run 脚本、`planning/bundle.json`、`planning/inventory.csv`、对应 `data/` 及 `.envinit-downloads/` 元数据。检查 bundle 目标平台与材料路径，不能混入另一个系统的物料。规划模板不是现场最终规划。

显式 `--profile` 只下载并组装交付目录。下载完成后，在下载机上用相应平台的 downloader 初始化或编辑规划，例如：

```bash
"$VERIFY_DIR/env_tool_downloader-linux-amd64" config \
  --project "$VERIFY_DIR/ubuntu"
```

同一版本、同一 profile、同一目录再运行下载命令，核对用户 planning 未被覆盖、已校验材料复用、项目最终处于 ready 状态。不要手工删除项目锁/状态来绕过版本或 profile 不一致；变更版本或系统时新建目录、导入规划。配置文件的初始化、保存和 review 规则见 [`env_init config` pipeline 方案](envinit-configure-design.md)。

### 8.3 验收记录与现场边界

至少记录 tag、commit、Actions run、资产名、profile、组装日志、材料时间/版本、规划文件和验收人。受控保存完整交付目录，避免只留下可变物料目录的链接；其中 downloader 和规划可能含凭据/机器信息，应按敏感交付物保管。

编译、列 profile 和下载成功仅证明对应链路，不证明驱动安装、RDMA 连通、网络重载或重启恢复。正式现场验收还须覆盖 Ubuntu netplan/networkd、Kylin legacy network/NetworkManager，以及目标硬件上的阶段重跑。不要在下载电脑运行 `run1.sh/run2.sh` 验收，更不要据此关机或刷固件。

## 9. 故障、重试与回退

发布过程不是跨 COS/AList/GitHub 的原子事务：可能已上传部分对象或已清理旧版本，但 GitHub Release 尚未创建。先定位失败步骤再处理。

| 现象 | 检查和处理 |
| --- | --- |
| 本地 Go 构建/测试失败 | 核对 Go 版本、依赖缓存、实际错误和工作区；不要跳过测试包装成正式成功 |
| COS 上传失败 | 检查凭据、会话 token、桶/前缀/endpoint 权限。已成功上传的部分不会自动回滚；修正后再决定重试 |
| AList 文件未就绪 | 检查 `/releases` 映射、账号权限及缓存；脚本会刷新并轮询。COS 有文件不等于 AList 路径正确 |
| 大材料缺失或下载 404 | 发布脚本未检查大材料。恢复正确 profile 对象并刷新 AList；不能靠重新打 tag 修复物料路径 |
| 缺 `release-notes/<tag>.md` | workflow 在组装和清理之后才检查说明，可能已有外部副作用。若仅缺说明，可核对后人工补发说明；若改源码/模板，使用新 commit/new tag，不移动已公开 tag |
| 脚本成功、GitHub create 失败 | 先 `gh release view` 检查是否已创建。人工 `release/` 完整保留时可只重试发布；Actions 没有单独上传中间构建 artifact，runner 结束后不能假定目录仍可取回 |
| GitHub Release 已存在 | 整体重跑仍会先上传 COS/清理历史，最后 create 才失败。不要用整个 workflow 当“更新说明”按钮；只改说明可审核后 `gh release edit` |
| 需重跑失败的 Actions | 确认同一 tag、输入未变、Release 尚未存在、旧版本保留风险已评估后，使用 `gh run rerun <run-id> --failed`。当前只有一个 job，会重跑整个 job，不只是失败的 shell 步骤 |
| downloader 中断 | 使用同版同 profile 原目录重试；保留 `.envinit-downloads/` 和规划，不能跨版本强行续传 |
| 要回退现场版本 | 优先使用已归档、验收过的完整旧交付目录。只换旧 downloader 不会回退长期 profile 材料；旧 COS 对象被清理时还需有受控备份才能恢复下载 |

若已公开的源码/模板/资产需要修改，原则上发新版本，不覆盖旧 tag 和下载资产。回退 GitHub 的 latest 标记不会回退服务器已写入的网络、驱动或固件，目标机恢复应按各阶段恢复/备份流程单独处理。

## 10. 发布完成检查单

- [ ] 已确认发布范围，工作区无未纳入的功能依赖，tag 精确指向审核后的提交。
- [ ] 中文说明在 tag 内，描述与实际交付一致，未把待验收事项写成已通过。
- [ ] 测试、静态检查、所需架构构建通过；profile 材料与模板匹配。
- [ ] AList 专用账号权限可接受，COS 清理策略明确，历史回退材料已保留。
- [ ] Actions 或人工发布完成，GitHub 8 项资产齐全、版本/manifest/摘要一致。
- [ ] 从 GitHub 新下载的 downloader 完成两个 profile 组装与重跑保护检查。
- [ ] 已记录实机验收结果及未覆盖项，并归档可复现的完整交付目录。
- [ ] 已核对 latest/prerelease 状态和对外交付说明；没有把历史残留版本误报为最新版本。
