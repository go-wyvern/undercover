# 应用接口

前端与 Go 同源，所有修改操作校验 Origin / Sec-Fetch-Site。除大厅、配置、健康检查与账号入口以外，接口要求服务端会话 Cookie。错误统一返回 `{"error":"可读信息"}`。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查 |
| GET | `/api/config` | 可用能力，不含凭证 |
| GET | `/api/auth/me` | 当前用户和已加入房间 |
| POST | `/api/auth/guest` | 游客登录，`displayName` |
| GET | `/auth/login` | XBuilder 登录，支持安全的 `return_to` |
| GET | `/auth/callback` | OAuth 回调 |
| POST | `/auth/logout` | 撤销会话，并离开当前房间 |
| GET | `/api/rooms` | 大厅列表，仅公开信息 |
| POST | `/api/rooms` | 创建并入座，`name`、`theme`、`capacity`，可选 `password`、`autoFillAI`（默认 true） |
| GET | `/api/rooms/{id}` | 当前成员的个性化房间快照 |
| POST | `/api/rooms/{id}/join` | 加入房间，口令房需传 `password`；已有成员可直接恢复 |
| POST | `/api/rooms/{id}/ready` | 设置本人准备状态，`ready: boolean` |
| POST | `/api/rooms/{id}/settings` | 房主设置 `autoFillAI: boolean` |
| POST | `/api/rooms/{id}/ai-seat` | 房主安排/移除 AI 席位，`seat: number`、`bot: boolean` |
| POST | `/api/rooms/{id}/members/{playerId}/remove` | 房主移除其他真人成员 |
| POST | `/api/rooms/{id}/leave` | 离开，游戏中改为 AI 接续 |
| DELETE | `/api/rooms/{id}` | 仅当前房主可关闭，永久删除房间及本局记录 |
| POST | `/api/rooms/{id}/start` | 仅房主开始，校验其他真人准备状态，按房间配置补 AI |
| POST | `/api/rooms/{id}/playback-ready` | 当前行动者确认指定前置播报已完成，或切换文字参与 |
| POST | `/api/rooms/{id}/speech` | `text`；可附 `audio`（base64 PCM16LE、16kHz 单声道，最长30秒） |
| POST | `/api/rooms/{id}/skip` | 跳过自己的发言 |
| POST | `/api/rooms/{id}/vote` | `targetId`，空字符串表示弃权 |
| GET | `/api/rooms/{id}/events` | SSE，`room` 为个性化快照，`closed` 表示房间已关闭，`removed` 表示已离席或被移除 |
| GET | `/api/rooms/{id}/assistant/voice` | 完整灵矽语音对话，STT → 大模型 → MCP 游戏工具 → 主持回复音频 |
| GET | `/api/rooms/{id}/voice` | 浏览器同源 WebSocket，仅自己的发言/投票回合可连接 |
| GET | `/api/rooms/{id}/audio/{event}` | 本房间公开发言的音频，不接受任意文本 |

房间快照仅包含 `myWord`，不包含完整 Words 映射或卧底 ID。只有 `finished` 阶段返回 `reveals`。每次状态变化增加 version，前端忽略过期快照。SSE 断线重连后发送当前快照，不需要重放动作。

## 开局准备、口令与房主管理

新建房间可设置 4 至 32 字的口令，省略或留空表示公开房。大厅仍展示口令房，但只返回 `passwordRequired` 布尔标记；通过分享链接或直接请求 join 也必须校验口令。口令以随机盐 PBKDF2-SHA256 派生值保存，不返回给前端、SSE 或灵矽上下文。连续口令验证按账号和房间限为每分钟 5 次；达到限制返回 429。

缺少口令返回 403 和 `code: "room_password_required"`，错误口令返回 403 和 `code: "room_password_invalid"`。已经在房间中的账号可以不再提供口令来恢复当前席位；这不允许新账号绕过校验。

等待开局或上一局结束时，非房主点击准备或取消准备；房主发起开局即视为本人准备，其他真人必须全部准备。`game.start` 与 HTTP start 共用同一服务端校验，拒绝时提示尚未准备的座号与名字。成功发牌后清除本局准备状态，下一局需要重新确认。

