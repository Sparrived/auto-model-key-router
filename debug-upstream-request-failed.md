# Debug Session: upstream-request-failed

Status: [OPEN]

## Symptom

用户反馈客户端显示 `Upstream request failed`，但服务运行日志显示本地 `/v1/chat/completions` 收到了请求，并且 uvicorn access log 返回 HTTP 200。

## Initial Hypotheses

1. 上游接口实际返回了一个 200 响应，但响应体是错误事件或非 OpenAI-compatible 格式，客户端把它展示为 `Upstream request failed`。
2. 流式请求中上游连接在响应头返回后中断，本地 access log 仍记录 200，但流内容里出现异常或被截断。
3. 当前代理只记录 access log，没有记录上游 URL、状态码、异常类型和请求是否流式，导致 200 是本地代理状态而不是上游结果。
4. 客户端连接使用 IPv6/代理环境正常到达本地服务，但上游 `base_url`、模型名或鉴权 key 与请求不匹配，错误被代理透传后被客户端改写展示。
5. 请求体 `stream`、模型 alias 或路径解析触发了非预期分支，导致计量/响应路径与客户端预期不一致。

## Evidence Plan

- 添加最小运行时诊断上报，记录代理入口、上游请求、上游响应、上游异常、流式关闭事件。
- 复现一次请求后读取诊断日志，根据日志确认或排除假设。

## Evidence

- 用户提供的上游错误明确显示：`The 'trae-gpt-5.5' model is not supported when using Codex with a ChatGPT account.`
- 本地客户端请求使用的是外部别名 `trae-gpt-5.5`，当前代理虽然用别名解析到了本地真实模型配置和 key 池，但转发给上游时仍保留了原始请求体里的 `model` 字段。
- 因此上游收到的是别名而不是真实模型 ID，导致上游按不支持的模型拒绝请求。

## Confirmed Root Cause

当前代理在别名解析后只用真实模型 ID 选择 key，没有在发送上游请求前把请求体中的 `model` 改写为真实模型 ID。

