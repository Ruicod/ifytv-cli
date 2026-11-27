# 整集制作所需的站点接口

IFYTV Agent 使用设备授权调用生图、配音和成片回写。项目仍由现有个人或团队画布接口创建，视频镜头继续通过该项目的 runs 接口生成并在原卡播放。整集用客户端导出的 H.264/AAC MP4。下文是客户端开发版的接口约定。

## 设备与项目权限

所有媒体请求使用当前设备授权的 Bearer。站点需要按授权确认账号、相应的生图及配音权益，并在每个项目请求上核对该账号对目标项目的访问与编辑权限。项目单独授权时用 `403 cli_grant_required`，客户端会返回同站授权页。服务端用现有权限和额度规则处理媒体任务。原视频 Key 继续有原有范围。

## 能力发现

`GET /api/v1/cli/media/capabilities` 返回当前账号实际可用的能力。总是返回生图模型和音色数组，无可用项时返回空数组。

```json
{"image_models":["image-model-id"],"voices":[{"id":"voice-id","name":"音色名称"}],"publish_episode":true}
```

## 项目私有上传

`POST /api/v1/cli/projects/{project_id}/media/uploads` 使用 multipart 的 `file` 字段上传参考图或整集 MP4。服务端在读取并存储文件前核验设备、账号和项目编辑权限。站点保存有账号与项目归属的临时对象，返回 `url`、`media_type` 和 `size`。URL 形如 `https://站点/api/v1/cli/projects/{project_id}/media/uploads/{上传UUID}`，包含同一项目，且没有查询参数或片段。后续任务只接受本项目的有效上传回执。对象由站点内部解析，需要对外部模型提供素材时，由服务端签发该任务所需的限时地址。整集发布后沿用项目素材访问控制。

## 生图与配音

`POST /api/v1/cli/projects/{project_id}/media/tasks` 接受以下两种请求。参考图列表与摘要一一对应，均来自该项目的私有上传回执。

```json
{"kind":"image","model":"image-model-id","ratio":"9:16","prompt":"角色参考图","references":["本项目的私有上传地址"],"reference_sha256":["64 位十六进制 SHA-256"]}
```

```json
{"kind":"speech","voice":"voice-id","text":"今晚的最后一封信。","emotion":"平静"}
```

首次创建返回 `201`，重放返回 `200`。响应包含任务 ID、任务种类、状态。`GET /api/v1/cli/projects/{project_id}/media/tasks/{task_id}` 返回同样的状态结构。状态取值为 `queued`、`running`、`succeeded` 或 `failed`。可选 `phase`、`progress` 和 `error_code`。生成成功后，`GET /api/v1/cli/projects/{project_id}/media/tasks/{task_id}/download` 返回完整 PNG 或 MP3 字节。保持已授权项目中的原任务与成品可按任务 ID 取回，即使当前模型目录不再列出原模型或音色。下载路由需要返回同站的 `200` 与完整 `Content-Length`，禁止分段响应和跨站重定向。格式为 `image/png` 或 `audio/mpeg`，单文件上限 32 MiB，PNG 上限 1600 万像素。下载仍需检查任务归属和项目权限。

```json
{"task_id":"56ffe246-d3b1-42f7-ad4e-9f3bc149f7d2","kind":"image","status":"queued"}
```

## 整集成片卡

`POST /api/v1/cli/projects/{project_id}/media/episodes` 将已上传的完整 MP4 保存为这张画布中的成片卡。保存应沿用现有云素材存储、去重、配额和协作通知。只在站点已实现且账号范围允许时返回 `publish_episode: true`。

```json
{"title":"folded-letter-final","video_url":"本项目的私有上传地址","video_sha256":"64 位十六进制 SHA-256"}
```

```json
{"project_id":"003d9550-c1bb-4892-84ff-307fb7a2e5b6","node_id":"episode-video","asset_id":"db554bb8-cc31-4279-8fad-5c0f1e816e99","duplicate":false}
```

## 断线恢复与验收

两个 POST 都使用 `Idempotency-Key`。客户端按站点设备身份、项目、操作和运行名产生稳定键。服务端以授权账号和项目隔离回执。媒体地址必须对应该身份已上传并允许在本项目使用的对象，从存储读回字节核对摘要。比较镜头配置、台词、音色和媒体内容时使用摘要，允许相同字节取得新的临时上传链接。冲突参数返回 `409 idempotency_conflict`。相同请求并发、响应丢失或续签时复用原任务或成片卡。结合已有视频的请求幂等处理，断线前已完成的镜头不再次收费。

联调从一条只包含故事的提示词开始，核对同一张画布上的参考素材和镜头回执，整集导出的长度、实际对白和字幕，以及刷新后能播放的成片卡。对没有目标项目编辑权限的请求，确认拒绝后的私有上传存储里没有新对象。重试一项素材与一次回写，分别确认原任务和原卡。缺生图或配音权限时检查没有提交对应任务。
