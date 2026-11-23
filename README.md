# ifytv-cli

用本地图片和参考音频生成 IFYTV 视频，按运行名或任务 ID 找回进度、下载 MP4，或把成片导入指定云画布。配套中文 Skill 支持在 Codex 和 Claude Code 中用自然语言操作。

**当前为发布前版本。** 现在可以从本地源码运行 CLI、构建安装包并安装 Skill。`v0.1.0` 的公开下载地址供正式发布使用，当前按下方源码步骤安装。

## 从源码运行

在此仓库根目录使用 Go 1.22 或更高版本。前两条命令可离线验证本机 CLI。第三条将它安装到 `go env GOBIN` 指定的目录，该值为空时使用 `go env GOPATH` 下的 `bin`。

```sh
go run ./cmd/ifytv --version
go run ./cmd/ifytv --help
go install ./cmd/ifytv
```

将上述安装目录加入终端和 Agent 的 PATH，再运行 `ifytv --version`。普通源码构建显示 `dev`。也可以在源码目录把后续命令开头的 `ifytv` 换成 `go run ./cmd/ifytv`。

## 配置站点和 Key

`IFYTV_BASE_URL` 填你使用的 IFYTV 站点根地址，包含协议和主机。`IFYTV_API_KEY` 填该站点签发的视频 Key。下面的命令只为当前终端及其子进程设置变量，Key 在本机隐藏输入。使用 Agent 时，从已配置的终端启动它，或通过它的启动环境注入这两个变量。

**PowerShell**

```powershell
$env:IFYTV_BASE_URL = Read-Host 'IFYTV 站点根地址'
$env:IFYTV_API_KEY = [System.Net.NetworkCredential]::new('', (Read-Host 'IFYTV API Key' -AsSecureString)).Password
```

**Bash**

```bash
read -r -p 'IFYTV 站点根地址 ' IFYTV_BASE_URL
read -r -s -p 'IFYTV API Key ' IFYTV_API_KEY
printf '\n'
export IFYTV_BASE_URL IFYTV_API_KEY
```

## 从五张图、三段声音到成片

把 H3 示例复制到镜头工作目录并命名为 `shot.json`。示例内的修表师、邮差、学徒依次对应前三张图和三段音频，另外两张图给出钟表铺和怀表。将八个示例路径换成自己的素材路径，相对路径以 `shot.json` 所在目录为准。先查当前站点的模型目录，确认模型 ID、`omni` 模式和素材数量。提交生成任务可能计费，参考音色与台词归属需以成片试听为准。[H3 示例](skills/ifytv/examples/h3.json)

进入镜头工作目录后，可用运行名 `shot-07` 跟踪这次生成。

```sh
ifytv models --json
ifytv video create --spec ./shot.json --run shot-07 --json
ifytv video status --run shot-07 --json
ifytv video wait --run shot-07 --json
ifytv video download --run shot-07 --out ./shot-07.mp4
```

保留响应的 `task_id`。关闭终端后，用同一站点、同一把 Key 和运行名继续查询。上传中断时，保留原素材和规格，重新运行同一条创建命令即可续传。若建单应答中断，CLI 使用已保存的请求与幂等键查回同一任务。已有任务可按 ID 直接等待、下载或导入。

以下命令中的 `1111…` 和 `2222…` 分别代表任务 UUID 与目标云画布项目 UUID，请替换为自己的值。每条命令在 `--run` 和 `--task` 中选一个。导入需要目标项目授权，遇到 `authorization_url` 时在浏览器打开并授权，再重试原导入命令。

```sh
ifytv video status --task 11111111-1111-4111-8111-111111111111 --json
ifytv video wait --task 11111111-1111-4111-8111-111111111111 --json
ifytv video download --task 11111111-1111-4111-8111-111111111111 --out ./shot-07.mp4
ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --run shot-07 --json
ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --task 11111111-1111-4111-8111-111111111111 --json
```

## 取得同一版本的 CLI 和 Skill

在仓库根目录执行本地打包。它会用 `-ldflags "-X main.version=0.1.0"` 将版本写入 CLI，再对四个目标交叉编译。相同源码和 Go 工具链生成稳定的归档。已有版本目录受保护，重复验证同一版本可给 `-out` 指定一个新的空目录。

```sh
go test ./tools/ifytv-release -count=1
go run ./tools/ifytv-release -version 0.1.0 -out ./dist/cli-releases
```

`dist/cli-releases/latest.json` 记录版本号。`dist/cli-releases/0.1.0/manifest.json` 按下表顺序列出附件的文件名、平台、架构、版本固定的 URL、字节数和 SHA-256。打包命令只写本地文件。正式发布后，可从本仓库对应标签的 Release 获取同名附件。[仓库 Releases](https://github.com/Ruicod/ifytv-cli/releases)

| 系统 | 0.1.0 附件 | 解压内容 |
| --- | --- | --- |
| Windows x64 | `ifytv-0.1.0-windows-amd64.zip` | `ifytv.exe` |
| macOS Intel | `ifytv-0.1.0-darwin-amd64.zip` | `ifytv` |
| macOS Apple Silicon | `ifytv-0.1.0-darwin-arm64.zip` | `ifytv` |
| Linux x64 | `ifytv-0.1.0-linux-amd64.zip` | `ifytv` |
| Codex / Claude Code | `ifytv-skill-0.1.0.zip` | `ifytv/SKILL.md`、`ifytv/examples/h3.json` |

### Windows 安装

以下为首次安装示例。选择 Codex 时保持 `.agents/skills`，选择 Claude Code 时把它换成 `.claude/skills`。脚本用版本清单校验两个 ZIP 后再解压。

```powershell
$releaseDir = './dist/cli-releases/0.1.0'
$cliZip = 'ifytv-0.1.0-windows-amd64.zip'
$skillZip = 'ifytv-skill-0.1.0.zip'
$manifest = Get-Content -Raw (Join-Path $releaseDir 'manifest.json') | ConvertFrom-Json
if ($manifest.version -ne '0.1.0') { throw '清单版本不匹配' }
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
cd ./dist/cli-releases/0.1.0
cli=ifytv-0.1.0-darwin-arm64.zip
skill=ifytv-skill-0.1.0.zip
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
