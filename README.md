# IFYTV CLI 和 Agent Skill

把云画布链接、图片、配音和台词交给 Agent，它会把素材和提示词放到画布上，连到一张视频卡。你可以在原卡看进度、播放成片，也可以让 Agent 下载 MP4。

## 一行安装

在运行 Agent 的电脑或 WSL 里，选一条命令执行。每条都会安装 **IFYTV CLI 和配套 Skill**，自动识别系统和架构、校验下载包。支持 Windows、macOS、Linux 的 x64 和 ARM64，无需编译或管理员权限。

**Windows · PowerShell**

```powershell
irm https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/install.ps1 | iex
```

**macOS / Linux / WSL · 终端**

```sh
sh -c 'script=$(curl -fsSL https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/install.sh) && sh -c "$script"'
```

<details>
<summary>使用 npm、pnpm、Bun、Yarn、wget 或 Windows 命令提示符安装</summary>

任选你已有的工具。包管理器直接使用本项目的 GitHub Release 安装包。精简 Linux 环境需要 `curl` 或 `wget`、`sha256sum` 或 `shasum`，以及 `unzip`、`bsdtar` 或 Python 3 中的一种。

**npm**

```sh
npx -y https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/ifytv-install.tgz
```

**pnpm**

```sh
pnpm dlx https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/ifytv-install.tgz
```

**Bun**

```sh
bunx --bun ifytv-install@https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/ifytv-install.tgz
```

**Yarn 2 及以上**

```sh
yarn dlx ifytv-install@https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/ifytv-install.tgz
```

**wget**

```sh
sh -c 'script=$(wget -qO- https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/install.sh) && sh -c "$script"'
```

**Windows 命令提示符（cmd）**

```bat
powershell -NoProfile -Command "irm https://github.com/Ruicod/ifytv-cli/releases/download/v0.2.0-rc.5/install.ps1 | iex"
```

</details>

安装完成后重新打开终端和 Agent。运行 `ifytv --version` 应显示 `0.2.0-rc.5`。如果 Windows 仍命中系统中已有的旧版，用 `Get-Command ifytv -All` 查看顺序，或直接使用安装器打印的完整路径。离线 ZIP 和 SHA-256 校验清单可从发布页下载。[查看本版下载](https://github.com/Ruicod/ifytv-cli/releases/tag/v0.2.0-rc.5)

### 支持的 Agent

安装器默认把 Skill 放入 **Codex** 和 **Claude Code** 的用户技能目录。如果发现 Cursor、Gemini CLI、GitHub Copilot、OpenCode、Windsurf、Kiro CLI 或 OpenClaw 的配置目录，也会为它们安装。后来才装的 Agent 可以再运行一次安装命令，或执行 `ifytv install --agent cursor` 单独添加 Cursor。运行 `ifytv install --help` 可查看可选名称。

<details>
<summary>已有 IFYTV CLI，想用 skills 工具给其他 Agent 添加 Skill</summary>

运行下面这行，按工具提示选择 Agent，也可以在末尾加 `--agent opencode` 指定目标。这条命令只安装本版 Skill，适用于 `ifytv --version` 已显示 `0.2.0-rc.5` 的电脑。

```sh
npx skills add Ruicod/ifytv-cli@v0.2.0-rc.5 -g
```

</details>

## 第一次生成视频

打开 Agent 新对话，把**可编辑的 IFYTV 云画布链接**和素材给它。比如这样告诉它。

> 在这张画布上，用这五张图和三段 MP3 做一条 15 秒的视频。三段声音依次对应修表师、邮差和学徒，按我给的台词生成。做好后把 MP4 下载到当前目录。

第一次生成时，打开 Agent 给出的官网链接，核对设备码和计费账号，点击「允许」。首次使用某张画布时，还需在项目授权页允许访问。以后沿用这台设备和项目即可。设备授权免费，提交视频生成按所选模型计费。

在画布上可以看到各张图片、可试听的配音和提示词连到了哪张视频卡。卡片显示排队、生成和保存状态，模型提供百分比时也会显示。关闭对话或刷新画布后服务器继续处理，协作者可以看到更新。出片后在同一张卡上播放。要续查可以说「查一下刚才的视频」，要断开授权可以说「退出这台设备的 IFYTV 授权」。

## 直接使用命令行

CLI 默认连接 IFYTV 官网。连接测试或自建站点时，将 `IFYTV_BASE_URL` 设为该画布的 HTTPS 根地址。设备凭据按站点保存在本机当前用户的私有目录中，已有自动化也可通过 `IFYTV_API_KEY` 指定视频身份。

| 用途 | 命令 |
| --- | --- |
| 查看当前站点的模型与参数 | `ifytv models --json` |
| 获取设备授权链接 | `ifytv auth login --json` |
| 等待授权 / 查看本机授权 | `ifytv auth wait --json` / `ifytv auth status --json` |
| 撤销设备授权 | `ifytv auth logout` |
| 在画布创建视频 | `ifytv video create --project <项目UUID> --spec ./shot.json --run shot-07 --json` |
| 查看进度 | `ifytv video status --run shot-07 --json` |
| 等到原卡可播放 | `ifytv video wait --run shot-07 --json` |
| 下载成片 | `ifytv video download --run shot-07 --out ./shot-07.mp4` |
| 把已有任务导入画布 | `ifytv canvas import --project <项目UUID> --task <任务UUID> --json` |

项目 UUID 在画布链接 `…/canvas/cloud/<项目UUID>` 末尾。镜头规格里的相对路径以 JSON 所在目录为准，图片和音频按数组顺序上传。H3 示例含五张图和三段声音，替换素材路径及台词后使用。[查看 H3 镜头示例](https://github.com/Ruicod/ifytv-cli/blob/v0.2.0-rc.5/skills/ifytv/examples/h3.json)

创建后保留 `task_id`、`node_id` 和 `project_url`。中断后用同一运行名查询，`run_pending` 表示可以沿用原项目、规格、素材及运行名重试。换电脑可用 `--task <任务UUID>` 替代 `--run`，并使用原站点和视频身份。收到项目授权链接时，在浏览器确认后重试原命令。重复导入同一任务会复用原卡。

设备授权使用账号的第一把可用视频 Key。处理旧任务时，沿用创建任务时的视频身份。需要把脚本绑定到一次设备授权时，把响应里的 `authorization_id` 传给 `auth wait` 和后续命令的 `--authorization-id`。[查看完整 Skill 使用说明](https://github.com/Ruicod/ifytv-cli/blob/v0.2.0-rc.5/skills/ifytv/SKILL.md)

## 从源码开发

源码在 `dev` 分支，使用 Go 1.22 或更高版本。在仓库根目录运行 `go test ./...` 可执行测试。维护者可用 `go run ./tools/ifytv-release -version 0.2.0-rc.5 -out ./dist/candidates` 重建本版六个平台的 CLI、Skill 和一行安装包。[浏览开发分支](https://github.com/Ruicod/ifytv-cli/tree/dev)
