# SotAPI

[![Wiki](https://img.shields.io/badge/docs-Wiki-2F81F7?style=flat-square&logo=github&logoColor=white)](https://github.com/Xbai-hang/sotapi/wiki)
[![CI](https://img.shields.io/github/actions/workflow/status/Xbai-hang/sotapi/ci.yml?style=flat-square&label=CI&logo=githubactions&logoColor=white)](https://github.com/Xbai-hang/sotapi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Xbai-hang/sotapi?style=flat-square&logo=github)](https://github.com/Xbai-hang/sotapi/releases)
[![Container](https://img.shields.io/badge/image-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white)](https://github.com/Xbai-hang/sotapi/pkgs/container/sotapi)
[![Go](https://img.shields.io/github/go-mod/go-version/Xbai-hang/sotapi?style=flat-square&logo=go&logoColor=white)](go.mod)
[![Telegram](https://img.shields.io/badge/channel-Telegram-26A5E4?style=flat-square&logo=telegram&logoColor=white)](https://core.telegram.org/bots/api)
[![Toy Project](https://img.shields.io/badge/status-toy_project-FF69B4?style=flat-square)](#它怎么玩)
[![License](https://img.shields.io/github/license/Xbai-hang/sotapi?style=flat-square)](LICENSE)

> An LLM API, powered by actual humans.

**SotAPI 是一个把真人伪装成 LLM 的多协议 API 网关，也是一个一本正经调用朋友的玩具项目。**

客户端照常发送 LLM API 请求，协议适配器把它交给统一的请求生命周期；SotAPI 再将完整上下文投递到指定的 Channel 会话。

SotAPI 统一管理鉴权、路由、投递、fallback、等待和协议转换。

[快速开始](#快速开始) · [客户端接入](#客户端接入) · [部署指南](https://github.com/Xbai-hang/sotapi/wiki/Deployment) · [开发路线](https://github.com/Xbai-hang/sotapi/wiki/Development-Roadmap)

```mermaid
sequenceDiagram
    participant Client as LLM Client
    participant SotAPI
    participant Telegram
    participant Human

    Client->>SotAPI: POST /v1/chat/completions
    SotAPI->>Telegram: 发送任务与完整上下文
    Telegram->>Human: 展示任务
    Human->>Telegram: 回复任务消息
    Telegram->>SotAPI: 返回真人答案
    SotAPI-->>Client: 对应协议的响应
```

## 它怎么玩

- **客户端以为自己在调用模型**：它照常发送协议请求、API Key 和模型名称。
- **“模型”首先可以是 Channel 另一头的人**：朋友、群友或者你自己都可以回答；真人链路不可用时，也可以让独立配置的 LLM 接手回答。
- **协议只是外壳**：不同 LLM API 由各自的 Adapter 接入同一个真人请求生命周期。
- **不需要做回答后台**：真人只需在 Telegram 中回复任务消息。
- **本地也能玩**：默认 polling 模式不要求公网域名或 HTTPS。

## 快速开始

推荐使用 Docker Compose 和默认的 Telegram polling 模式。

### 你需要准备

- Docker Engine 与 Docker Compose v2；
- 一个由 [@BotFather](https://t.me/BotFather) 创建的 Telegram Bot；
- 回答者的 Telegram chat ID；
- 一个用于保护 SotAPI 接口的随机 API Key。

让回答者先打开 Bot 并发送 `/start`。如果还不知道 chat ID，请在启动 SotAPI 前调用：

```bash
curl "https://api.telegram.org/bot<BOT_TOKEN>/getUpdates"
```

在返回结果中找到 `message.chat.id`。请保护 Bot Token，不要把命令或输出粘贴到公开位置。更完整的 Telegram 准备步骤见[部署指南](https://github.com/Xbai-hang/sotapi/wiki/Deployment#2-创建-telegram-bot)。

### 1. 获取项目

```bash
git clone https://github.com/Xbai-hang/sotapi.git
cd sotapi
cp configs/config.example.yaml configs/config.yaml
```

生成 API Key：

```bash
openssl rand -hex 32
```

### 2. 修改三个配置值

打开 `configs/config.yaml`，替换 API Key、Bot Token 和回答者 chat ID：

```yaml
auth:
  mode: api_key
  api_keys:
    - "replace-with-a-random-secret"

telegram:
  bot_token: "replace-with-a-telegram-bot-token"
  update_mode: polling

users:
  - id: "alice"
    channel: "telegram"
    recipient: "123456789"
```

示例配置已经定义了 `sotapi-human` 模型以及对应的用户池，第一次运行无需修改其他字段。

### 3. 启动服务

```bash
docker compose up -d --build --wait
```

检查容器与模型目录：

```bash
docker compose ps

curl --fail http://localhost:8080/healthz

curl --fail http://localhost:8080/v1/models \
  -H 'Authorization: Bearer replace-with-a-random-secret'
```

### 4. 发起第一次请求

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-a-random-secret' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "sotapi-human",
    "messages": [
      {"role": "user", "content": "请审阅这次发布决定。"}
    ]
  }'
```

此时 curl 会保持等待，Telegram 会收到一条 SotAPI 任务。回答者必须使用 Telegram 的**回复**功能回答该任务消息；SotAPI 收到回复后，curl 才会返回 Chat Completion。

默认真人响应超时为 5 分钟。需要更多回答时间时，请修改 `human.response_timeout`，并确保客户端和反向代理的超时都比它更长。

## 客户端接入

当前 MVP 的 Chat Completions Adapter 可以直接用于支持自定义 API Base URL 的客户端：

| 配置 | 默认值 |
| --- | --- |
| Base URL | `http://localhost:8080/v1` |
| API Key | `auth.api_keys` 中配置的任意一个值 |
| Model | `sotapi-human`，或 `models[].id` 中配置的其他名称 |

不同客户端对 Base URL 的拼接方式可能不同，请确认最终请求路径是 `/v1/chat/completions`。

### 协议进度

SotAPI 的核心请求生命周期不绑定某一家 LLM API。不同协议在边界处完成请求与响应转换，当前仓库进度如下：

| Protocol Adapter | 状态 |
| --- | --- |
| OpenAI Chat Completions | 可用 |
| OpenAI Responses | 路线图 |
| Anthropic Messages | 路线图 |
| Gemini | 路线图 |

### 当前端点

| 方法 | 路径 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| `GET` | `/healthz` | 无 | 存活检查 |
| `GET` | `/v1/models` | Bearer API Key | 获取可用模型 |
| `POST` | `/v1/chat/completions` | Bearer API Key | 普通或流式文本请求 |

当前 MVP 能力：

- `system`、`developer`、`user`、`assistant` 文本消息；
- 普通 JSON 与 `stream: true` 的 SSE 响应；
- 单候选 `n=1`；
- DeepSeek-style `reasoning_content`；
- 用户池没有在线真人时进入可配置的 fallback；
- 与当前协议一致的错误结构。

MVP 目前聚焦文本；Tool Calling、Structured Output、多模态内容及其他 Adapter 的进度见[开发路线](https://github.com/Xbai-hang/sotapi/wiki/Development-Roadmap)。

## Telegram 模式

| 模式 | 工作方式 | 适合 |
| --- | --- | --- |
| `polling`（默认） | SotAPI 通过 `getUpdates` 主动获取回复 | 本地开发、VPS、IP 或内网部署 |
| `webhook` | Telegram 主动把回复推送到 SotAPI | 已有公网域名与可信 HTTPS 证书的部署 |

两种模式的 API 和回答体验相同，只需修改 `telegram.update_mode` 后重启。一个 Telegram Bot 不应同时交给 SotAPI 和其他接收服务使用；polling 与 webhook 也不能在 Telegram 端同时生效。

真人默认在线。开启 `human.auto_offline.enabled` 后，连续达到配置次数仍未在响应时限内回复会自动离线，并收到 Telegram 通知；发送 `/online` 可恢复在线并清零连续未回复次数。

Fallback 默认直接返回 `fallback.template`。将 `fallback.mode` 设为 `llm` 后，用户池没有在线真人时会先请求 `fallback.llm` 中配置的 OpenAI-compatible Chat Completions 服务；调用失败时仍返回模板：

```yaml
fallback:
  mode: llm
  template: "The human responder is currently unavailable. Please try again later."
  llm:
    base_url: "https://api.openai.com"
    api_key: ""
    model: "your-fallback-model"
    timeout: 30s
    stream: false
```

建议通过 `SOTAPI_FALLBACK_LLM_API_KEY` 环境变量提供模型密钥。`stream` 控制 SotAPI 与 fallback 模型之间是否使用原生流式响应；Caller 的响应格式仍由其请求中的 `stream` 决定。

LLM 调用使用请求副本：Caller 提供的 `system` 和 `developer` 消息会被移除，并替换为 SotAPI 的内置 system prompt；投递给真人的原始消息不受影响。

Webhook 的 HTTPS、Nginx/OpenResty 反向代理与超时配置见[部署指南](https://github.com/Xbai-hang/sotapi/wiki/Deployment#https-webhook-部署)。

## 玩之前看一眼

- 每次请求的完整文本上下文都会发送到配置的 Telegram 会话，请只选择可信回答者。
- `configs/config.yaml` 包含 API Key 和 Bot Token，已被 Git 忽略；请限制文件权限并保护备份。LLM fallback 的密钥建议仅通过环境变量注入。
- 公网 API 应同时使用 HTTPS 与 `auth.mode: api_key`。
- 默认 `human.response_timeout` 为 5 分钟；反向代理的读取超时必须大于该值。
- 当前 Pending 请求、回复关联、在线状态和统计保存在内存中；进程重启会终止所有在途请求，并将真人恢复为默认在线。
- 当前每个用户池只能配置一名回答者，单实例部署最符合现阶段的状态模型。

## 文档

| 文档 | 内容 |
| --- | --- |
| [项目介绍](https://github.com/Xbai-hang/sotapi/wiki/Project-Introduction) | 工作方式、使用场景与信任边界 |
| [部署指南](https://github.com/Xbai-hang/sotapi/wiki/Deployment) | Telegram、Docker Compose、webhook 与运维 |
| [架构设计](https://github.com/Xbai-hang/sotapi/wiki/Architecture) | 组件职责、请求生命周期与状态模型 |
| [开发路线](https://github.com/Xbai-hang/sotapi/wiki/Development-Roadmap) | 当前边界与后续里程碑 |

## 开发

本地开发需要 Go 1.27 或更高版本：

```bash
go run ./cmd/sotapi config validate --config configs/config.example.yaml
make verify
```

欢迎通过 [Issues](https://github.com/Xbai-hang/sotapi/issues) 报告问题或提出场景，也欢迎提交 [Pull Requests](https://github.com/Xbai-hang/sotapi/pulls)。

## License

[MIT](LICENSE)
