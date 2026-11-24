# ifytv-cli

让 Codex 或 Claude Code 在 IFYTV 云画布上放好图片、配音和提示词，生成视频。打开画布就能看到素材连到了哪张视频卡、当前进度，以及原卡上的成片。也支持下载 MP4 和导入已有任务。

从目标 IFYTV 站点的 `/cli` 页面安装配套的 CLI 和 Skill。`dev` 分支提供画布内生成和浏览器设备授权。安装页显示「尚未发布」时，可按下方的源码方式体验 dev 版。

## 在 Codex 或 Claude Code 里使用

打开新对话，把可编辑的云画布链接、素材和台词发给 Agent。你可以直接这样说。

> 在这张画布上，用这五张图和三段 MP3 做一条 15 秒视频。三段声音依次对应修表师、邮差和学徒，按我给的台词生成。

打开画布就能看到图片、可试听的配音和提示词连到一张视频卡。卡片显示排队、生成和保存状态，模型报告百分比时也会显示。出片后直接在原卡点播放。提交后关掉对话或刷新页面，服务器都会继续处理，项目协作者也能看到更新。需要本地文件时可以接着说「把这条视频下载到当前目录」。

第一次生成时，Agent 会给你一个官网链接和设备码。打开链接，用自己的 IFYTV 账号登录，核对页面上的设备码和计费账号，点击「允许」。首次在某张云画布生成或导入时，在项目授权页确认对这张画布的访问。以后可以直接说「查一下刚才的视频」。

设备授权本身免费，提交视频生成按所选模型计费。想断开这台设备时，对 Agent 说「退出这台设备的 IFYTV 授权」即可。

## 从源码运行

体验 dev 版时，也可以请 Agent 用 Go 1.22 或更高版本，从同一次检出安装 CLI 和 Skill。

```sh
git clone --branch dev --single-branch https://github.com/Ruicod/ifytv-cli.git
cd ifytv-cli
go run ./cmd/ifytv --version
go install ./cmd/ifytv
```

将 Go 安装目录（`go env GOBIN`，为空时取 `go env GOPATH` 下的 `bin`）加入 Agent 的 `PATH`。把仓库里的 `skills/ifytv` 整个目录复制到所用 Agent 的技能目录，即 Codex 的 `~/.agents/skills/ifytv` 或 Claude Code 的 `~/.claude/skills/ifytv`。在启动 Agent 的环境中把 `IFYTV_BASE_URL` 设为目标 dev 画布的网址根路径（只含 `https://` 和域名），重新打开对话，运行 `ifytv --version` 核对安装。源码构建显示 `dev`。

## 命令行登录与脚本

CLI 默认连接官网。直接使用终端时，先运行 `auth login`，按返回链接在浏览器确认，再运行 `auth wait`。CLI 把设备凭据保存在当前操作系统用户的私有目录中，按站点分开。

```sh
ifytv auth login --json
ifytv auth wait --json
ifytv auth status --json
```

运行 `ifytv auth logout` 可撤销本机的设备授权。自动化调用可以把授权提示返回的 `authorization_id` 传给 `auth wait` 和后续生成命令的 `--authorization-id`，让这次请求只使用刚才确认的账号。已有脚本可以继续从自己的凭据管理器提供 `IFYTV_API_KEY`。连接自建或测试站点时，可用 `IFYTV_BASE_URL` 指定其 HTTPS 根地址。

## 从五张图、三段声音到成片

把 H3 示例复制到镜头工作目录并命名为 `shot.json`。示例内的修表师、邮差、学徒依次对应前三张图和三段音频，另外两张图给出钟表铺和怀表。将八个示例路径换成自己的素材路径，相对路径以 `shot.json` 所在目录为准。先查当前站点的模型目录，确认模型 ID、`omni` 模式和素材数量。提交生成任务可能计费，参考音色与台词归属需以成片试听为准。[H3 示例](skills/ifytv/examples/h3.json)

进入镜头工作目录，将示例项目 UUID 换成目标云画布链接末尾的 UUID。可以在建单前打开画布查看素材，使用同一个运行名跟进原卡并下载。

```sh
ifytv models --json
ifytv video create --project 22222222-2222-4222-8222-222222222222 --spec ./shot.json --run shot-07 --json
ifytv video status --run shot-07 --json
ifytv video wait --run shot-07 --json
ifytv video download --run shot-07 --out ./shot-07.mp4
```

保留响应中的 `task_id`、`node_id` 和 `project_url`。画布任务的 `video wait --run` 会等到成片保存到原卡。关闭终端后，用原站点、视频身份和运行名继续查询。上传或提交中断时，先运行 `video status --run`。返回 `run_pending` 时，用原素材、规格和运行名重试创建，CLI 会复用原幂等键和原目的地。新建仅下载的任务可以省略 `--project`。已有任务可按 ID 等待、下载或导入。

一个账号有多把视频 Key 时，设备授权使用第一把可用的视频渠道 Key。旧脚本若使用另一把 Key，请沿用原脚本凭据续查原任务。若原运行名返回 `run_not_found`，先核对站点和视频身份，避免重复创建同一部视频。

以下命令中的 `1111…` 和 `2222…` 分别代表任务 UUID 与目标云画布项目 UUID，请替换为自己的值。每条命令在 `--run` 和 `--task` 中选一个。设备已连接后，若收到项目授权链接，在浏览器确认对该画布的访问，再重试原导入命令。

