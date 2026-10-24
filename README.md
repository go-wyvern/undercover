<p align="center">
  <img src="docs/images/logo.svg" alt="UnderCover 卧底俱乐部图标" width="96" height="96" />
</p>

<h1 align="center">UnderCover ｜ 卧底俱乐部</h1>

<p align="center">
  <a href="https://github.com/go-wyvern/undercover/actions/workflows/deploy.yml"><img src="https://github.com/go-wyvern/undercover/actions/workflows/deploy.yml/badge.svg?branch=main" alt="构建与镜像发布" /></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.24+" /></a>
  <a href="https://nodejs.org/"><img src="https://img.shields.io/badge/Node.js-22.12%2B-5FA04E?logo=nodedotjs&amp;logoColor=white" alt="Node.js 22.12+" /></a>
  <a href="https://vuejs.org/"><img src="https://img.shields.io/badge/Vue-3-4FC08D?logo=vuedotjs&amp;logoColor=white" alt="Vue 3" /></a>
  <a href="https://www.typescriptlang.org/"><img src="https://img.shields.io/badge/TypeScript-5.9-3178C6?logo=typescript&amp;logoColor=white" alt="TypeScript 5.9" /></a>
  <a href="https://linx.qiniu.com/"><img src="https://img.shields.io/badge/Linx-AI-1F4539" alt="Powered by 灵矽 AI" /></a>
</p>

一个通过语音玩的「谁是卧底」网页应用。支持四人、六人局，朋友可以加入同一房间，空位由 AI 补齐。围坐圆桌后，按住说话即可开局、描述词语和投票。

灵矽智能体负责语音识别、理解玩家意图、生成 AI 玩家的描述与投票，并提供语音合成；Go 通过 MCP 工具执行游戏操作、保存房间并校验规则。前端采用 Vue 3 + TypeScript，由 `go:embed` 嵌入 Go 程序。

## 1. 准备环境

从源码构建并运行，需要准备：

