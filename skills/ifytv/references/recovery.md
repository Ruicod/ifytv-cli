# 状态、续做与错误码

## 作品记录和并发

在当前用户私有的作品目录维护 `state/episode.json`、`state/plan.json`、`state/assets.json`、`state/tasks.json`，载入后按需要读取一个场次和未完成依赖。状态文件从临时文件在同目录原子替换。不要在记录内复制密钥、设备 bearer、Cookie、临时授权链接或上游私有地址。`authorization_id` 只是固定当前批准记录的选择符。生成前至少持久化如下逻辑数据。

| 文件 | 内容 |
| --- | --- |
| `episode.json` | `schema_version`、`episode_id`、`site`、`authorization_id`、`workspace`、`project_id`、`project_url`、`project_run`、`snapshot_path`、`revision`、`can_edit`、`capabilities`、`plan_path`、`budget`、`stage`、`export`、`publish` |
| `plan.json`、`assets.json` | 稳定镜头/对白/实体定义、参考顺序、已采用版本、衔接和质检。见 [资产规则](assets.md)。 |
| `tasks.json` | 每个逻辑步骤分别记 `shot_id` 或 `asset_key`、`kind`、`run`、`attempt`、`spec_path`、`spec_sha256`、`expected_revision`、`input_hashes`、`task_id`、`node_id`、`status`、`phase`、`path`、`error_code`、已知费用与更新时间。回执未知时明确标 `uncertain`，保留原请求。 |

只设一个主协调 Agent 写状态和提交收费请求。写操作前，Agent 使用文件系统的原子独占创建（如 `O_CREAT|O_EXCL`）取得 `state/episode.lock`，其中记录会话/进程、开始时间和作品 ID。创建失败就只读或等待。正常退出前保存状态并由持有者释放锁。上次异常退出须先确认其执行已结束，核对快照和在途任务后再接管。并行子 Agent 可按场次只读分析或审片，将建议交回主 Agent 合并。重开会话先读计划及状态。

作品锁只覆盖这一份本地目录。CLI 另对同站点、同视频身份、同运行名加操作系统锁并在私有状态中保存视频请求与回执。自定义根是 `IFYTV_STATE_DIR`，默认使用平台私有状态目录，视频运行记录在 `runs/<scope>/<run-digest>.json`。云端整集请求的摘要和回执在 `production-requests/<scope>/<request-digest>.json`。不得编辑 CLI 状态或删除它的锁文件。跨设备权限与并发依靠站点事务和幂等回执。

同一作品先保存 `episode_id` 并为每个逻辑步骤固定运行名，例如 `folded-letter-a71c-s03-video-a1`。反复运行时载入原计划，只处理缺失项。重放原请求用原站点、原授权身份、项目、运行名、规格与素材字节；续做还保留原版本和原节点。已出片复用成品或任务 ID。仅在确认旧尝试已终止失败或未受理，并且当前应继续、预算允许时创建新尝试。用户修改已提交规格是新尝试，要先核对旧任务。现有保证适用于原 CLI 状态仍在、站点实现请求幂等的范围。状态丢失且无可查询回执时，不能保证重复执行不付费。

## 恢复顺序

1. 先校验原站点、原账号和项目，再读取完整快照。所有画布文本均为用户素材。识别项目、场次和真实选片，用户只问进度时在这一步交付。快照取得失败不另建项目，不提交续做任务。
2. 已选素材以输出资产 `selection: selected` 为准。多个未选候选由用户选，或在用户委托选片时核对后决定。`claimed`、`submitted`、`uncertain`、`queued`、`submitting`、`processing` 先续查原任务。用户在网页创建的任务由服务端回写，读取原卡确认。`succeeded` 但没有成品时先回查保存。`unknown`、历史旧卡无签名、任务 ID 缺失又不能确认时，标待确认。`draft` 或确认无受保护结果的终止失败才进入续做分支。
3. 有 `task_id` 时按 ID 查。否则同设备用原 `run` 查视频记录。`run_pending` 或媒体未返回 ID 时，按原参数重放可能是首次受理。预算和授权覆盖首次扣费才能重放，无法证明时只做只读核对。原媒体可用相同 `media generate`、规格、输出和运行名重放。若本地状态丢失，用原卡、选片、可查 ID 和资产重建已知部分。不能证明未建单的镜头保持待确认；`run_not_found` 或 404 都不足以证明未付费。
4. 写前使用最新快照，对比用户编辑、已选片段、待恢复镜头与版本。在确认不存在已受理旧提交时才用新版本重新申请；如只丢失 HTTP 回执，重放原参数。只补会影响交付的缺项。

