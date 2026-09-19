# Jev Adaptive Thinking

CLIProxyAPI 动态库插件：仅对 `model: "jev-auto"` 的请求，根据首次用户 prompt
选择模型，并在当前进程内固定同一会话的 provider/model。

默认按任务分三档：简单任务用 `deepseek-v4-flash:deepseek`，常规开发用
`gpt-5.6-sol`，复杂推理、架构和困难调试用 `gpt-5.6-astra`。
Jev 超时、失败或缺少可用首轮文本时，回退并锁定 `gpt-5.6-sol`。

## 构建与离线验证

需要 Go 1.26+、C 编译器及 CGO。SDK 固定为 CLIProxyAPI `v7.3.8`
（C ABI 1 / schema 6），建议使用该版本宿主；旧版本兼容性尚未 live 验证。

```bash
make test
make build
```

macOS 输出 `build/jev-adaptive-thinking.dylib`，Linux 输出
`build/jev-adaptive-thinking.so`。在目标系统和架构上构建；暂未验证 Windows。
离线测试只使用内存、临时文件和本地模拟 HTTP 服务，不调用 OpenRouter。

## 配置与安装

1. 将 `config.example.json` 复制为 `config.json`，替换三个 `provider` 占位值。
   `model` 是该 provider 在宿主中可执行的模型 ID/别名，按实际配置调整。
   **占位 provider 会导致注册失败**，不会猜测真实 provider。
   示例中保留的 `:deepseek` 是模型 ID 的一部分，不会被拆解为 provider。
2. 把动态库放入宿主的插件发现目录，并配置下面的 YAML。示例适用于
   `v7.3.8`：使用 `plugins.dir`，不是 `plugins.path`。路径使用绝对路径。
3. 为 **CLIProxyAPI 进程**提供 `OPEN_ROUTER_API_KEY`。如果宿主由服务管理器
   启动，需要在服务环境中设置；终端里设置的变量不一定会传给已有服务。
4. 由你启动/重启测试宿主并执行下面的 live testing。本项目不会自动修改宿主配置。

```yaml
plugins:
  enabled: true
  dir: "/absolute/path/to/jev-adaptive-thinking/build"
  configs:
    jev-adaptive-thinking:
      enabled: true
      priority: 1
      config_path: "/absolute/path/to/jev-adaptive-thinking/config.json"
```

Jev 请求直接发送到 `https://openrouter.ai/api/alpha/decisions`，默认模型
`typesafe/jev-1.13`。JSON 可配置 `jev_model`、`timeout_ms`（默认 1500）、
`fallback`（默认 `standard`）和 `candidates`。每个候选包含唯一 `key`、
显式小写 `provider`、`model` 和 `description`；provider/key 使用小写字母开头，
后续支持小写字母、数字、下划线和连字符，最长 64 字符。
密钥不写入 JSON。描述参与 Jev 分类，新增/调整模型时一起维护。

JSON 在 `plugin.register` / `plugin.reconfigure` 时读取。修改 JSON 文件本身
不会触发宿主的 YAML watcher；需要通过宿主触发插件重新配置，或重启宿主。
插件内部拒绝无效更新并保留旧配置；有效更新只影响新会话。
重启会清空所有会话绑定；若宿主通过卸载重载插件应用配置，也会重置绑定。

## 会话与请求行为

- 支持 Chat Completions、Responses、Claude Messages；流式与非流式共用绑定。
- 同一会话的并发首轮请求只调用一次 Jev。后续请求不再分类，包括工具续接、
  历史压缩、任务变复杂和上游执行失败。上游错误交给宿主处理。
- 会话以调用方身份摘要及明确 session/thread/conversation ID 隔离。
  优先级依次为 `X-Claude-Code-Session-Id`、Claude `metadata.user_id` 中的会话 ID、
  `Session-Id` / `Session_id`、`X-Session-ID`、`X-Conversation-Id`、
  `X-Thread-Id` / `Thread-Id` / `Thread_id`、body 中的
  `session_id` / `sessionId` / `thread_id` / `conversation_id`、metadata 同名字段、
  `conversation.id`。
- 子代理通过独立 session ID，或 `X-Claude-Code-Agent-Id` /
  `metadata.agent_id` / `metadata.subagent_id`（含 Claude 结构化 user_id）隔离。
  `parent_session_id` 不会被用来继承父会话模型。
- 不将 request ID、`previous_response_id`、普通 user ID、`prompt_cache_key`
  或消息内容当作会话 ID。没有稳定会话 ID 时直接走 fallback，不调用 Jev、不缓存。
- 首条用户消息支持字符串或文本块；忽略 system/developer 和工具结果。
  首轮含图片/音频/其他非文本内容时使用 fallback。Jev 输入最多前 8000 个
  Unicode 字符；原始请求不被截断。插件中途加入历史会话时，使用请求中可见的
  最早用户消息，无法恢复客户端未发送的历史。
