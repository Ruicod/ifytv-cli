---
name: ifytv
description: 把一句故事或现有剧本做成短剧、漫剧或单镜视频，在 IFYTV 云画布上完成分镜、角色与场景资产、配音、逐镜生成、整集合成、续做、下载和导入。可用于未指定平台的短剧或漫剧成片需求。不适用于通用剪辑、第三方画布或单纯剧本润色。
---

# IFYTV 云画布制作

先确定交付范围。剧本、分镜或提示词只需要文本时，执行第 3 阶段。只查看画布或梳理缺口，执行第 1、2 阶段并交付现状。只做素材、单镜、下载、导入或本地合成时，只运行相关阶段及其前置步骤。用户给画布链接就用原画布，无链接默认建到「我的画布 → 云端画布」，明确指定团队时才选择该团队。只有用户明确只要本地文件时使用本地模式。默认用一张画布承载一集。

从作品目录执行命令。大写变量见 [接口字段](references/interfaces.md)，作为独立参数传入。预先创建输出父目录，首次使用稳定且未占用的文件名。更新作品状态或提交前取得 [本地写锁](references/recovery.md#作品记录和并发)，只读查看可另存快照。按阶段读取 [资产一致性](references/assets.md) 和 [状态与错误](references/recovery.md)。以下是开发版 CLI 命令，云操作依赖站点支持。公开 rc.5 的单镜用法见接口字段。

## 1. 确认能力和授权

**前置** 用户要求读取云画布或操作媒体。使用原画布同站点和原账号。剧本任务跳过登录。

```sh
ifytv --version --json
ifytv --help --json
ifytv models --json
ifytv auth status --json
ifytv auth login --json
ifytv auth wait --authorization-id "$AUTH" --json
ifytv media capabilities --authorization-id "$AUTH" --json
ifytv episode check --json
```

**执行与输出** 按需运行。仅在需要设备授权且未授权时调用 `auth login`。显示本次 `authorization_url` 和 `user_code` 后，立即用同一个 `authorization_id` 等待。设备命令沿用 `AUTH`。若 `source: environment`，按 [鉴权说明](references/interfaces.md#鉴权与定位) 处理。模型用 `models`，生图和配音用 `media_capabilities`，本地整集预检要求 `status: ready`。

**失败与重试** 按 [状态与错误](references/recovery.md) 处理。目标能力缺失时说明缺哪一步，可继续写剧本或复用用户已有的素材。云端整集运行使用报价，旧逐镜接口没有报价。用户给出硬预算且无法验证时，暂停付费步骤并保存计划。单纯读取或写文本继续。

**写入** `state/episode.json` 的 `site`、`authorization_id`、`capabilities`、`budget`、`stage`。

## 2. 定位画布并恢复

**前置** 已固定设备授权。有链接或恢复记录时先读原画布。只在需要新云画布时执行创建分支。

```sh
ifytv canvas inspect --project "$PROJECT" --out "$SNAPSHOT" --authorization-id "$AUTH" --json
ifytv canvas create --workspace personal --run "$PROJECT_RUN" --authorization-id "$AUTH" --json
ifytv canvas workspaces --authorization-id "$AUTH" --json
ifytv canvas create --workspace "$TEAM" --run "$PROJECT_RUN" --authorization-id "$AUTH" --json
```

**执行与输出** 先建立快照的父目录，`SNAPSHOT` 每次用未占用的路径。快照含 `revision`、`can_edit`、`groups`、`nodes`、`connections`。保留版本完整整数，按组和实际卡定位场次。团队只用名称或 UUID 唯一匹配的可写团队。新建前保存 `PROJECT_RUN`，确认 `project_id` 后立即给用户画布链接。已有任务 ID 且只要求查询时可直接到第 5 阶段。

**失败与重试** 无编辑权限时可交付只读现状。快照过期重新读取。创建回执丢失时以同工作区、运行名和授权重放。续做时缺完整快照则暂停该画布写入。个别旧卡状态不明时，保留原卡并处理已确认的独立步骤。详见 [恢复顺序](references/recovery.md#恢复顺序)。

**写入** `project_id`、`project_url`、`workspace`、`project_run`、`snapshot_path`、`revision`、`can_edit`。已有画布先记录已选片段、在途任务及不确定节点。

## 3. 固定剧本、分镜和资产表

**前置** 有用户故事或剧本。续做已有项目时以快照和用户指定范围为准。

**执行与输出** Agent 将故事整理成场次、镜头和对白。按首次创作默认约 30 秒、9:16、3–5 个生成镜头，保留用户指定的时长与画幅。每镜包含稳定 `shot_id`、场次、时长、人物/场景/道具 ID、动作、构图、衔接及对白。按镜头查已缓存模型目录，实际时长须落在可用规格内。只有源素材或关键连续性缺失时才做额外关键帧。详细约束见 [资产一致性](references/assets.md)。整集需要上云时，按 [云端整集运行](references/interfaces.md#云端整集运行) 查原集、存计划、报价、恢复和确认成片。站点支持并已采用云端整集时，生成、合成和回写跟同一云端运行，只取回或审核产物。明确缺云端入口且此集没有待确认云端请求时，再走下列逐镜流程。

**失败与重试** 故事约束和模型规格冲突时先调整未提交镜头。核对总时长、台词归属和资源依赖后，固定计划版本。续做时保留用户改过的文本和选片。

**写入** `state/plan.json` 的 `plan_version`、`scenes`、`shots`、`characters`、`locations`、`props`、`dialogue`。在 `state/episode.json` 记 `plan_path`、`stage` 与已映射的画布卡。

## 4. 准备参考图与逐句配音

**前置** 确认所需素材、模型和项目权限，优先用已有资产。生图和配音需要云项目。只交付本地文件时优先用已有素材或用户许可的本地工具，缺失且无法制作时记录缺项，保留本地模式。创建请求前将规格、输出路径和 `RUN` 记录为待确认。

```sh
ifytv canvas fetch --project "$PROJECT" --asset "$ASSET" --out "$OUT" --authorization-id "$AUTH" --json
ifytv media generate --project "$PROJECT" --spec "$SPEC" --run "$RUN" --out "$OUT" --authorization-id "$AUTH" --timeout 20m --json
ifytv media fetch --project "$PROJECT" --kind "$KIND" --task "$TASK" --out "$OUT" --authorization-id "$AUTH" --timeout 20m --json
```

**执行与输出** 带当前项目的身份取回所需原素材。按 [字段与示例](references/interfaces.md#素材与镜头规格) 写生图或配音 JSON。先定角色基准、场景和道具，必要时用这些图片生成分镜图。每条对白用同角色锁定的音色，验证文件和解码后记录实际长度。成功读取 `path`、`bytes`，有媒体任务时同时记录 `task_id`。

**失败与重试** 已知 ID 时用 `media fetch`。回执未知时，预算和授权能覆盖可能的首次扣费才重放原运行与规格。音频过长先调整镜头或对白，避免截断。参考不一致则复用同一基准补做，终止失败且允许再生成时才开新尝试。详细分支见 [状态与错误](references/recovery.md)。

**写入** `state/assets.json` 的实体版本、素材 ID、文件、SHA-256、音色和实际时长。任务写 `state/tasks.json`。

## 5. 逐镜提交并检查原卡

**前置** 已校验规格、素材顺序和许可范围。已有画布按 [恢复顺序](references/recovery.md#恢复顺序) 逐卡分类。已选成片取回，在途任务续查，明确空卡或可重做的失败卡才能续做。已有项目每次补镜前确认 `can_edit: true` 并刷新 `REVISION`，新项目核对创建回执。

```sh
ifytv video status --run "$RUN" --authorization-id "$AUTH" --json
ifytv video status --task "$TASK" --authorization-id "$AUTH" --json
ifytv video create --project "$PROJECT" --revision "$REVISION" --node "$NODE" --spec "$SPEC" --run "$RUN" --authorization-id "$AUTH" --json
ifytv video create --project "$PROJECT" --revision "$REVISION" --spec "$SPEC" --run "$RUN" --authorization-id "$AUTH" --json
ifytv video create --project "$PROJECT" --spec "$SPEC" --run "$RUN" --authorization-id "$AUTH" --json
ifytv video create --no-canvas --spec "$SPEC" --run "$RUN" --authorization-id "$AUTH" --json
ifytv video wait --run "$RUN" --authorization-id "$AUTH" --timeout 20m --json
ifytv video wait --task "$TASK" --authorization-id "$AUTH" --timeout 20m --json
ifytv video download --task "$TASK" --out "$OUT" --authorization-id "$AUTH" --json
ifytv canvas import --project "$PROJECT" --task "$TASK" --authorization-id "$AUTH" --json
```

**执行与输出** 根据目标只选择一条创建命令。无 `--node` 的版本守卫用于确认缺卡的镜头。无 `--revision` 的画布命令用于新画布或明确的单镜新增。本地模式只用于用户明确指定的本地视频。下载使用原任务。`canvas import` 是写入，用于用户指定的已有成片，或确认回执绑定同一项目、任务与卡的 CLI 成片补保存。网页任务按站点回写，等待后重读快照。报告实际 `phase`、`progress` 和 `node_id`。按资产的 `selection: selected` 取用，选片需有用户标记或委托。

**失败与重试** 超时先恢复任务或原运行。收到版本冲突或原卡不可继续时重读快照，核对旧请求是否受理。保留运行及完整参数，禁止用新名字绕过未知结果。下载后检查片段能播放，涉及对白时逐句看嘴型、听声音，无法试听时记为未验证。

**写入** `state/tasks.json` 每镜的 `run`、`attempt`、`spec_path`、`expected_revision`、`task_id`、`node_id`、`status`、`path`、`error_code`。在 `state/plan.json` 记录所选片段、审核和衔接帧。

## 6. 合成与回写

**前置** 全部采用片段已在本地，镜头顺序明确，配音和字幕时间按真实片段核对。用户要保留片段原声时，先查 [导出限制](references/interfaces.md#整集合成规格)。

```sh
ifytv episode export --spec "$EPISODE_SPEC" --out "$FINAL" --json
ifytv media publish --project "$PROJECT" --file "$FINAL" --run "$PUBLISH_RUN" --authorization-id "$AUTH" --json
```

**执行与输出** `episode export` 返回 `path`、`bytes`、`duration_ms` 和 `shot_count`。核对播放、对白顺序和字幕。`media_capabilities.publish_episode` 为 `true` 且需要回写时，用同一成片文件、同一发布运行名，返回 `project_url`、`node_id` 和 `asset_id`。站点只能播放逐镜时，给用户本地整集和逐镜画布链接。

**失败与重试** 合成前保留输入和规格。若已有输出，校验与记录一致后复用。合成失败修复本地输入或环境。回写失败保留本地 MP4，补回写时沿用原项目、字节、发布运行和账号。

**写入** `state/episode.json` 的 `export`、`publish`、`stage`，记录规格、MP4 路径、时长、SHA-256、画布卡和验收结果。

## 最小验收

- [ ] 一句故事产生可校验的剧本和 3 个镜头，沿用同一角色基准图及音色。
- [ ] 已有链接写入原项目。无链接创建到个人云端，明确指定团队才创建到团队。
- [ ] 已选片段复用，在途镜头按原 ID 查询，确认缺失时才生成；并发、断线重放无重复建单或扣费。
- [ ] 每句台词核对说话人、音色、口型和字幕；实际不能核对的项目记录为未验证。
- [ ] 整集 MP4 能播放，回写后刷新能从成片卡播放，重复发布返回同一卡。
- [ ] 无权限、硬预算无报价和未知任务状态都返回具体阻塞，已有进度可恢复。
