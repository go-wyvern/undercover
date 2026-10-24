# 卧底俱乐部 · 灵矽 AI

Go 后端 + Vue 3 / TypeScript 前端的「谁是卧底」应用。浏览器展示房间大厅，点击入座后围坐圆桌，中央的灵矽 AI 机器人负责发牌与主持。支持四人、六人局，空位由 AI 补齐，按住中央按钮可说“开始游戏”“再来一局”、描述或投票，松开后由灵矽理解并执行。

前端由 Vite 构建，通过 `go:embed` 编进 Go 二进制。灵矽智能体负责完整语音对话、意图理解、主持和所有 AI 玩家行为；Go 提供端侧 MCP 游戏工具，保存房间和词卡，并校验发牌、轮次、投票与胜负。

产品定位、体验问题与页面交互见 [产品设计](docs/product-design.md)。架构职责、游戏交互泳道图和语音同步机制见 [架构文档](docs/architecture.md)。接口详情见 [API 文档](docs/api.md)。

## 本地运行

需要 Go 1.24+、Node.js 22.12+、PostgreSQL 和 Redis。先创建本应用的独立 PostgreSQL 数据库，复制 `.env.example` 为 `.env`，填写 `DATABASE_URL` 和 `REDIS_URL`。应用启动时检查连接并自动创建 `undercover_rooms` 表；数据库账号需要建表及读写权限。新数据库的大厅为空，通过页面创建第一间房间。

```sh
npm --prefix web ci
make run
```

打开 <http://127.0.0.1:18080>，登录后进入房间。已接入灵矽时，房主按住中央按钮说「开始游戏」，结束后可说「再来一局」。未配置灵矽时，使用「开始体验」按钮和文字体验发牌、描述、投票、平票加赛和结算；页面显示「体验模式」。此模式的玩家描述来自词库，投票是演示策略，播报使用浏览器语音能力。

仓库同时保留构建后的 `web/dist`，可直接运行：

```sh
go run ./cmd/undercover
```

修改前端后必须重新构建，才能更新嵌入的资源：

```sh
npm --prefix web run build
go build -o bin/undercover ./cmd/undercover
./bin/undercover
```

开发时可在两个终端分别运行 Go 服务和 `npm --prefix web run dev`；Vite 将 `/api` 与 `/auth` 代理至 Go 服务。

## 接入灵矽语音

在 `.env` 中填写灵矽配置。应用不会从浏览器读取灵矽登录信息，也不会使用 spxbench 的密钥。

灵矽公开协议以软件终端的身份接入。无需硬件，但需要将一个稳定的虚拟 MAC 绑定到灵矽智能体。账号管理 API Key 与语音终端 Token 是两类凭证。

1. 创建一个唯一的软件终端身份，执行以下命令，并将打印的 MAC 保存到 `.env` 的 `LINX_DEVICE_IDS`，之后始终复用该身份：

   ```sh
   go run ./cmd/linx-device
   ```

2. 将输出的激活码填入灵矽控制台对应智能体的「设备管理」。智能体应使用网页工具模式提示词（见 `docs/linx-host-prompt.txt`），按 `game.*` 工具操作真实房间。关闭无关工具，长期记忆建议关闭。
3. 将工具打印的 `LINX_DEVICE_IDS=...` 配置加入 `.env`，重启 Go 服务。服务端会通过 OTA 自动取得语音 Token，不会将 Token 发送给前端。
4. 按住中央「按住和灵矽说话」，授权麦克风，松开后直接与灵矽主持对话。可以说「开始游戏」、描述自己的词、「我投二号」「继续主持」「轮到谁」。最长录音30秒；明确的游戏操作由灵矽调用工具执行，圆桌实时同步，无需再点文字确认。

`LINX_DEVICE_IDS` 可以是多个已绑定 MAC，用逗号分隔。每个设备身份同一时间只占用一个上游语音连接；多房间同时使用语音时建议准备多个软件终端。正在使用的设备不会被并发复用。同一条主持/AI 发言的播报请求会在服务端合并并缓存，供房间成员共同播放。

