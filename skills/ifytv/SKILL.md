---
name: ifytv
description: 用 IFYTV CLI 把本地图片、声音和镜头规格提交为视频，续查已有任务、下载 MP4，或把已生成的成片导入指定云画布。适用于用户直接给素材、运行名或 IFYTV 任务 ID 的请求。
---

# 用 IFYTV 生成视频和导入画布

先辨认用户给的是新素材、本机运行名，还是已经生成的任务 UUID。运行名用于在本机、同一站点和同一把 Key 下续办。任务 UUID 可在配置好原站点和原 Key 的另一台机器使用。查询、等待、下载、导入各选一个定位参数 `--run` 或 `--task`。

## 准备 CLI 和凭据

在 Agent 所在终端运行 `ifytv --version` 检查可执行文件。让用户在本机设置 `IFYTV_BASE_URL` 为站点根地址（如 `https://video.example.invalid` 是格式示意），通过终端隐藏输入或本机密钥管理器设置 `IFYTV_API_KEY`。聊天、镜头 JSON 和命令参数均无需填写密钥。用 `ifytv models --json` 读取该站点的模型目录，核对模型 ID、模式、时长、画幅、参考素材数量。

若请求包含安装，CLI 和 Skill 均取自 `Ruicod/ifytv-cli` 的同一版本。发布前，在本地源码根目录运行 `go run ./tools/ifytv-release -version 0.1.0 -out ./dist/cli-releases` 可取得整套附件。发布后的附件由该仓库对应版本的 GitHub Release 提供。按版本目录的 `manifest.json` 核对两个 ZIP 的字节数和 SHA-256，再将平台包的 `ifytv`（Windows 为 `ifytv.exe`）放到 PATH 上。把 `ifytv-skill-<版本>.zip` 连同顶层 `ifytv/` 解压到所用 Agent 的技能目录。Codex 用户目录为 `~/.agents/skills/`，Claude Code 为 `~/.claude/skills/`。打开新会话即可按自然语言调用。

## 从本地素材建单

例如用户说「用这五张图和三段配音做十秒 H3。修表师、邮差、学徒分别用第一、二、三段声音，做好后下载到当前目录。」

1. 选一个工作目录，把示例复制为 `shot.json`，按用户提供的文件改写路径。相对路径从 JSON 所在目录解析。示例中的人物、台词和文件名均为虚构，八个素材文件由用户提供。[H3 镜头规格](examples/h3.json)
2. 建单前列出素材的对应关系。保留用户给的数组顺序，尤其是音频顺序。在提示词中把图片序号、角色、音频序号及其台词写在一起。用户给的绑定明确即可采用，有歧义时再询问。对示例应列出下表。

   | 图片 | 角色或用途 | 参考音频 | 台词 |
   | --- | --- | --- | --- |
   | 图 1 | 修表师 | 音频 1 · 01-修表师.mp3 | 几点了？ |
   | 图 2 | 邮差 | 音频 2 · 02-邮差.mp3 | 刚好三点。 |
   | 图 3 | 学徒 | 音频 3 · 03-学徒.mp3 | 我来上弦！ |
   | 图 4 | 钟表铺场景 | — | — |
   | 图 5 | 怀表道具 | — | — |

3. 只要求准备或检查规格时，交付规格和对应表即可。用户要求实际生成时，`video create` 会上传素材并提交可能计费的任务。取本次镜头独有的运行名，记录返回的 `task_id`。

```sh
ifytv video create --spec ./shot.json --run shot-07 --json
ifytv video status --run shot-07 --json
ifytv video wait --run shot-07 --json
ifytv video download --run shot-07 --out ./shot-07.mp4
```

提交应答中断时，保留原素材、规格和运行名，用同一条创建命令恢复。已知任务 ID 时也可直接续查。等待中断后再次运行 `video wait`。若任务已失败，带上任务 ID 和错误原因给用户。下载时选用户指定的输出位置。模型可能混淆声线或说话人，需要逐句试听成片才能评价实际配音。

## 把已有成片导入云画布

例如用户说「把昨天那个任务放到这个云画布」，并给出项目链接和任务 UUID。从链接确认目标项目 UUID。以下两个 UUID 均为示例，实际操作时替换成用户指定的值。

```sh
ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --task 11111111-1111-4111-8111-111111111111 --json
```

同一台机器上若只给了运行名，用 `ifytv canvas import --project 22222222-2222-4222-8222-222222222222 --run shot-07 --json`。导入直接使用已有任务。若任务仍在处理中，按原 ID 等待成功再导入。若返回 `authorization_url`，请用户在对应站点的浏览器中授权该项目，再对同一项目和任务重试导入。`duplicate: true` 表示可打开已导入的视频卡。

交付用户请求的结果。生成时附任务 ID，下载时给出实际 MP4 路径，导入时给出响应中的 `project_url` 与 `node_id`。