| 依赖 | 版本要求 | 用途 |
| --- | --- | --- |
| [Go](https://go.dev/dl/) | **1.24+**，推荐 1.26 | 编译服务、运行软件终端激活工具 |
| [Node.js](https://nodejs.org/en/download) | **22.12+**，推荐 24 | 构建前端 |
| npm | 使用 Node.js 自带的版本 | 安装前端依赖 |
| PostgreSQL | 推荐 **17+** | 准备一个可连接的应用数据库 |
| Redis | 推荐 **7+** | 单实例或 Redis Cluster 均可 |
| 浏览器 | 支持麦克风、AudioWorklet 和音频播放 | 进行语音游戏 |

Go 的版本下限以 [go.mod](go.mod) 为准；前端使用 Vite 7，Node.js 要求可参考 [Vite 7 文档](https://v7.vite.dev/guide/)。PostgreSQL 和 Redis 的版本为推荐配置。

Go 和 Node.js 用于构建。构建完成后，运行机器只需可执行文件、运行配置和可连接的 PostgreSQL / Redis，无需安装 Node.js，也无需单独启动前端服务。

还需要一个可使用灵矽服务的七牛云账号。运行机器需要能访问灵矽的 HTTPS 和 WSS 服务；下载依赖时需要能访问 Go 模块源和 npm registry。

## 2. 下载项目并配置环境

```sh
git clone https://github.com/go-wyvern/undercover.git
cd undercover
cp .env.example .env

go mod download
npm --prefix web ci
```

编辑项目根目录的 `.env`。以下使用昵称入座方式，先填写这些变量：

```dotenv
UNDERCOVER_ADDR=127.0.0.1:18080
UNDERCOVER_ALLOW_GUEST=true

# 填写已经准备好的数据库连接；账号需要建表及读写权限。
# 此处 sslmode=disable 仅为本机示例，远程数据库按服务要求配置 TLS。
DATABASE_URL="postgresql://YOUR_USER:YOUR_PASSWORD@127.0.0.1:5432/undercover?sslmode=disable"
REDIS_URL="redis://127.0.0.1:6379/0"
REDIS_ADDR=

# 下一步完成软件终端绑定后填写。
LINX_DEVICE_IDS=
```

使用 Redis Cluster 时，将逗号分隔的节点地址填写到 `REDIS_ADDR`，例如 `redis-host:6379,redis-host:6380,redis-host:6381`；它非空时优先于 `REDIS_URL`。认证信息及其他可选项见 [.env.example](.env.example)。

程序启动时自动读取当前目录下的 `.env`；也可以直接通过进程环境变量提供配置。连接信息应能从运行机器访问。

## 3. 注册灵矽并创建智能体

1. 注册或登录七牛云账号，进入 [灵矽 AI 控制台](https://xrobo.qiniu.com/)。
2. 在智能体管理中创建一个智能体，例如「卧底俱乐部主持」，进入角色配置页。
3. 将下方提示词复制到角色介绍中，保留 `{{game_instructions}}` 和 `{{game_context}}` 两个占位符。Go 会在连接时填入主持约束和当前房间信息。
4. 选择支持工具调用的大语言模型，在技能 / 意图识别设置中启用 **Function Call**，并选择普通话和喜欢的音色。可选项以账号页面为准。
5. 记忆模式选择「无记忆」，关闭与游戏无关的插件，点击页面顶部的「保存配置」。

第 3 步的角色介绍使用以下内容：

```text
你是网页“谁是卧底”的语音主持。
遵循本次连接注入的主持约束，以当前房间状态中的座位、姓名和真人/AI身份为准。
只使用中文，不输出思考分析或工具调用过程。

主持约束：
{{game_instructions}}

当前房间：
{{game_context}}
```

配置操作可参考官方 [快速入门](https://linx.qiniu.com/docs/xrobot/guide/quick-start) 和 [智能体配置指引](https://linx.qiniu.com/docs/xrobot/guide/agent-config)。

这个智能体负责网页中的 AI 主持和 AI 玩家。`game.*` 工具由 Go 在连接后通过 MCP 提供，灵矽自动发现；无需在控制台逐个填写工具，也无需另建云端 MCP 服务。

## 4. 绑定软件终端

应用以软件终端身份连接灵矽，无需购买硬件。在项目根目录运行：

```sh
go run ./cmd/linx-device
```

工具会生成一个虚拟 MAC，并打印激活码以及类似下面的配置：

```dotenv
LINX_DEVICE_IDS=02:AB:CD:12:34:56
```

上面的 MAC 仅为格式示例，请将**工具实际输出的值**保存到 `.env`，再完成绑定：

1. 打开刚创建的智能体，在「设备管理」中添加设备，输入工具输出的激活码。
2. 完成绑定后，再次运行 `go run ./cmd/linx-device`。工具会读取 `.env` 中保存的 MAC；看到“软件终端已绑定”即配置成功。
3. 后续运行、重启或迁移应用时，继续使用这个已绑定的 MAC。激活码需要重新获取时，也应保留相同 MAC 后重试。

通常只需填写 `LINX_DEVICE_IDS`。`LINX_OTA_URL`、`LINX_WS_URL`、`LINX_WS_TOKEN` 和 `LINX_API_KEY` 均可留空，程序会通过官方 OTA 接口自动获取连接凭证。AI 玩家使用已绑定智能体的模型，无需额外配置 `LLM_BASE_URL`、`LLM_API_KEY` 或 `LLM_MODEL`。

需要多间房同时使用语音时，可以绑定多个软件终端，并将它们的 MAC 用英文逗号填入 `LINX_DEVICE_IDS`。每个终端同一时间服务一条灵矽连接。

## 5. 构建并启动

在项目根目录执行：

```sh
npm --prefix web run build
go build -o bin/undercover ./cmd/undercover
./bin/undercover
```

打开 [http://127.0.0.1:18080](http://127.0.0.1:18080)，输入昵称入座。首次运行的大厅为空，可以创建第一间房间。

前端已经嵌入可执行文件。修改前端后，需要重新执行前端构建和 Go 编译；运行时请在放有 `.env` 的目录启动程序，或通过进程环境变量提供配置。

让其他玩家访问时，将 `UNDERCOVER_ADDR` 改为 `0.0.0.0:18080`，并为访问入口配置 **HTTPS**。反向代理需要支持 WebSocket 转发和 SSE 事件流，关闭 SSE 缓冲。浏览器远程使用麦克风需要安全上下文，本机可通过 `localhost` / `127.0.0.1` 访问。当前同一个应用数据库只运行一个游戏进程。

## 6. 开始游戏

1. 创建房间，选择四人或六人局，将房间链接发给朋友；空位会在开局时由 AI 补齐。
2. 允许浏览器使用麦克风；若页面提示开启声音，先点击启用播放。
3. 房主按住中央语音按钮说「开始游戏」，松开后由灵矽理解并调用工具发牌。
4. 查看自己的秘密词卡，轮到本人时按住说出描述；投票阶段可以说「我投二号」。页面也提供文字描述和点击投票。
5. 听主持播报和玩家发言，结束后房主可以说「再来一局」。

语音按钮会按回合启用，播报期间先收听。没有配置 `LINX_DEVICE_IDS` 时，也可使用页面明确标识的体验模式熟悉规则；完整灵矽语音游戏需要完成上述智能体和软件终端配置。

## 相关文档

- [产品设计](docs/product-design.md)：产品定位与页面交互。
- [架构文档](docs/architecture.md)：前端、Go 与灵矽的职责及交互泳道图。
- [API 文档](docs/api.md)：应用接口与 MCP 游戏工具。
- [灵矽 OTA 协议](https://linx.qiniu.com/docs/xrobot/platform/OTA)、[WebSocket 协议](https://linx.qiniu.com/docs/xrobot/platform/websocket)、[端侧 MCP 协议](https://linx.qiniu.com/docs/xrobot/mcp/hardware-mcp)。