房主可切换空位自动补 AI（`autoFillAI`），也可为指定空位预留 AI 或移除预留 AI。预留 AI 占用座位，真人加入时选择首个真正的空位；关闭自动补位后，必须由真人或预留 AI 坐满才能开局。预留 AI 随下一局保留，自动补位 AI 仅服务当前局。旧房间没有此字段时默认继续自动补位。

加入、离席、移除成员、调整 AI 席位或修改补位设置，均清除所有真人的准备状态；离席保留其他成员座号，房主转移后由新房主继续管理。相同设置的重复请求不重置准备状态。上述房间设置、预留 AI 与准备状态通过 PostgreSQL 的房间 JSON 保存，重启后恢复。游戏进行中拒绝准备状态、补位设置及房主管理动作，正常主动离席仍沿用 AI 接续规则。

快照新增 `hostPlayerId`、玩家的 `ready`、`autoFillAI`、`passwordRequired`、`availableSeats`、`canStart` 与 `startBlockReason`。`lobbyPlayers` 仅在等待/结束时提供下一局座位，结束时 `players` 仍保留上一局完整揭牌信息。准备状态变更经 SSE 实时推送。修改准备状态只接受 `ready`，不接受用户/座位参数；房主管理由服务端验证身份。

被移除成员立即失去房间、音频、语音和 SSE 的访问权限。已有 SSE 发送 `removed` 事件后关闭；页面停止录音与播放并返回大厅。移除不是封禁，对方可按原加入条件重新入座，并触发重新准备。

## 关闭房间

房主调用 `DELETE /api/rooms/{id}`，成功返回 `{"ok":true}`。普通成员及其他用户返回 403；房间已经关闭返回 404。删除 PostgreSQL 记录成功后，服务端清除房间状态、取消相关语音与 AI 工作，并向当前 SSE 成员发送：

```text
event: closed
data: {"message":"房主已关闭房间"}
```

前端停止录音与播放、关闭事件流并返回大厅。若断线错过 `closed`，重连时发现房间返回 404 也会返回大厅。大厅列表每五秒刷新一次。存储删除失败不会移除当前房间。

## 浏览器语音协议

1. 建立 `/api/rooms/{id}/voice` WebSocket，使用已有会话 Cookie。
2. 等待 `{"type":"ready"}`，再开始发送音频。
3. 发送二进制 PCM16LE：16kHz、单声道，每帧320采样，即640字节/20ms。
4. 松开后发送 `{"type":"stop"}`。
5. 收到 `{"type":"transcript","text":"识别文字"}`；校对并调用 speech/vote 接口才推进对局。
6. 取消发送 `{"type":"cancel"}` 或断开连接。失败返回 `{"type":"error","message":"可读提示"}`。

语音连接不接受客户端提供上游地址、Token、设备 ID 或智能体 ID。所有这些信息都由部署者在 Go 服务端配置。

## 完整智能体会话

`/assistant/voice` 仅允许当前存活发言玩家、尚未投票的存活玩家使用；等待与结束阶段仅房主可用来开局或再来一局。发牌阶段、已出局、已投票或未轮到本人的请求在 WebSocket 升级前返回 409，不占用灵矽设备和房间主持会话。客户端完成当前行动的前置播报并经服务端开放行动后才启用录音按钮。客户端等待 `ready` 后发送 PCM，松开后发 `stop`。收到 `transcript` 仅展示用户原话并保持连接；收到 `assistant_done` 才结束。成功动作通过 SSE 推送原发言与固定提示，所有成员（包括发言本人）立即按记录入队，播放原声或该记录的灵矽合成音频。`agentActive` 不会暂停或清空此队列。若本条 TTS 未就绪，音频接口等待当前会话的缓存；动作完成后返回 `assistant_done`，`streamed` 为 `"false"`，可不含 `id` 和 `text`。未执行动作的查询由Go生成真实状态提示或规则，并保存为 `source: "linx_agent"` 的记录，通过同一队列播报；不转发模型自由回复。

