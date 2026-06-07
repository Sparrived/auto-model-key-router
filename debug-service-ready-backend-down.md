# Debug Session: service-ready-backend-down

Status: [OPEN]

## Symptom
用户反馈：服务显示已启用，但后端似乎没有真正启用。

## Hypotheses
1. 系统服务/计划任务处于 Ready，但实际进程未启动或启动后立即退出。
2. 后端进程已启动，但绑定的 host/port 与当前配置页面检查的 host/port 不一致。
3. `/health` 健康检查请求失败，因为服务绑定到 IPv6、特定网卡地址或 localhost 映射不一致。
4. 后端启动失败源于配置加载、模型配置为空、端口占用或依赖异常，错误只写入日志文件。
5. PID 文件残留或来自旧进程，导致页面辅助信息与实际后端状态不一致。

## Evidence Plan
- 查看当前配置中的监听地址、端口、日志路径、PID 文件路径。
- 查看后端日志中是否有 uvicorn 启动、绑定失败或异常退出信息。
- 运行实时健康检查与 CLI 状态查询，对比系统服务状态。

## Notes
等待运行时证据。
