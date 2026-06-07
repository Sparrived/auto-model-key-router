# Debug Session: trae-invalid-request

Status: [OPEN]

## Symptom
Trae 调用本地路由失败，服务日志出现 `WARNING uvicorn.error Invalid HTTP request received.`

## Hypotheses
1. Trae 配置使用了 `https://127.0.0.1:8000` 或 HTTPS 协议，但本服务只提供 HTTP。
2. Trae 配置的 base URL 路径不正确，例如缺少或重复 `/v1`，导致请求没有进入代理路由。
3. Trae 使用了代理、CONNECT 或非 HTTP/1.1 数据直接发到 uvicorn 端口。
4. Trae 请求没有携带本地 API key，真实失败是鉴权 401，Invalid HTTP request 是额外噪声。
5. 本地服务监听正常，但 Trae 连接到了错误端口或旧实例。

## Evidence Plan
- 确认当前服务健康检查、监听地址和模型列表。
- 查看最近运行日志和请求统计，确认是否有有效 `/v1/...` 请求进入应用。
- 用 HTTP 与 HTTPS 分别请求本地端口，复现 uvicorn 的 Invalid HTTP request 日志特征。
- 检查本地鉴权 key 和 Trae 应配置的 base URL。

## Notes
等待证据采集。