开局报幕、逐位发言提醒、AI 描述、投票明细、平票加赛和胜负结果会自动排队播报。房间顶部显示当前语音状态；浏览器拦截自动播放时，点击「点击开启声音」继续。每条公开记录都可以点击喇叭重听。开始录音会停止本机播报，避免回录；词卡不会进入公共播报。

已绑定灵矽时优先播放智能体音色；灵矽失败时尝试浏览器语音，并明确显示备用来源。语音开关保存在本机。重连不会自动重播全部历史记录，可以手动重听。AI 发言气泡、头像高亮和新增描述记录跟随本机音频播放进度展示。桌面持续显示各座位的词卡；自己的卡可以点击翻看，其他人的词和身份只在结算后揭晓。不同客户端的播放进度并未严格同步。

完整对话链路：浏览器 AudioWorklet → 16kHz 单声道 PCM16LE → Go → 灵矽 ASR → 灵矽大模型 → MCP 工具调用 Go → 灵矽 TTS → 房间播报。语音会话不会在收到 STT 后结束。灵矽的真实回复文字和返回音频会保存为本房间的公开主持记录，其他房间无法读取；尚未发牌时也能对话。请求者在 PCM 模式下通过 Web Audio 边接收边播放，结束后保留整段音频用于重听；Opus 模式及其他房间成员使用整段音频播放。

端侧 MCP 工具包括 `game.get_state`、`game.get_rules`、`game.start`、`game.submit_description`、`game.vote`、`game.skip`、`game.get_ai_batch`、`game.submit_ai_batch`。工具绑定当前登录账号与房间，不接受客户端指定其他账号；同一会话的重复请求不会重复执行动作。主持处理期间暂停回合超时和备用 AI，避免同时操作。

真人词卡不交给主持模型；工具仅在扮演 AI 的任务中返回该 AI 的词。语音会话由同一个灵矽主持模型扮演多个 AI，模型上下文并未在这些角色之间独立隔离。公开回复若包含尚未揭晓的完整词语，会替换为安全提醒。词卡只在网页私密区域展示。

按钮和文字输入同样由灵矽继续处理 AI 回合，不需要额外的模型地址、Key 或模型名称。未绑定灵矽时才使用明确标识的本地演示策略。旧的 `/voice` 接口保留为仅转写模式。公开记录的重听通过灵矽 `text_response` 合成；按协商格式将 PCM 封装成 WAV、Opus 封装成 Ogg。

浏览器麦克风在远程访问时需要 HTTPS，localhost 可直接使用。服务器的设备连接使用 WSS。凭证仅放在本地 `.env`，不会进入前端。真实灵矽 OTA、WSS、语音开局、真人描述、AI 分组描述与投票、Go 计票，以及 PCM 实时音频输出均已完成隔离房间联调；权限与重试等边界另有模拟回归测试。

官方参考：

