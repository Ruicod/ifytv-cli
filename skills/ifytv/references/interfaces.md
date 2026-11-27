# 当前 CLI 接口与字段

以开发分支源码的帮助和当前站点回执为准。命令的 `--json` 让 stdout 成为单个 JSON 对象。stderr 是运行进度。即使命令失败也解析 stdout，因为它可能包含原任务或卡片。检查退出码，错误用 `error.code`、`error.message`、可选 `error.http_status`，任务本身的原因用顶层 `error_code`。状态和百分比缺失时标为未知。本参考里的 `state/*` 字段由 Agent 写入，不能加入 CLI 的严格 JSON 规格。

命令中的 `AUTH` 是本次 `authorization_id`，`PROJECT` 是原站点画布链接或项目 UUID，`PROJECT_ID` 只取项目 UUID，`TEAM` 来自可写队伍列表，`REVISION`/ `NODE` 来自最新快照。 `ASSET` 和 `TASK` 分别取素材和任务 UUID，`KIND` 取原素材任务的 `image` 或 `speech`。运行名分别取状态里的本次步骤、建画布和发布运行名。`SPEC` 是当前素材或视频规格，`EPISODE_SPEC` 是导出规格。 `SNAPSHOT`、`OUT`、`FINAL` 取作品记录里的快照、素材/片段和成片路径。

| 用途 | 回执关键字段 |
| --- | --- |
| `--version` 或 `--help` | `version` 或 `help` |
| `auth login/status/wait` | `status`、`source`、`authorization_id`，待授权时可有 `authorization_url` 和 `user_code` |
| `models` | `models[]`，包含 `id`、`modes`、`ratios`、实际时长和按类型的素材限制。是站点公开目录，账号权限仍以提交及媒体能力校验为准。 |
| `media capabilities` | `media_capabilities.image_models`、`voices[{id,name}]`、`publish_episode` |
| `canvas workspaces` | `workspaces[{id,name,kind,can_create_project}]`，响应已过滤可写团队 |
| `canvas create` | `project_id`、`workspace_id`、`workspace_kind`、`project_url` |
| `canvas inspect` | 无 `--out` 时是 `canvas`。带 `--out` 时从回执的 `path` 读完整快照文件，顶层另有 `bytes`。已有文件不会被覆盖。 |
| `canvas fetch` | `project_id`、`asset_id`、`path`、`bytes`。验证服务端 SHA-256 和 MIME 对应扩展名。 |
| `media generate/fetch` | `task_id`、`status`、`path`、`bytes`，按需有 `phase`、`progress`、`error_code`。`generate` 提交后等待并下载，`fetch` 用项目、原种类与任务 ID 恢复。 |
| `video create/status/wait/download` | `task_id`、状态/阶段，在已有运行记录可解析时有 `project_id` 与 `node_id`。下载另有 `path`、`bytes`。`wait --run` 可试保存原卡；`wait --task` 本身不定位原卡。 |
| `canvas import` 或 `media publish` | `project_id`、`node_id`、`project_url`，按操作给 `asset_id`、`duplicate`。 |
| `episode check/export` | `status: ready` 或 `status: succeeded`，合成回执含 `path`、`bytes`、`duration_ms`、`shot_count`。 |

## 鉴权与定位

`--run` 适用于本机原视频运行。跨设备已有任务 UUID 时优先 `--task`，两者不能同用。设备授权首次使用时让用户在自己的浏览器核对设备码与账号。通过现有 `IFYTV_BASE_URL` 选择 HTTPS 根地址。若 `source: environment`，`IFYTV_API_KEY` 已覆盖设备登录。仅在原任务使用这个 Key 且动作为其已授权的视频操作或导入时，省略 `--authorization-id`。需要快照、建画布或媒体能力时，在不继承该环境覆盖的子进程中以原账号完成设备批准，保留用户配置和 Key。带 `--authorization-id` 时不能同时向该进程传 Key。快照 `revision` 是无符号 64 位整数，读取和复写时保留全部数字，避免用 JavaScript Number 表示。

### 公开 rc.5 的单镜入口

