# 角色、场景、道具与对白

## 固定计划和基准

在 `state/plan.json` 中为每个场次、镜头和对白分配稳定 ID。`shots[]` 按实际剪辑顺序排列。保存 `scene_id`、`shot_id`、`duration_ms`、`character_ids[]`、`location_id`、`prop_ids[]`、`description`、`camera`、`continuity_from`、`dialogue_ids[]` 和已知 `node_id`。对白保存 `line_id`、`character_id`、`text`、`start_ms`、`end_ms`、`voice_id`。用户改剧本时递增 `plan_version`，只重新判断受改动影响的镜头和衍生素材。

在 `state/assets.json` 中为每个实体保存 `entity_id`、`version`、`role`、`path`、`sha256`、`asset_id`（如有）、`source_task_id`（如有）和 `status`（`candidate` 或 `accepted`）。实体有多个图时记录角度和用途。服装或年龄变化建立显式版本，关联原实体 ID，禁止在制作中静默替换基准。优先使用原画布的已采用素材和用户提供文件。

角色基准图放 `assets/characters/<character_id>/<version>/`，场景和道具放 `assets/locations/<location_id>/<version>/` 与 `assets/props/<prop_id>/<version>/`。基准从可辨识的正面或半身开始，必要时加角度。场景额外记空间方向和可见入口，道具记持有角色和当前状态。已接受的基准保持不可变。每个角色固定 `voice_id`、语速和风格说明。分镜检查只比较当前故事要求保持一致的脸、服装、位置、光线和道具状态。

## 绑定到真实输入

| 阶段 | 真实字段 | 绑定要求 |
| --- | --- | --- |
| 生成关键帧 | 生图规格 `references[]` | 放本镜用到的基准文件，从角色到场景和道具按已登记顺序排列。前镜末帧只在需要承接姿势或空间时加。 |
| 生成视频 | 视频规格 `images[]`、`audio[]`、必要时 `videos[]` | 记录媒体类型内的从 1 开始的输入序号，依真实顺序在 `prompt` 里列角色、图号、音频号、台词和说话时段。文件数组始终用真实文件路径。 |
| 已有画布 | `nodes[].assets[]` 与 `connections[]` | 按素材级 `role`、`selection`、实际输入和同类型顺序找原引用。匹配失败时保留旧卡。 |
| 整集合成 | `shots[].dialogue[].audio`、`start_ms`、`end_ms`、`text` | 每句用已选声轨及实测时长。时间相对本镜起点。 |

在 `state/plan.json` 保存 `reference_bindings[]`，每项 `entity_id`、`asset_id` 或 `path`、`sha256`、`input_kind`、`input_index`。这些是作品记录字段。当前 CLI 的视频规格只接收上表里的真实输入字段，不接收 `reference_bindings`。只有模型宣告支持且模式允许时才传特定媒体类型。遇到槽位不足，保留人物、关键道具和台词的绑定，考虑拆镜或合规选用替代能力。无法证明绑定关系时先停该镜。

## 生成后检查与回退

- 生图后比较稳定角色特征。参考已足够时直接用在视频，避免为每镜多生成一张图。需要补关键帧时只补该镜，用同一基准。候选漂移时留存失败原因，再由允许的独立尝试重做。不得把漂移图提升为下一镜的基准。
- 逐句试听原音轨与成片，比较声音归属、文本、嘴动起止和串音。先确定参考 MP3 完整、实际时长放得下、台词无重叠。只有源音频 `audio[]` 且目录允许时才能声称输入已绑定。提示词绑定是软约束。后期用 TTS 覆盖成片声音不会修复嘴型。
- 已出片但质检失败时保留候选、原任务 ID、实际费用（若能确认）和原因。预算允许且用户已授权再生成，才产出新尝试。用户已采用的卡先保留，改其结果需要明确范围。
- 合成使用采用片段，连续性以实际上一镜末帧或已接受参考为准。记录每镜 `qc.status`（`pass`、`fail`、`unverified`）和逐句对照。只有确实检查过的项目标通过。