- [平台 API](https://linx.qiniu.com/docs/xrobot/api/)
- [OTA / 软件终端激活](https://linx.qiniu.com/docs/xrobot/platform/OTA)
- [语音 WebSocket 协议](https://linx.qiniu.com/docs/xrobot/platform/websocket)
- [第三方业务系统接入](https://linx.qiniu.com/docs/xrobot/guide/third-app)

## AI 回合与语音同步

所有正式 AI 回合均走已绑定的灵矽智能体。Go 保存权威状态、校验工具动作并调度待处理的 AI 回合。语音开局、文字描述和点击投票后，待处理的 AI 回合也由灵矽接续。旧版 `.env` 中的 `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` 已不再读取，可以删除。

自动调度使用公开设备语音协议：首次生成并缓存一句“继续主持”的 PCM 音频，以音频输入触发灵矽，再由它调用相同的 `game.*` 工具；不调用独立的 Chat Completions 服务。灵矽暂不可用时保留待处理回合并重试，不静默切换到本地机器人。

返回的每句 TTS 记录音频偏移及对应的公开发言。实时 Web Audio 使用排定的播放时间，整段音频使用媒体播放时间，驱动当前座位的头像气泡。新 AI 描述不再在整批结果到达时一起显示。主播播报和 AI 角色共用控制台配置的音色。已缓存原声支持准确重听；重启后重新合成的语音无法保证沿用原声的逐句时长。

## XBuilder 账号

参考 `spxbench/internal/spxbench/auth.go` 的协议实现：

- `POST /oauth/par`：推送授权请求。
- PKCE S256 + OAuth state：校验回调来源。
- `POST /oauth/token`：服务端交换令牌。
- `GET /user`：获取账号资料，scope 为 `account:user:read`。
- 浏览器使用 HttpOnly、SameSite=Lax 的随机会话 Cookie，上游 Token 不进入浏览器。

填写 `.env` 中 `XBUILDER_ACCOUNT_*` 和至少 32 字符的 `UNDERCOVER_SESSION_SECRET`，并在账号服务注册本应用的回调，例如 `https://your-domain.example/auth/callback`。配置账号后默认关闭游客入口，可显式设置 `UNDERCOVER_ALLOW_GUEST=true` 开放游客。

会话保存到 Redis，键为 `undercover:session:<Token 的 SHA-256 哈希>`，值为用户资料与过期时间，TTL 为七天。浏览器仍只持有随机 Token Cookie；退出会删除 Redis 会话，Redis 不可用时返回服务错误。并未复用 spxbench 的数据库或配置文件。真实 XBuilder 登录需要本应用自己的 OAuth Client 配置，已通过模拟账号服务验证 PAR、PKCE、回调和会话流程。

## 规则与状态

- 四人局为三平民一卧底，六人局为五平民一卧底。
- 密码学随机选择词对、卧底座位和词语方向，整局固定。
- 自己的词卡由后端按会话过滤，结束前不返回其他人的词和阵营。
- 存活玩家按座位发言；每人每轮一票，不能投自己或已出局玩家。
- 平票候选人补充发言后加投，再平票则无人出局，进入下一轮。
- 卧底出局则平民胜利；卧底存活到总人数只剩两人则卧底胜利。
- 发言或投票超时为 60 秒，分别跳过或弃权。
- 主动离席后由 AI 接续座位，房主离开会转移房主；没有真人时重置房间。
- 个性化 SSE 推送房间快照，刷新页面可恢复自己所在的房间。
- 房主在房间右上角点击「关闭房间」并确认后，永久删除房间和本局记录；所有成员返回大厅。普通成员只能离席。大厅最多五秒刷新后移除该房间。

## 存储与部署

`DATABASE_URL` 指向 PostgreSQL，Go 通过 `database/sql` 与 `github.com/lib/pq` 访问。`undercover_rooms` 每间房间一行，`state` JSONB 保存座位、隐藏词、完整对局及公开记录，`updated_at` 记录更新时间。状态写入成功后再通知前端；写入失败会回滚内存中的变更。关闭房间执行真正的 `DELETE`，不会在重启后自动重建。

`REDIS_URL` 指向会话存储。Redis 的持久化、备份及访问控制由部署环境配置；若清空会话库，玩家需要重新登录。PostgreSQL 与 Redis 的连接信息只保留在 Go 服务端。应用运行及设备激活均不需要 `data` 目录，原来的 `UNDERCOVER_DATA_DIR`、`-data-dir` 已移除。

订阅、游戏计时器及音频缓存仍属于单个 Go 实例。当前请部署一个 Go 副本；应用通过 PostgreSQL advisory lock 阻止同一数据库 schema 同时启动第二个游戏进程。所有玩家访问同一个服务地址，即可看到并加入同一组房间。原始录音仅存内存，重启后不恢复；已保存的文字对局、词卡和有效 Redis 登录会话可以恢复。

旧版本升级时，先停止旧进程，再一次性导入房间：

```sh
go run ./cmd/undercover -import-rooms /absolute/path/to/rooms.json
```

该命令在事务中导入并启动服务，已有数据库房间不会被覆盖。导入后正常启动不再携带该参数，也不要反复导入旧备份，以免重新导入已经关闭的房间。旧的文件 Session 不迁移，升级后重新登录。确认房间完整后，可将旧目录移到备份位置。


## 测试

```sh
make test
make build
```

测试覆盖胜负、非法动作、重复投票、平票、并发入座、隐藏信息隔离、状态恢复、账号 PAR/PKCE、Cookie 会话、跨来源请求阻止、灵矽 OTA/WS 握手、设备连接租用、识别转发和真人录音播放。`go test -race ./...` 检查并发访问，也覆盖房主权限、关闭房间通知、存储失败回滚与会话错误处理。以下命令额外执行真实存储集成测试（只创建独立临时 PostgreSQL schema 和 Redis 测试前缀）：

```sh
UNDERCOVER_TEST_DATABASE_URL='postgresql://your-user:your-password@127.0.0.1:5432/undercover?sslmode=disable' \
UNDERCOVER_TEST_REDIS_URL='redis://127.0.0.1:6379/0' \
go test -race ./...
```

## 目录

```text
cmd/undercover/       Go 服务入口
cmd/linx-device/      灵矽软件终端激活工具
internal/auth/       XBuilder 登录、游客与持久化会话
internal/game/       权威规则、房间、灵矽 AI 任务
internal/linx/       OTA、WebSocket、PCM/WAV 与 Opus/Ogg
internal/server/     HTTP API、SSE、语音网关
internal/storage/    PostgreSQL 房间与 Redis Session 适配器
web/src/             Vue 3 + TypeScript
web/public/          麦克风 AudioWorklet 和静态资源
web/dist/            构建产物，由 go:embed 嵌入
```

部署时由 Go 提供前端和 API，同一域名即可。若放在反向代理后，请开启 WebSocket 转发、关闭 SSE 缓冲，并配置 HTTPS；不要为事件流设置很短的连接超时。

## GHCR 镜像发布

发布流程参考 spxbench，使用 `.github/workflows/deploy.yml`，在 GitHub Actions 手动触发：

1. 将代码推送到 GitHub 仓库的 `main` 分支。
2. 进入 **Actions → Publish undercover image → Run workflow**，选择 `main`。
3. 工作流先构建前端并运行测试，包括真实 PostgreSQL/Redis 集成测试，成功后构建并推送镜像。

镜像地址为 `ghcr.io/go-wyvern/undercover:main`，另保留 `sha-<提交前七位>` 标签，支持 `linux/amd64` 与 `linux/arm64`。发布使用仓库自动提供的 `GITHUB_TOKEN` 和 `packages: write`，无需添加个人 GHCR Token。灵矽、XBuilder、数据库和 Redis 的配置均在运行时注入，不进入镜像或 Actions 构建配置。

Dockerfile 分三阶段构建：Node.js 编译 Vue，Go 嵌入静态文件并交叉编译，最后使用带 CA 证书的非 root distroless 镜像运行服务。镜像不包含 Node.js、Go 工具链或本地 `.env`。

服务器准备 `.env.production`，按 `.env.example` 填写 PostgreSQL、Redis、XBuilder 回调域名及灵矽设备身份。数据库地址应能从容器访问；`127.0.0.1` 在容器内指向容器自身。运行示例：

```sh
docker pull ghcr.io/go-wyvern/undercover:main
docker run -d --name undercover --restart unless-stopped \
  -p 127.0.0.1:18080:18080 \
  --env-file .env.production \
  -e UNDERCOVER_ADDR=0.0.0.0:18080 \
  ghcr.io/go-wyvern/undercover:main
```

然后由 HTTPS 反向代理转发至本机 `18080`。请保持一个 Go 副本；PostgreSQL 与 Redis 独立部署，无需挂载 `data` 目录。首次发布的 GHCR Package 访问权限由 GitHub 决定；私有镜像需先登录 GHCR，若希望匿名拉取，可在 Package 设置中调整可见性。

本地构建可执行 `docker build -t undercover:local .`。发布完成后，Actions Summary 会列出镜像标签及 digest，部署也可以固定使用 `ghcr.io/go-wyvern/undercover@sha256:...`。
