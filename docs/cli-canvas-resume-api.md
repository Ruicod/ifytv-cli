# 接手续做云画布的站点接口

本约定支持 Codex 和 Claude Code 接手网页上已制作过的项目。Agent 读取一份完整的画布结构，按需取得媒体，查已有任务或在同一张画布补缺。三项接口均使用已批准的设备授权，并按目标项目核对权利。读取只授予设备当前身份对这个项目已有的查看权限。续做需要对应项目的编辑和视频生成权限。

## 读取整张画布

`GET /api/v1/cli/projects/{id}/snapshot` 返回当下有权查看的结构。复用画布仓库的项目快照、素材归属和节点生成记录，按项目作只读投影。返回项目 ID、标题、正整数 `revision` 和显式 `can_edit`。每页都含 `groups`、`nodes`、`connections` 三个数组，空页也返回空数组。

```json
{
  "project_id": "003d9550-c1bb-4892-84ff-307fb7a2e5b6",
  "title": "第六集",
  "revision": 37,
  "can_edit": true,
  "groups": [{"id": "sc014", "title": "SC014"}],
  "nodes": [
    {"id": "script-06", "kind": "text", "title": "剧本", "text": "夜。邮差站在门外。"},
    {"id": "video-014", "kind": "video", "group_id": "sc014", "title": "进门镜头", "prompt": "从走廊向门内推进",
     "assets": [{"id": "db554bb8-cc31-4279-8fad-5c0f1e816e99", "kind": "image", "mime_type": "image/png", "role": "input", "name": "角色参考"}],
     "video_settings": {"model": "实际模型ID", "mode": "omni", "duration": 10, "ratio": "16:9"},
     "generation": {"status": "draft"}}
  ],
  "connections": [{"id": "script-to-shot", "source_node_id": "script-06", "target_node_id": "video-014"}]
}
```

分组通过可选 `parent_id` 嵌套。节点可指向 `group_id`，可以带可显示的 `title`、`text` 和 `prompt`。节点 ID 原样保留。连线可带 `source_port`、`target_port`。资产仅投影已授权的 ID、类型、可显示名称、MIME 类型及用途。已有 `mime_type` 时优先用于选择文件扩展名。类型为 `image`、`audio`、`video` 或 `text`，用途为 `input`、`output` 或 `attachment`。选片使用项目的真实选择标记，明确选用时给 `selection: "selected"`，明确的候选给 `"candidate"`。节点标记描述候选卡，同卡有多个片段时，各个 `role: "output"` 素材带自己的 `selection`。按网页保存的选用项或主视频 ID 映射到实际素材。未知选择时省略。目标节点的 `input` 素材数组保留同一类型下的原有参考顺序，含从连线取得的真实输入。可选 `video_settings` 仅投影网页已选择的 `model`、`mode`、`duration`、`ratio`、`resolution`、`face_check` 和 `generate_audio`，缺项省略。

`generation.status` 为 `draft`、`claimed`、`submitted`、`uncertain`、`queued`、`submitting`、`processing`、`succeeded`、`failed` 或 `unknown`。只有确认没有提交且卡可继续时才给 `draft`。用户在网页开始的任务也纳入。生成期间可给实际阶段、百分比和机器错误码。延续网页已提交任务时，由站点的任务回写流程更新原卡，即使原网页已关闭。读取请求只呈现回写状态。模型已完成而素材尚未保存时，可保留处理中并给出保存阶段。仅当此设备身份可以用已有 CLI 查询时才提供 `task_id`。状态不明的旧节点保留为 `unknown`。

若需分页，返回不透明的 `next_cursor`，客户端在相同项目上用 `?cursor=<值>` 追到末页。游标绑定授权账号、项目和同一份版本视图，各页保持相同项目、标题、版本和编辑权限。版本变化无法延续时返回 `409 canvas_snapshot_changed`。所有节点、分组与连线必须完整，允许在不同页返回关联项。响应单页不超过 4 MiB，使用可合并的普通 JSON。投影排除浏览器凭据、插件内部配置、签名 URL 及无权查看的资产。响应不进入共享缓存。旧站点缺路由使用 404，真实项目不存在可返回 `canvas_project_not_found`。项目需要授权时用 `403 cli_grant_required`。

## 按需读取卡片素材

`GET /api/v1/cli/projects/{project_id}/assets/{asset_id}/download`。服务器同时校验项目查看权限、目标资产与项目的关联及资产权限，即使已经知道素材 ID 也重新检查。返回同一路径上的完整 200 响应，提供可信的 `Content-Type`、`Content-Length` 和原始文件的 `X-IFYTV-Asset-SHA256`。常见的图片、音频、视频和纯文本资产按实际 MIME 类型传输。正文是原始媒体字节。凭据和私有素材都沿用同站访问控制。

## 带版本创建或续做视频

`POST /api/v1/cli/projects/{project_id}/continuations` 复用现有视频任务流程。使用既有的 `Idempotency-Key` 和视频规格，附快照版本以及可选目标节点。

```json
{
  "expected_revision": 37,
  "target_node_id": "video-014",
  "spec": {"model":"实际模型ID","mode":"omni","duration":10,"ratio":"16:9","prompt":"从走廊向门内推进"},
  "reference_names": []
}
```

服务器按当前编辑权限定位原项目。对同一账号、项目和幂等键，先查已受理回执，匹配原目标、版本和请求后重放原任务与节点。再在同一项目事务里检查版本和目标卡当前状态，并领取生成占用。版本过期但未受理时返回 `409 canvas_revision_changed`，且没有建单、预扣或落卡。已有活动或未确认的提交、已选片段、已保存成片或有效编辑锁时，返回 `409 canvas_node_not_resumable`，保留现状。没有活动任务或受保护结果的空卡可续做。已确认失败且符合相同保护条件的卡，可用新的尝试在原卡继续。在同一卡更新进度并保存视频，同步给协作者。续做回执要绑定现有 `/videos` 成片保存流程，重复保存复用同一 `node_id`。没有 `target_node_id` 的请求可在该版本上新建一张缺失的视频卡。成功回执与现有视频生成一致，含实际 `id`、`status`、`project_id` 和 `node_id`。

续做上传图像、音频和参考视频使用项目私有的 `POST /api/v1/cli/projects/{project_id}/media/uploads`。仅接收该项目本次授权可使用的暂存引用。该路由的 `file` 表单和响应沿用媒体生产协议，服务端解析私有暂存对象并按模型要求转交。待提交运行恢复时，CLI 可对已验证的同站暂存回执发起授权的单字节 Range GET 来检查是否存在，返回 200 或 206，有权限但已过期时返回 404 或 410。已受理请求的幂等回执独立于暂存文件的生命周期。对于尚未受理的续做，若原暂存对象已过期，先确认没有已受理回执、生成占用或建单，再以 `410 canvas_reference_expired` 明确拒绝。客户端可读最新项目并用新尝试名重新准备同一缺失镜头。

普通单镜生成继续走既有 `/runs`。站点和 CLI 联调时至少覆盖人工做了一半的项目、跨页引用、原任务查询、原卡续做、两台设备并发、原响应丢失复放、只有查看权限、素材跨项目拒绝与授权撤回。