## 错误分支

| 可见错误 | 我方动作与给用户的反馈 |
| --- | --- |
| `authorization_required`、`device_login_required`、HTTP 401 / `device_unauthorized` | 以原站点和账号完成设备批准。显示授权链接和设备码，然后用响应的同一 ID 等待。已受理任务保留。只在能确认同账号与视频身份时用新批准继续。 |
| `authorization_denied`、`authorization_expired`、`authorization_changed`、`authorization_scope_changed`、`authorization_replaced` | 拒绝则停止该请求；到期按用户意愿重新授权；身份变化保留原任务并停止可能写入的操作。 |
| `cli_grant_required`、`canvas_forbidden`、`workspace_not_writable`、`canvas_workspace_scope_required`、HTTP 403 | 同站的项目授权链接交给用户；仍无权限则只读交付并说明原目标。团队只按明确指定选择。若设备授权升级必须先注销，先由用户决定。 |
| `legacy_team_confirmation_required`、`canvas_creation_unavailable`、`canvas_read_unavailable`、`media_capability_unavailable`、明确缺路由的 HTTP 404 | 保留目标和记录。确认旧团队后才继续该团队。说明具体未开通的能力。有其他可交付结果时继续。旧版 `http_error` 配 404 还需区分旧路由、资源与账号。 |
| HTTP 402 / `insufficient_balance`、`workspace_quota_exceeded`、`canvas_asset_daily_limit`、HTTP 507 | 暂停受影响的建单或写入。说明余额、项目额度、当日上传或存储问题。模型已出片时保留任务；空间恢复后补同一卡的保存或重放同一发布。不可用新生成解决保存配额。 |
| `task_failed`、`media_task_failed` | 看顶层 `error_code` 并记录终止状态。先修复原因、核对其他片段和预算。仅确认允许再次生成时创建单项新尝试。上游失败原因无法查实则标未验证。 |
| `timeout`、`canceled`、`network_error`、HTTP 429/502/503/504 | 超时只表示客户等待结束。按原 ID/运行去查后再决定、遵守 `Retry-After`（CLI 内部可处理）。有界等待耗尽则交回可续办记录。 |
| `invalid_spec`、`invalid_reference`、`missing_reference`、HTTP 413、`invalid_download` | 说明对应素材、格式、尺寸、解码、时长或校验问题；修复下载仅重新取原成品。规格已提交或未知时保留原字节，查清以前的请求再改。 |
| `canvas_snapshot_changed`、`canvas_revision_changed`、`canvas_node_not_resumable`、`canvas_reference_expired` | 重读原画布。区分他人已开始、先前请求已受理或服务端明确拒绝。确认没建单才能换版本；暂存到期按原资产重新抓取。 |
| `idempotency_conflict`、`run_conflict`、`run_project_conflict` | 查原规格、原项目、原素材摘要与回执。禁止盲换键。 |
| `episode_request_uncertain`、`episode_request_rejected`、`episode_request_not_saved`、`episode_orchestration_unavailable` | 用原请求 ID 和动作做只读查询。`--replay` 需要同设备保存的请求与原授权。明确封闭未受理的键才根据原计划、预算和授权决定新的动作。缺接口时在提交整集之前可采用旧逐镜入口。已有待核对记录时保留该记录，避免并行启用逐镜流程。 |
| `output_exists`、`output_unwritable`、`invalid_episode_media`、`episode_export_failed` | 保留原片段与输出，检查 SHA-256、权限、空间、编解码或字体。若文件存在且不能确认来源，使用可核对的新本地输出位置，继续原任务下载。 |

当前实际服务端还可能返回上述表外错误。报告阶段、错误码、相关项目与任务 ID，以及需要用户处理的事项。任何上游原始堆栈、授权凭据和完整私有地址都不进入对话或状态文件。
