# SotAPI

<div align="center">
<p><em>An LLM API, powered by actual humans.</em></p>
<p><strong>由「能工智人」驱动的多协议 LLM API 网关</strong></p>
<p align="center">
  <a href="https://github.com/Xbai-hang/sotapi/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/Xbai-hang/sotapi/ci.yml?style=flat-square&label=CI&logo=githubactions&logoColor=white" alt="CI" /></a>
  <a href="https://codecov.io/gh/Xbai-hang/sotapi"><img src="https://img.shields.io/codecov/c/github/Xbai-hang/sotapi?style=flat-square&logo=codecov" alt="Codecov" /></a>
  <a href="https://github.com/Xbai-hang/sotapi/releases"><img src="https://img.shields.io/github/v/release/Xbai-hang/sotapi?style=flat-square&logo=github" alt="Release" /></a>
  <a href="https://github.com/Xbai-hang/sotapi/pkgs/container/sotapi"><img src="https://img.shields.io/github/v/release/Xbai-hang/sotapi?style=flat-square&label=image&logo=docker&logoColor=white" alt="Container" /></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Xbai-hang/sotapi?style=flat-square&logo=go&logoColor=white" alt="Go" /></a>
  <a href="https://core.telegram.org/bots/api"><img src="https://img.shields.io/badge/channel-Telegram-26A5E4?style=flat-square&logo=telegram&logoColor=white" alt="Telegram" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Xbai-hang/sotapi?style=flat-square" alt="License" /></a>
  <a href="https://github.com/Xbai-hang/sotapi/wiki"><img src="https://img.shields.io/badge/docs-Wiki-2F81F7?style=flat-square&logo=github&logoColor=white" alt="Wiki" /></a>
</p>
<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#客户端接入">客户端接入</a> ·
  <a href="https://github.com/Xbai-hang/sotapi/wiki/Deployment">部署指南</a> ·
  <a href="https://github.com/Xbai-hang/sotapi/wiki/Development-Roadmap">开发路线</a>
</p>
</div>
客户端像调用标准大模型一样发起请求，SotAPI 将完整上下文投递到 Telegram 等 Channel 会话，**「能工智人」**在 IM 软件中「回复（Reply）」，SotAPI 将**「能工智人」**的回复转化为标准的模型协议响应。

<p align="center">
  <img src="assets/workflow.svg" alt="SotAPI Request Lifecycle" width="100%" />
</p>

## 核心特性

- 🎭 **能工智人即模型**：兼容 OpenAI API 规范，无缝接入各类支持自定义 Base URL 的客户端（如 Cherry Studio、RikkaHub 等）。
- 💬 **零操作后台**：「能工智人」直接在 Telegram 中使用「回复」即可应答，无需安装或登录任何专用后台。
- 🔄 **灵活降级（Fallback）**：「能工智人」离线时，支持返回预设提示模板，或自动无缝降级为调用上游真实大模型（如 OpenAI）。
- ⚡ **零公网依赖**：默认 Telegram Polling 模式，无需公网 IP、域名或 HTTPS 证书即可在本地/内网跑通；生产环境亦支持高并发 Webhook 模式。
- 🌊 **完整协议支持**：支持普通 JSON 与 SSE 流式响应（`stream: true`），原生支持 DeepSeek 风格 `reasoning_content`（思考过程）。
- 🛡️ **生产级治理**：内置 Token 鉴权、响应超时控制、能工智人连续未回复自动离线（支持 `/online` 指令上线恢复）与运行时配置热重载。

## 快速开始

推荐使用 Docker Compose 和默认的 Telegram Polling 模式极速起步。

### 1. 准备配置

克隆项目并复制配置文件：

```bash
git clone https://github.com/Xbai-hang/sotapi.git
cd sotapi
cp configs/config.example.yaml configs/config.yaml
```

打开 `configs/config.yaml`，填入以下 3 项必要信息：

```yaml
auth:
  api_keys:
    - "replace-with-a-random-secret"      # 接口调用密钥（例如 openssl rand -hex 32）

telegram:
  bot_token: "replace-with-bot-token"     # Telegram Bot Token（由 @BotFather 获取）

users:
  - id: "alice"
    channel: "telegram"
    recipient: "123456789"                # 「能工智人」智人的 Telegram Chat ID
```

> **获取 Chat ID**：先在 Telegram 中向创建的 Bot（通过 [@BotFather](https://t.me/@BotFather) 创建）发送 `/start`，随后访问 `https://api.telegram.org/bot<BOT_TOKEN>/getUpdates` 即可在返回 JSON 中找到 `message.chat.id`。

### 2. 启动服务

```bash
docker compose up -d
```

### 3. 发起请求

向 SotAPI 发送标准 Chat Completion 请求：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-a-random-secret' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "sotapi-human",
    "messages": [
      {"role": "user", "content": "你好，请问当前版本发布了吗？"}
    ]
  }'
