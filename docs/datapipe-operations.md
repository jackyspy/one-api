# DataPipe 操作手册

## 概述

DataPipe 用于把 One API 的外部请求拆成两段：

- **Proxy（公网）**：对外提供 HTTP（OpenAI compatible）入口，把请求转发到 NATS。
- **Forwarder（内网）**：从 NATS 收到请求后，交给 One API 的 Relay 逻辑处理，再把响应通过 NATS 返回。

> 建议：Proxy 放公网、Forwarder 放内网；NATS 作为两者之间的消息通道。

## 架构

```
Agent -> HTTP -> Proxy -> NATS -> Forwarder -> HTTP -> LLM API
```

## 快速开始

### 1) 启动 NATS

确保 Proxy 与 Forwarder 都能连接到同一个 NATS：

- `nats://<host>:4222`

### 2) 编译

```bash
go build -o one-api .
```

### 3) 启动 Forwarder（内网）

```bash
./one-api forwarder \
  --nats-url nats://127.0.0.1:4222 \
  --nats-subject llm.requests
```

Forwarder 默认会启动内部 Relay：`127.0.0.1:13000`。

### 4) 启动 Proxy（公网）

```bash
./one-api proxy \
  --nats-url nats://127.0.0.1:4222 \
  --nats-subject llm.requests \
  --port 3000
```

### 5) 测试

```bash
curl http://127.0.0.1:3000/v1/chat/completions \
  -H "Authorization: Bearer test" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Hello"}]}'
```

## 常用命令

```bash
./one-api --help
./one-api --version

./one-api serve --help
./one-api proxy --help
./one-api forwarder --help
```

> 无子命令直接运行 `./one-api` 等价于 `./one-api serve`。

可选参数（对应环境变量）：

- Proxy：`--max-body-bytes`（`NATS_MAX_BODY_BYTES`）
- Forwarder：`--nats-queue`（`NATS_QUEUE`）

## 关键环境变量（与 CLI 参数等价）

- `PORT`：Proxy/Serve 对外监听端口
- `NATS_URL`：NATS 地址（Proxy/Forwarder 必填）
- `NATS_SUBJECT`：NATS subject（Proxy/Forwarder 必填，需一致）
- `NATS_QUEUE`：Forwarder 的 NATS queue group（可选，多 forwarder 负载均衡）
- `NATS_MAX_BODY_BYTES`：Proxy 通过 NATS 转发的最大请求体（字节，默认 524288）
- `FORWARDER_INTERNAL_PORT`：Forwarder 内部 Relay 端口（默认 13000）
- `SQL_DSN`：Forwarder 使用的数据库 DSN（默认 SQLite）

> Redis：`REDIS_CONN_STRING` 与 `SYNC_FREQUENCY` 都需要设置才会启用 Redis（详见源码逻辑）。

## 数据库（Forwarder）

Forwarder 需要数据库存储 Channel 配置。

- 默认：SQLite 文件 `one-api.db`（当前目录）
- 可选：MySQL/PostgreSQL（通过 `SQL_DSN`）
- SQLite DSN 扩展：支持 `sqlite:/path/to/one-api.db`、`sqlite::memory:`

## 生产注意事项

- **安全**：务必给 NATS 配置 TLS/mTLS + ACL；不要让 NATS 裸奔在公网。
- **大请求体**：音频/文件上传可能触发 NATS payload 限制；建议限制接口或做分片/外置存储。
- **可观测性**：建议给 Proxy/Forwarder 单独配置日志目录与守护进程（systemd/pm2/supervisor）。