按 [一行安装](https://github.com/Ruicod/ifytv-cli#一行安装) 安装与重启 Agent。安装后命令或版本异常时，从当前用户的 `.local/bin` 核对实际安装的二进制，Windows 文件名为 `ifytv.exe`。旧版 `--project` 可用同站链接里的 UUID。先读本机帮助。若用户明确只要本地视频、已确认版本为 rc.5 且成功读取的帮助里确实没有 `--no-canvas`，才执行旧版的无项目写法。开发版该写法会新建个人云画布。

```sh
ifytv video create --project "$PROJECT_ID" --spec "$SPEC" --run "$RUN" --json
ifytv video create --spec "$SPEC" --run "$RUN" --json
```

## 云端整集运行

在用户要求云画布整集交付并且第 2 阶段已经得到项目时使用。先查现有运行，判断同集是否已开始。大项目用回执中的 `next_cursor` 继续查询。只有明确返回 `episode_orchestration_unavailable`，且当前集没有已提交或待核对的云端请求时，才用主流程第 4–6 阶段处理。鉴权失败、权限不足、回执异常和待核对请求均按恢复规则处理。

```sh
ifytv episode status --project "$PROJECT" --episode-id "$EPISODE_ID" --authorization-id "$AUTH" --json
ifytv episode status --project "$PROJECT" --episode-id "$EPISODE_ID" --cursor "$CURSOR" --authorization-id "$AUTH" --json
ifytv episode plan --project "$PROJECT" --revision "$REVISION" --spec "$PLAN" --request-id "$PLAN_REQUEST" --authorization-id "$AUTH" --json
ifytv episode quote --project "$PROJECT" --run-id "$RUN_ID" --plan-hash "$PLAN_HASH" --authorization-id "$AUTH" --json
ifytv episode resume --project "$PROJECT" --run-id "$RUN_ID" --revision "$REVISION" --plan-hash "$PLAN_HASH" --request-id "$RESUME_REQUEST" --quote-id "$QUOTE_ID" --currency "$CURRENCY" --budget-units "$BUDGET_UNITS" --authorization-id "$AUTH" --json
ifytv episode status --project "$PROJECT" --run-id "$RUN_ID" --authorization-id "$AUTH" --json
ifytv episode status --project "$PROJECT" --run-id "$RUN_ID" --cursor "$CURSOR" --authorization-id "$AUTH" --json
ifytv episode status --project "$PROJECT" --request-id "$REQUEST_ID" --operation "$OPERATION" --authorization-id "$AUTH" --json
```

1. **查原集。** `EPISODE_ID` 是作品持续使用的 UUID。有云端运行时读取原 `RUN_ID`。本地记录丢失时可省略 `--episode-id` 列该项目的运行，再按实际剧集核对。列表回 `episode_runs.items[]`。只有空白计划或已确认的新版本才规划下一次写入。商定新的场次/镜头范围之后固定 UUID 与计划版本，不重建整个原画布。
2. **保存计划。** `PLAN` 是云端计划 JSON，和本地 `episode export` 规格分开。`PLAN_REQUEST` 是提交前已落盘的独立 UUID。`episode plan` 仅保存计划。回执 `episode_run` 给实际 `run_id`、`revision`、`plan_hash`、`next_action` 和镜头绑定。遇到版本冲突重新读快照，识别用户变化后再决定是否提交新计划。任何新尝试先核对原请求。
3. **报价和恢复。** 以原计划散列报价。`episode_quote` 给 `quote_id`、`currency`、`lines[]`、`expires_at` 和可证明时的 `max_total_units`。`BUDGET_UNITS` 是获准的同币种整数上限。上界未知、报价过期或超过批准额度时，保留计划及原运行，不开始新付费操作。允许继承已批准的额度时可省略新的 `--currency`/`--budget-units`，由站点验证原批准。需要只处理部分步骤时，在报价和恢复的命令中重复传相同的 `--step-id`。`RESUME_REQUEST` 是这次动作独立、已落盘的 UUID。
4. **跟进原运行。** `next_action.type` 为 `poll` 时按 `poll_after_seconds`（如有）有界查询。`reconcile` 先核对原受理和计费。`authorize` 处理权限，`fix_input` 只修所报输入，`review` 处理候选或质检。`execute` 表示可按现行计划、报价和授权发起动作。`done` 时查实际终态。等待结束只续查同一 `RUN_ID`。结果确认成功时使用 `artifact.node_id`、`artifact.asset_id` 以及返回的原卡 `project_url`。需要下载时，对该资产用 `canvas fetch`。按主流程逐句检查成片。
5. **保存回执。** 在 `state/episode.json` 保存 `episode_id`、`run_id`、`plan_version`、`plan_hash`、`revision`、`quote_id`、`billing`、`artifact`、`next_action`。在 `state/tasks.json` 保存步骤 ID、任务 ID、节点 ID、状态和原请求。`REQUEST_ID` 和 `OPERATION` 取该次请求原值（`plan` 或 `resume`）。回执丢失后用这组值查询 `episode_runs.lookup_state`。已受理的回执带原请求摘要。只有同设备的请求记录匹配、回查仍不确定且原授权足以覆盖首次执行时，才给原 `plan` 或 `resume` 加 `--replay`。服务端确认 `not_accepted` 并封闭原键后，才评估下一次获准提交。

### 云端计划字段

| 结构 | 字段 |
| --- | --- |
| 整集 | `episode_id` UUID、`plan_version` 正整数、`ratio` 为 `9:16` 或 `16:9`、`scenes[]`、`entities[]`、`shots[]`、`steps[]`。缺项集合用空数组。 |
| 场次与实体 | `scenes[]` 每项 `scene_id`、`order`、`script`。`entities[]` 每项 `entity_id`、`kind`（`character/location/prop`）、`version`、`description`、`baseline_asset_ids[]` 和可选 `voice_id`。 |
| 镜头与对白 | `shots[]` 每项 `shot_id`、`scene_id`、`order`、`duration_ms`、`description`、可选 `target_node_id`、`reference_bindings[]` 和 `lines[]`。对白含 `line_id`、`character_id`、`text`、`start_ms`、`end_ms`，可选 `voice_id` 和 `audio_asset_id`。 |
| 素材绑定 | `entity_id`、`version`、`kind`（`image/speech/video`）、从 1 开始的 `slot`。已有素材用 `asset_id`，生成态素材用 `from_step_id` 与 `output_slot`。 |
| 制作步骤 | `step_id`、`kind`（`image/speech/video/render/publish`）、`depends_on[]`、`config`、`output_slots[]`。如适用，绑定 `entity_id`、`shot_id` 或 `line_id`。步骤 ID 与输出槽位稳定。各模型字段采用站点版本对应的配置 schema。依赖按制作顺序无环。 |

## 素材与镜头规格

以下为字段样例。模型与音色 ID 用实际能力回执，路径用真实文件。文件写 UTF-8 JSON，相对路径以规格所在目录为准。生图最多 8 张本地参考图。图和语音下载分别使用 `.png` 与 `.mp3`。续做要求视频参考为本地文件。项目素材下载按快照给出的 MIME 和实际扩展名。

```json
{"kind":"image","model":"actual-image-model-id","ratio":"9:16","prompt":"画风、构图、角色与一致性要求","references":["./assets/characters/postman/v1/front.png"]}
```

```json
{"kind":"speech","voice":"actual-voice-id","text":"最后一封信，是寄给我的。","emotion":"平静"}
```

```json
{"model":"actual-video-model-id","mode":"omni","duration":10,"ratio":"9:16","prompt":"邮差用图1，0.5至4秒按音频1说「最后一封信，是寄给我的。」","images":["./assets/characters/postman/v1/front.png"],"audio":["./assets/dialogue/line01.mp3"]}
```

视频要求 `model`、`mode`、`duration`（秒）、`ratio`、`prompt`。可选 `resolution`、`face_check`、`generate_audio`、`images`、`audio`、`videos` 或旧单引用 `video_url`。若写 `n` 只能为 1。实际输入数量、模式、尺寸和时长必须通过该模型的目录校验。[H3 五图三音完整示例](../examples/h3.json)

## 整集合成规格

```json
{"ratio":"9:16","shots":[{"video":"shots/s01.mp4","duration_ms":10000,"dialogue":[{"audio":"assets/dialogue/line01.mp3","start_ms":500,"end_ms":4000,"text":"最后一封信，是寄给我的。"}]},{"video":"shots/s02.mp4","duration_ms":10000},{"video":"shots/s03.mp4","duration_ms":10000}]}
```

支持 `9:16`/`16:9`，至多 24 镜、128 条对白、120 秒。按实测长度匹配镜头和台词区间。对白不得重叠或移到镜头之外。当前导出器以独立对白音轨为声源，烧录同一批对白的字幕，不自动保留片段音频、场景音效或混音。用户要求原声时，确认是否有可单独交付的完整片段，或使用已提供的合适剪辑能力保留原声；当前 `episode export` 的结果不可冒充原声成片。预检验证 FFmpeg 与 ffprobe 等本地工具，仍要检查本次字幕字体实际呈现。
