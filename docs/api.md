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
| POST | `/api/rooms` | 创建并入座，`name`、`theme`、`capacity` |
| GET | `/api/rooms/{id}` | 当前成员的个性化房间快照 |
| POST | `/api/rooms/{id}/join` | 加入房间 |
| POST | `/api/rooms/{id}/leave` | 离开，游戏中改为 AI 接续 |
| DELETE | `/api/rooms/{id}` | 仅当前房主可关闭，永久删除房间及本局记录 |
| POST | `/api/rooms/{id}/start` | 房主开始，空位补 AI |
| POST | `/api/rooms/{id}/speech` | `text`；可附 `audio`（base64 PCM16LE、16kHz 单声道，最长30秒） |
| POST | `/api/rooms/{id}/skip` | 跳过自己的发言 |
| POST | `/api/rooms/{id}/vote` | `targetId`，空字符串表示弃权 |
| GET | `/api/rooms/{id}/events` | SSE，`room` 为个性化快照，`closed` 表示房间已关闭 |
| GET | `/api/rooms/{id}/assistant/voice` | 完整灵矽语音对话，STT → 大模型 → MCP 游戏工具 → 主持回复音频 |
| GET | `/api/rooms/{id}/voice` | 浏览器同源 WebSocket，仅自己的发言/投票回合可连接 |
| GET | `/api/rooms/{id}/audio/{event}` | 本房间公开发言的音频，不接受任意文本 |

房间快照仅包含 `myWord`，不包含完整 Words 映射或卧底 ID。只有 `finished` 阶段返回 `reveals`。每次状态变化增加 version，前端忽略过期快照。SSE 断线重连后发送当前快照，不需要重放动作。

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

`/assistant/voice` 允许房间成员在等待、描述、投票或结束阶段与主持交谈。客户端等待 `ready` 后发送 PCM，松开后发 `stop`。收到 `transcript` 仅展示用户原话并保持连接；收到 `assistant_done` 才结束。成功动作通过 SSE 推送原发言与固定提示，所有成员（包括发言本人）立即按记录入队，播放原声或该记录的灵矽合成音频。`agentActive` 不会暂停或清空此队列。若本条 TTS 未就绪，音频接口等待当前会话的缓存；动作完成后返回 `assistant_done`，`streamed` 为 `"false"`，可不含 `id` 和 `text`。未执行动作的普通询问仍可保存为 `source: "linx_agent"` 的主持记录并实时播放。

端侧 MCP 通过同一条服务端灵矽 WebSocket 自动注册。公开状态中不包含真人词卡，AI 任务的秘密词只交给灵矽模型。任务组 ID 用于阻止过期 AI 操作，所有工具都使用当前会话账号和房间。

普通询问使用实时 PCM 时，`ready` 包含 `audioFormat` 与 `sampleRate`（字符串）。客户端保留录音时已解锁的 AudioContext；`assistant_sentence` 提供 `text`、`offsetMs`、`logId`、`playerId`、`seat`（数值字段为字符串）。后续二进制消息为该采样率的单声道 PCM16LE，排队播放后，字幕与头像应在排定的 AudioContext 播放时间触发，不能收到消息就提前显示。`assistant_reply_id` 标记已听过的公开记录；`assistant_done` 带 `id` 与 `streamed`，应等本机已排队音频播放完后再关闭音频上下文。`reset_audio` 要立即清空排队声音。未实时播放的请求与其他成员通过 SSE 记录读取完整 WAV/OGG 音频。


主持记录的 `audioCues` 为 `{offsetMs:number, text:string, logId?:string, playerId?:string, seat?:number}[]`。偏移相对于缓存的完整原声音频；整段播报按媒体 `currentTime` 切换气泡，新 AI 描述跟随对应音频首次播放才显示。普通按钮/文字动作后的待处理 AI 回合也自动调度同一个灵矽智能体。