```sh
ifytv video status --task 11111111-1111-4111-8111-111111111111 --json
ifytv video wait --task 11111111-1111-4111-8111-111111111111 --json
ifytv video download --task 11111111-1111-4111-8111-111111111111 --out ./shot-07.mp4
ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --run shot-07 --json
ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --task 11111111-1111-4111-8111-111111111111 --json
```

## 取得同一版本的 CLI 和 Skill

以下示例将画布生成候选包单独放在本地候选目录。在仓库根目录执行打包。它会用 `-ldflags "-X main.version=0.2.0-rc.4"` 将版本写入 CLI，再对四个目标交叉编译。相同源码和 Go 工具链生成稳定的归档。已有版本目录受保护，重复验证同一版本可给 `-out` 指定一个新的空目录。

```sh
go test ./tools/ifytv-release -count=1
go run ./tools/ifytv-release -version 0.2.0-rc.4 -out ./dist/canvas-candidates
```

`dist/canvas-candidates/latest.json` 记录版本号。`dist/canvas-candidates/0.2.0-rc.4/manifest.json` 按下表顺序列出附件的文件名、平台、架构、版本固定的 URL、字节数和 SHA-256。打包命令只写本地文件。正式发布后，可从本仓库对应标签的 Release 获取同名附件。[仓库 Releases](https://github.com/Ruicod/ifytv-cli/releases)

| 系统 | 0.2.0-rc.4 附件 | 解压内容 |
| --- | --- | --- |
| Windows x64 | `ifytv-0.2.0-rc.4-windows-amd64.zip` | `ifytv.exe` |
| macOS Intel | `ifytv-0.2.0-rc.4-darwin-amd64.zip` | `ifytv` |
| macOS Apple Silicon | `ifytv-0.2.0-rc.4-darwin-arm64.zip` | `ifytv` |
| Linux x64 | `ifytv-0.2.0-rc.4-linux-amd64.zip` | `ifytv` |
| Codex / Claude Code | `ifytv-skill-0.2.0-rc.4.zip` | `ifytv/SKILL.md`、`ifytv/examples/h3.json` |

### Windows 安装

以下为首次安装示例。选择 Codex 时保持 `.agents/skills`，选择 Claude Code 时把它换成 `.claude/skills`。脚本用版本清单校验两个 ZIP 后再解压。

```powershell
$releaseDir = './dist/canvas-candidates/0.2.0-rc.4'
$cliZip = 'ifytv-0.2.0-rc.4-windows-amd64.zip'
$skillZip = 'ifytv-skill-0.2.0-rc.4.zip'
$manifest = Get-Content -Raw (Join-Path $releaseDir 'manifest.json') | ConvertFrom-Json
if ($manifest.version -ne '0.2.0-rc.4') { throw '清单版本不匹配' }
foreach ($name in @($cliZip, $skillZip)) {
    $asset = @($manifest.assets | Where-Object name -CEQ $name)
    $path = Join-Path $releaseDir $name
    if ($asset.Count -ne 1 -or (Get-Item -LiteralPath $path -ErrorAction Stop).Length -ne $asset[0].bytes -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256 -ErrorAction Stop).Hash -ne $asset[0].sha256) {
        throw "安装包校验失败 $name"
    }
}
$binDir = Join-Path $HOME '.local/bin'
$skillsDir = Join-Path $HOME '.agents/skills'
New-Item -ItemType Directory -Force -Path $binDir,$skillsDir | Out-Null
Expand-Archive -LiteralPath (Join-Path $releaseDir $cliZip) -DestinationPath $binDir -ErrorAction Stop
Expand-Archive -LiteralPath (Join-Path $releaseDir $skillZip) -DestinationPath $skillsDir -ErrorAction Stop
$env:Path = "$binDir;$env:Path"
ifytv --version
```

### macOS / Linux 安装

进入版本目录，按上表设置 `cli`。示例选择 Apple Silicon。将输出的两个字节数和两个摘要分别与 `manifest.json` 中同名附件的 `bytes`、`sha256` 核对。

```sh
cd ./dist/canvas-candidates/0.2.0-rc.4
cli=ifytv-0.2.0-rc.4-darwin-arm64.zip
skill=ifytv-skill-0.2.0-rc.4.zip
wc -c "$cli" "$skill"
if command -v sha256sum >/dev/null; then
    sha256sum "$cli" "$skill"
else
    shasum -a 256 "$cli" "$skill"
fi
cat manifest.json
```

两项校验都相符后执行。若使用 Claude Code，把技能目录改为 `"$HOME/.claude/skills"`。

```sh
skills_dir="$HOME/.agents/skills"
mkdir -p "$HOME/.local/bin" "$skills_dir"
unzip "$cli" -d "$HOME/.local/bin"
unzip "$skill" -d "$skills_dir"
export PATH="$HOME/.local/bin:$PATH"
ifytv --version
```

两个 Agent 可各解压一次同一个 Skill ZIP。确保下次启动时 PATH 仍包含 CLI 目录，再打开新会话。可以说「按这五张图和三段 MP3 做一条 H3，先列出每个角色的配音」或「把这个任务 ID 的成片导入我给的云画布」。[Skill 使用说明](skills/ifytv/SKILL.md)
