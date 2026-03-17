# DataPipe 开发文档

## 目标

在 **Proxy（公网）** 与 **Forwarder（内网）** 之间引入 DataPipe 抽象层，使请求可以通过 NATS 转发，并支持流式响应。

## 代码结构

新增/核心目录：

```
pkg/datapipe/
├── datapipe.go            # DataPipe 接口 + 注册表
└── nats/
    └── nats.go            # NATS DataPipe 实现

cmd/
├── root.go                # CLI 根命令（无子命令默认 serve）
├── serve.go               # 完整服务模式
├── proxy.go               # 透明代理模式（HTTP -> NATS）
└── forwarder.go           # 转发器模式（NATS -> 内部 Relay -> 外部 LLM）
```

入口：

- `main.go`：嵌入 `web/build/*` 并执行 `cmd.Execute()`。

## DataPipe 接口

`pkg/datapipe/datapipe.go`：

- `DataPipe`：初始化、提供 `http.RoundTripper`、以及（可选）监听端（Forwarder 用）。
- 全局注册表：`Register/Get/List`。

## NATS 实现（当前协议）

- 请求：JSON（方法/URL/Headers/Body(base64)）。
- 响应：先发 `header`，再发若干 `chunk`，最后 `eof`。
- 约定：Proxy 通过 `nats://<subject>/<path>` 构造 URL，`<subject>` 即 NATS subject（例如 `llm.requests`）。
- 可选：`max_body_bytes` 限制通过 NATS 转发的请求体大小（Proxy 侧）。
- 可选：`nats_queue` 让多个 Forwarder 以 queue group 方式消费同一 subject。

## 开发/调试

建议先用本机联调（同一台机器上跑 NATS、Proxy、Forwarder）：

```bash
# 1) 启动 NATS（示例）
# nats-server

# 2) 启动 Forwarder（默认会起内部 Relay: 127.0.0.1:13000）
./one-api forwarder --nats-url nats://127.0.0.1:4222 --nats-subject llm.requests

# 3) 启动 Proxy
./one-api proxy --nats-url nats://127.0.0.1:4222 --nats-subject llm.requests --port 3000

# 4) 发请求到 Proxy
curl http://127.0.0.1:3000/v1/chat/completions \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}'
```

## 已知限制（重要）

- **大请求体**：当前请求体整体 base64 进 NATS，易触发 `max_payload`（音频/文件上传风险最大）。
- **安全**：Header/Body 进入消息队列前未做加密/脱敏；生产需配置 NATS TLS/mTLS 与 ACL。
- **可靠性**：当前链路偏 at-most-once（基于 request/reply），需要更强可靠性需引入 JetStream/重试/幂等。
