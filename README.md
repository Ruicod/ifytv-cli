# IFYTV CLI 和 Agent Skill

把图片、配音和台词交给 Agent，它会把素材和提示词连到 IFYTV 云画布上的一张视频卡。你可以在原卡看进度、播放成片，也可以让 Agent 下载 MP4。已发布的版本支持在已有云画布上生成，开发版还可以为你新建个人云端画布。

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

要在已有画布生成视频，把**可编辑的 IFYTV 云画布链接**和素材一起发给 Agent。比如这样告诉它。

> 在这张画布上，用这五张图和三段 MP3 做一条 15 秒的视频。三段声音依次对应修表师、邮差和学徒，按我给的台词生成。做好后把 MP4 下载到当前目录。

第一次生成时，打开 Agent 给出的官网链接，核对设备码和计费账号，点击「允许」。首次使用某张画布时，还需在项目授权页允许访问。以后沿用这台设备和项目即可。设备授权免费，提交视频生成按所选模型计费。

在画布上可以看到各张图片、可试听的配音和提示词连到了哪张视频卡。卡片显示排队、生成和保存状态，模型提供百分比时也会显示。关闭对话或刷新画布后服务器继续处理，协作者可以看到更新。出片后在同一张卡上播放。要续查可以说「查一下刚才的视频」，要断开授权可以说「退出这台设备的 IFYTV 授权」。

### 没有画布链接时（开发版）

自动建画布需要配套的开发版 CLI 和站点服务。上面的一行安装命令安装的是已发布的 `v0.2.0-rc.5`，该版可在已有画布上生成。

可以直接说「用这些素材生成视频，把画布链接发给我」。没给链接时，Agent 会在当前登录账号的 **我的画布 → 云端画布** 新建项目。它保存在站点，换设备登录同一账号也能打开。给了可编辑的同站云画布链接，就在链接指向的原项目生成，即使同时提到了团队。只有你明确指定团队时，Agent 才在那个团队新建。

Agent 会自动运行 `ifytv auth status --json`，保存当前设备的授权 ID。首次登录按前节完成授权。下面引号中的 ID 取自这次设备授权，首次创建和断线续办都用同一个值。

```sh
ifytv video create --workspace personal --spec ./shot.json --run shot-07 --authorization-id '<本次ID>' --json
ifytv video wait --run shot-07 --authorization-id '<本次ID>' --json
```