端侧 MCP 通过同一条服务端灵矽 WebSocket 自动注册。公开状态中不包含真人词卡，AI 任务的秘密词只交给灵矽模型。任务组 ID 用于阻止过期 AI 操作，所有工具都使用当前会话账号和房间。

旧版本实时 PCM 协议中，`ready` 包含 `audioFormat` 与 `sampleRate`（字符串）。客户端保留录音时已解锁的 AudioContext；`assistant_sentence` 提供 `text`、`offsetMs`、`logId`、`playerId`、`seat`（数值字段为字符串）。后续二进制消息为该采样率的单声道 PCM16LE，排队播放后，字幕与头像应在排定的 AudioContext 播放时间触发，不能收到消息就提前显示。`assistant_reply_id` 标记已听过的公开记录；`assistant_done` 带 `id` 与 `streamed`，应等本机已排队音频播放完后再关闭音频上下文。`reset_audio` 要立即清空排队声音。未实时播放的请求与其他成员通过 SSE 记录读取完整 WAV/OGG 音频。


主持记录的 `audioCues` 为 `{offsetMs:number, text:string, logId?:string, playerId?:string, seat?:number}[]`。偏移相对于缓存的完整原声音频；整段播报按媒体 `currentTime` 切换气泡，新 AI 描述跟随对应音频首次播放才显示。普通按钮/文字动作后的待处理 AI 回合也自动调度同一个灵矽智能体。


`RoomView.myDraft` 仅包含当前账号在当前发言回合识别后尚未提交的原文，其他账号、公开日志及灵矽公开房间上下文不包含该字段。存在待确认发言时 `deadline` 为 0，暂停自动超时跳过；使用现有 `POST /api/rooms/{id}/speech` 确认后提交一次、清除草稿并推进下一位。原文未修改且录音仍在内存时复用原声；修改文字或服务已重启时按确认文本合成。读取状态、查询规则与模型自由回复均不代表已提交描述。


## 播报与行动计时

描述、平票描述及投票阶段，服务端为真人行动创建持久化的 `action` 窗口。`action.id` 独立于房间 version；`boundaryId` 和 `logIds` 指定本次必须先收听的公开记录。`state` 为 `waiting_playback` 时 `deadline=0`，发言、跳过、投票以及两个语音 WebSocket 入口均拒绝提前行动（409）。

前端按队列播放实时记录，全部指定记录完成后请求：

```http
POST /api/rooms/{id}/playback-ready
Content-Type: application/json

{"turnId":"action.id","throughLogId":"action.boundaryId","result":"played"}
```

`result` 也可为 `muted`（关闭声音）或 `unavailable`（停止/无法播放，改用文字）。身份由 Cookie 决定，不接受代报玩家。服务端验证行动 ID、边界及行动者；旧回合/错误边界返回409，重复回报返回原状态，不重置截止时间。

描述只等待当前真人。投票等待当前仍需投票的存活真人，离席者退出等待集合；AI 与观战者不需要回报。全部必要回报完成且灵矽处理会话已释放后，服务端将状态置为 `active`，保存 60 秒截止时间，再经 SSE 同步。

播放等待上限为120秒，缺少回报、后台或网络失败不会无限期阻塞游戏。上限后切换文字参与并开放完整60秒窗口；`textOnly` 通知相应客户端停止阻塞操作的音频队列、展示已提交文字。手动改用文字也走同一协议。实时回报与历史回听分开，取消录音/播放不算成功听完，历史回听不改变服务端计时。

窗口标识、待播记录、已确认状态、等待上限与行动截止时间保存在 PostgreSQL；刷新恢复尚未完成的前置播放，服务重启沿用已保存的上限/截止时间。`serverNow` 用于前端校准显示时钟。语音会话释放不再重新赠送60秒；服务端处理期间的超时保护最多延后至原截止时间后100秒，不会因重复查询而无限延期。已识别的待确认私密草稿继续使用现有保护规则。