- 绑定没有 TTL 或自动淘汰，只在进程/插件实例重建后清空。内存占用随会话数增长。
  部署按单进程设计，不共享多进程缓存。
- 手动指定其他模型会完全绕过插件；同会话再用 `jev-auto` 时恢复已有绑定。
- 插件不注册模型目录条目；客户端需支持直接输入 `jev-auto`。宿主没有对应
  provider/模型或该 provider 不支持所用协议时，请求可能失败，不自动改选模型。
- 首轮文本会发送给 OpenRouter/Jev 分类。Jev HTTP 客户端使用标准代理环境变量
  （如 `HTTPS_PROXY`），不继承 CLIProxyAPI 的私有 `proxy-url` 设置。

## 本地调试日志

固定路径由运行用户的 home 目录确定，与工作目录及 JSON 配置位置无关：

```text
$HOME/.local/state/jev-adaptive-thinking/logs/router.jsonl
```

Jason 当前机器对应：
`/Users/jasonxu/.local/state/jev-adaptive-thinking/logs/router.jsonl`。
首次初始化时自动创建日志目录；目录权限 `0700`，文件权限 `0600`。
每条事件立即追加写入，10 MiB 轮转，保留 `.1` 到 `.5` 共五个历史文件。
并发写入在进程内串行化，不逐条 fsync。

```bash
tail -F "$HOME/.local/state/jev-adaptive-thinking/logs/router.jsonl"
```

使用 jq 查看最近的路由或筛选会话：

```bash
jq -c 'select(.event == "decision" or .event == "fallback" or .event == "cache_hit")' \
  "$HOME/.local/state/jev-adaptive-thinking/logs/router.jsonl"

jq -c --arg session "替换为日志中的 session_hash" \
  'select(.session_hash == $session)' \
  "$HOME/.local/state/jev-adaptive-thinking/logs/router.jsonl"
```

事件包括 `initialized`、`config_loaded`、`config_rejected`、`decision`、
`cache_hit`、`fallback`、`shutdown`。字段包含 UTC 时间、PID、插件版本、
配置摘要、关联 ID、会话摘要、协议、流式标记、模型/provider、耗时、回退原因，
以及有效 Jev 响应中的请求 ID 和概率。`elapsed_ms` 是插件路由处理耗时，
不包含目标模型生成时间。非 `jev-auto` 请求不逐条记录。

不记录 prompt、完整请求/响应正文、会话原始 ID、认证头或密钥。
Jev 错误使用固定原因码与 HTTP 状态码，不写原始错误正文。
日志写入失败时继续路由，向 stderr 报告目标路径和原因，同类错误每分钟最多一次。

## 由你执行的 live testing

先在本机导出 `OPEN_ROUTER_API_KEY`，然后可独立验证 Jev 分类：

```bash
make live-test
```

该命令会发送 **三个真实、计费的 Jev 请求**，输出预期档位、实际选择、首轮
路由耗时和缓存命中耗时。它不执行三个目标模型，也不加载动态库或宿主；分类
结果用于人工判断，网络/协议错误或缓存不一致会导致测试失败。

宿主端到端测试需要先填写真实 provider/model 映射并加载动态库。
以下请求使用 `CPA_BASE_URL`（例如 `http://127.0.0.1:8317`）及宿主的
`CPA_API_KEY`，与 OpenRouter key 分开：

```bash
curl "$CPA_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' -H 'Session-Id: jev-debug-chat-1' \
  -d '{"model":"jev-auto","messages":[{"role":"user","content":"Extract the email: jane@example.com"}]}'

curl "$CPA_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' -H 'Session-Id: jev-debug-responses-1' \
  -d '{"model":"jev-auto","input":"Implement a paginated REST endpoint with validation."}'

curl "$CPA_BASE_URL/v1/messages" \
  -H "x-api-key: $CPA_API_KEY" -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' -H 'X-Claude-Code-Session-Id: jev-debug-claude-1' \
  -d '{"model":"jev-auto","max_tokens":256,"messages":[{"role":"user","content":"Design a distributed transaction protocol under network partitions."}]}'
```

每种协议再用相同 session ID、更复杂的文本重发，并加 `"stream":true` 和
curl `-N` 检查流式路径。日志应从一次 `decision`/`fallback` 变为 `cache_hit`，
provider/model 保持一致。需要重新触发 Jev 时换一个 session ID。
再检查无 session ID 固定 fallback、显式指定模型绕过插件、重启后重新决策。
目标模型停用或失败后应保持绑定；实际执行失败详情查看宿主日志。

本次交付完成离线测试与 macOS arm64 动态库构建；真实 Jev 调用、动态库宿主加载、
三种协议的端到端转发及 Linux 构建验证留给部署环境。