只建个人空画布用 `ifytv canvas create --workspace personal --run storyboard --authorization-id '<本次ID>' --json`，这一步不提交视频任务。新版 CLI 的这两条创建命令都默认选个人云端画布。示例显式写出 `--workspace personal`，旧客户端遇到它会提示错误。升级或补授权后沿用原运行名续办。[开发版 Agent 操作说明](skills/ifytv/SKILL.md#2-定位画布并恢复) · [镜头规格示例](skills/ifytv/examples/h3.json)

要建在指定团队，先用 `ifytv canvas workspaces --json` 查看可写团队。创建时传入 `--workspace <团队UUID>`，并把列表返回的 `authorization_id` 传给 `--authorization-id`。只说「放到团队」或遇到同名团队时，Agent 会请你指明要用哪个团队。

### 接着做半成品画布（开发版）

> 继续这张画布的第六集。沿用剧本、角色素材和已经选好的片段，只补未完成的镜头。

把这句话和云画布链接交给 Agent。它先读取画布上的剧本、分组、素材关系和任务状态，再继续原任务或补缺。已选片段直接复用，空的视频卡从原卡继续。只有要用某份图片、配音或视频时，才下载对应文件。

接手续做需要站点提供设备授权的画布读取、素材读取和原卡续做。Agent 用最新画布版本建单，项目有别人的更新时会重新读取。可以单独说「先看一下这张画布还缺哪些镜头」，只读梳理完成后给出缺口。[Skill 续做说明](skills/ifytv/references/recovery.md#恢复顺序) · [站点接口约定](docs/cli-canvas-resume-api.md)

### 一句话制作漫剧单集（开发版）

> 用「未来来信的邮差」做一集 30 秒竖屏漫剧，有对白和字幕。做好后把 MP4 和画布链接发给我。

把这句话发给安装了 IFYTV Skill 的 Codex 或 Claude Code。Agent 会写剧本和分镜，固定角色外观与音色，补齐分镜图和配音，逐镜生成视频，再合成一份带对白和字幕的 MP4。也可以只指定一步，例如「把这个剧本拆成分镜」或「给这句台词配音」。

整集使用一张画布。给了链接就用原画布，没有链接默认建到「我的画布 → 云端画布」。用户明确指定团队时，才在指定团队新建。每个镜头在原卡显示进度和播放。站点支持整集回写时，Agent 也会给出成片卡链接。

逐镜合成模式需要配套开发版 CLI、站点的生图与配音授权，以及本机的 FFmpeg 和 ffprobe。`ifytv episode check --json` 可以检查导出环境。Agent 会复用已提供的素材和已完成的镜头。当前公开一行安装的 rc.5 支持前述单镜头功能。[整集操作说明](skills/ifytv/SKILL.md) · [站点接口约定](docs/cli-production-api.md)

开发版 CLI 也提供云端整集入口，配套服务负责保存计划、报价、任务和成片。Dev 测试站点的匿名整集查询返回 HTTP 404。设备授权后的云端接口、完整出片与计费尚未验证。[云端整集编排接口变更单](docs/cli-orchestration-api.md)

## 直接使用命令行

CLI 默认连接 IFYTV 官网。连接测试或自建站点时，将 `IFYTV_BASE_URL` 设为该画布的 HTTPS 根地址。新建个人或团队画布需要设备授权。设备凭据按站点保存在本机当前用户的私有目录中。已有自动化可通过 `IFYTV_API_KEY` 操作这把 Key 已获授权的项目。

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

只要本地 MP4 时，先运行 `ifytv video create --help`。帮助里列出 `--no-canvas`，就用 `ifytv video create --no-canvas --spec ./shot.json --run local-07 --json`，即使版本号显示 rc.5 也一样。仅当已确认使用旧版 `v0.2.0-rc.5` 且帮助正常显示、没有该参数时，才用 `ifytv video create --spec ./shot.json --run local-07 --json`。出片后按同一运行名下载。

项目 UUID 在画布链接 `…/canvas/cloud/<项目UUID>` 末尾。镜头规格里的相对路径以 JSON 所在目录为准，图片和音频按数组顺序上传。H3 示例含五张图和三段声音，替换素材路径及台词后使用。[查看 H3 镜头示例](https://github.com/Ruicod/ifytv-cli/blob/v0.2.0-rc.5/skills/ifytv/examples/h3.json)

创建视频后保留 `task_id`、`node_id` 和 `project_url`。中断后用同一运行名查询，`run_pending` 表示可以沿用原项目、规格、素材及运行名重试。换电脑可用 `--task <任务UUID>` 替代 `--run`，并使用原站点和视频身份。收到项目授权链接时，在浏览器确认后重试原命令。重复导入同一任务会复用原卡。

设备授权使用账号的第一把可用视频 Key。处理旧任务时，沿用创建任务时的视频身份。需要把脚本绑定到一次设备授权时，把响应里的 `authorization_id` 传给 `auth wait` 和后续命令的 `--authorization-id`。[查看完整 Skill 使用说明](https://github.com/Ruicod/ifytv-cli/blob/v0.2.0-rc.5/skills/ifytv/SKILL.md)

### 云端整集命令（开发版）

这些命令需要站点实现整集接口，并使用原站点和项目的设备授权。将 `PROJECT`、`REVISION`、`RUN_ID`、`PLAN_HASH` 和报价值取自快照或回执。先保存两个不同的 UUID，分别标识计划和本次恢复操作。

```sh
ifytv episode plan --project "$PROJECT" --revision "$REVISION" --spec "$PLAN" --request-id "$PLAN_REQUEST" --authorization-id "$AUTH" --json
ifytv episode status --project "$PROJECT" --run-id "$RUN_ID" --authorization-id "$AUTH" --json
ifytv episode quote --project "$PROJECT" --run-id "$RUN_ID" --plan-hash "$PLAN_HASH" --authorization-id "$AUTH" --json
ifytv episode resume --project "$PROJECT" --run-id "$RUN_ID" --revision "$REVISION" --plan-hash "$PLAN_HASH" --request-id "$RESUME_REQUEST" --quote-id "$QUOTE_ID" --currency "$CURRENCY" --budget-units "$BUDGET_UNITS" --authorization-id "$AUTH" --json
```

报价只估价，无预扣。按站点返回的币种和额度单位填预算。有步骤费用未知时，报价会省略总额度上界。需要限制步骤时，在 `quote` 或 `resume` 中为每个步骤传一次 `--step-id`。按运行查询详情可传上一页的 `--cursor`。也可按 `--episode-id` 列出同一集的运行。

丢回执时保留原请求键。用 `episode status --request-id` 与原 `--operation` 查受理记录。已受理的回执绑定原请求键、操作、运行 ID 和原请求体 SHA-256，可以继续查原运行。仍为 `uncertain` 时继续核对，只有原操作仍获授权时，才给原 `plan` 或 `resume` 命令加 `--replay`，保持键和参数原样。若返回已封键的 `not_accepted`，核对后经授权才可另开操作。[请求恢复和回执字段](docs/cli-orchestration-api.md#开发版命令)

## 从源码开发

所有新增、修改和删除都直接在 `dev` 分支完成。项目所有者确认后，再将 `dev` 合并到 `main`。[浏览开发分支](https://github.com/Ruicod/ifytv-cli/tree/dev)

开发环境使用 Go 1.22 或更高版本。在仓库根目录运行 `go test ./...` 可执行测试。构建当前开发包时使用独立的预发布版本号和输出目录，例如下面的命令。

```sh
go run ./tools/ifytv-release -version 0.2.0-dev.canvas.2 -out ./dist/dev-candidates
```

要重建已发布的 rc.5，从它对应的 Git 标签创建单独的工作目录，在那里构建六个平台的 CLI、Skill 和一行安装包。

```sh
git worktree add --detach ../ifytv-cli-rc.5 v0.2.0-rc.5
go -C ../ifytv-cli-rc.5 run ./tools/ifytv-release -version 0.2.0-rc.5 -out ./dist/rc-rebuilds
```