```

此时请求将保持挂起，Telegram 会收到一条任务消息。**「能工智人」使用 Telegram 的「回复（Reply）」功能回复该消息后**，客户端即可实时收到响应。

## 客户端接入

在任何支持自定义 OpenAI API 的客户端中配置：

| 配置项 | 推荐值 | 说明 |
| :--- | :--- | :--- |
| **API Base URL** | `http://localhost:8080/v1` | 确保最终请求指向 `/v1/chat/completions` |
| **API Key** | `configs/config.yaml` 中配置的密钥 | 对应 `auth.api_keys` |
| **Model** | `sotapi-human` | 对应 `models[].id` |

### API 端点

| 方法 | 路径 | 鉴权 | 说明 |
| :--- | :--- | :--- | :--- |
| `GET` | `/healthz` | 无 | 服务健康存活检查 |
| `GET` | `/v1/models` | Bearer Key | 获取当前可用模型列表 |
| `POST` | `/v1/chat/completions` | Bearer Key | 文本对话（支持普通 JSON & SSE 流式输出） |

### 协议支持进度

| 协议适配器 | 状态 | 说明 |
| :--- | :--- | :--- |
| **OpenAI Chat Completions** | ✅ 可用 | 支持普通消息、流式 SSE、`reasoning_content` |
| **OpenAI Responses** | 🗺️ 路线图 | 规划中 |
| **Anthropic Messages** | 🗺️ 路线图 | 规划中 |
| **Google Gemini** | 🗺️ 路线图 | 规划中 |

## 核心机制与配置

### 运行模式切换

SotAPI 支持在 `telegram.update_mode` 中自由切换：
- **`polling`（默认）**：SotAPI 主动长轮询 Telegram 服务器拉取消息，适合本地开发、个人 VPS、内网部署，无需公网 IP 和证书。
- **`webhook`**：Telegram 主动将消息推送到 SotAPI 提供的回调地址，适合已有公网域名和有效 HTTPS 证书的高并发生产环境。

### 自动离线与上线

- **自动离线**：开启 `human.auto_offline.enabled: true` 后，当**「能工智人」**连续达到指定次数未在规定时限（默认 5 分钟）内回复时，系统会自动标记为离线并发送通知。
- **快速恢复**：在 Telegram 中发送 `/online` 指令，即可重新上线并清零未回复计数。

### 智能 Fallback（降级机制）

当用户池中无在线的「能工智人」可用时，SotAPI 支持两种降级策略：
- **模板回复（`mode: template`）**：直接返回配置的友好提示文本（如 *"The human responder is currently unavailable. Please try again later."*）。
- **上游 LLM 接管（`mode: llm`）**：自动将请求转发给上游 OpenAI 兼容的大模型处理（建议通过环境变量 `SOTAPI_FALLBACK_LLM_API_KEY` 注入密钥）。

```yaml
fallback:
  mode: llm
  template: "The human responder is currently unavailable. Please try again later."
  llm:
    base_url: "https://api.openai.com"
    model: "gpt-4o-mini"
    timeout: 30s
    stream: false
```

## 文档导航

更深入的部署与架构细节请参阅项目 Wiki：

| 文档 | 主要内容 |
| :--- | :--- |
| [📖 项目介绍](https://github.com/Xbai-hang/sotapi/wiki/Project-Introduction) | 设计初衷、核心场景与信任边界 |
| [🚀 部署指南](https://github.com/Xbai-hang/sotapi/wiki/Deployment) | Telegram Bot 详细配置、Docker Compose、Nginx HTTPS 反向代理 |
| [🏗️ 架构设计](https://github.com/Xbai-hang/sotapi/wiki/Architecture) | 请求生命周期、状态机模型与核心组件 |
| [🗺️ 开发路线](https://github.com/Xbai-hang/sotapi/wiki/Development-Roadmap) | 多模态支持、Tool Calling、结构化输出等规划 |

## 本地开发

本项目使用 Go 开发，需 Go 1.27+ 环境：

```bash
# 验证配置文件格式与正确性
go run ./cmd/sotapi config validate --config configs/config.example.yaml

# 运行代码检查与分层覆盖率测试 (vet + coverage)
make verify
```

## 社区 & 贡献

> 欢迎提交 [Issues](https://github.com/Xbai-hang/sotapi/issues) 反馈建议，或发起 [Pull Requests](https://github.com/Xbai-hang/sotapi/pulls) 参与贡献。

- 社区推荐：[Linux.do](https://linux.do)

## License

[MIT](LICENSE)
