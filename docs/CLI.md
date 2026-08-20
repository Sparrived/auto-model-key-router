# CLI 参数文档

Auto Model Key Router 安装后提供两个等价命令：

```text
amkr
auto-model-key-router
```

也可以通过模块运行：

```bash
python -m auto_model_key_router.main
```

## 基本语法

```bash
amkr [全局参数] [操作参数]
```

不提供操作参数时启动 Terminal UI：

```bash
amkr
```

建议每次只使用一个操作参数。程序没有为这些参数建立互斥组；同时传入多个操作时，只会执行优先级最高的一个。

## 配置文件

`--config` 未指定时使用系统应用缓存目录：

| 系统 | 默认路径 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\AutoModelKeyRouter\router-config.json` |
| macOS | `~/Library/Caches/AutoModelKeyRouter/router-config.json` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/router-config.json` |

首次加载不存在的配置时会创建配置；配置缺少 `local_api_key` 时会自动生成并写回。

## 全局参数

| 参数 | 值 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h`, `--help` | 无 | - | 显示帮助并退出 |
| `--version` | 无 | - | 显示当前版本并退出 |
| `--config PATH` | 路径 | 系统默认路径 | 指定要读取或创建的配置文件 |
| `--host HOST` | 地址 | 配置值 | 在当前 CLI 进程中覆盖监听地址 |
| `--port PORT` | 整数 | 配置值 | 在当前 CLI 进程中覆盖监听端口 |

`--host` 和 `--port` 不会写回配置文件。后台启动和系统服务会在子进程中重新读取配置文件，因此要永久修改监听地址，应通过 Terminal UI 或直接修改配置文件。

## 配置与日志

| 参数 | 说明 |
| --- | --- |
| `--show-config` | 显示配置摘要后退出，不启动服务 |
| `--show-address` | 显示 AMKR 的监听 IP、端口和服务地址后退出，不启动服务 |
| `--show-unified-model` | 显示请求模型 `unified-model` 当前指向的真实模型和 Key |
| `--show-logs [N]` | 打开调用日志界面，并显示最近 N 行运行日志；省略 N 时为 20 |

示例：

```bash
amkr --config router-config.json --show-config
amkr --config router-config.json --show-address
amkr --config router-config.json --show-logs
amkr --config router-config.json --show-logs 100
```

调用统计明细在日志界面中固定为每页 10 行；`N` 只控制运行日志的初始行数。

## 统一模型切换

配置文件字段名为 `unified_model`，客户端请求时使用的虚拟模型 ID 为 `unified-model`。

| 参数 | 值 | 说明 |
| --- | --- | --- |
| `--switch-model MODEL` | 模型 ID 或别名 | 把 `unified_model` 指向已有模型；写回时规范化为真实模型 ID |
| `--switch-key KEY` | Key 名称或 `auto` | 固定使用已有且启用的 Key；`auto` 恢复自动路由 |
| `--show-unified-model` | 无 | 查看当前指向 |

示例：

```bash
amkr --config router-config.json --switch-model gpt-5.5
amkr --config router-config.json --switch-model gpt-5.5 --switch-key main
amkr --config router-config.json --switch-key backup
amkr --config router-config.json --switch-key auto
amkr --config router-config.json --show-unified-model
```

行为说明：

- `--switch-key` 单独使用时，配置中必须已经存在 `unified_model`。
- 切换到另一模型且没有同时提供 `--switch-key` 时，会清除原固定 Key，恢复自动路由。
- MODEL 不存在、Key 不存在或 Key 已禁用时，命令返回失败且不写入无效配置。
- 修改会原子写回配置文件；运行中的服务会在后续请求时热重载。

## 后台进程

这组参数管理 CLI 自己启动的后台进程，不等同于 Windows 计划任务或 systemd user service。

| 参数 | 说明 |
| --- | --- |
| `--serve` | 跳过 Terminal UI，以独立后台进程启动服务 |
| `--status` | 查询配置中 `host`、`port` 对应服务的健康状态 |
| `--stop` | 根据 PID 文件停止 CLI 后台进程 |

示例：

```bash
amkr --config router-config.json --serve
amkr --config router-config.json --status
amkr --config router-config.json --stop
```

`--serve` 启动的子进程会重新读取 `--config` 指定的文件。日志路径和 PID 文件路径取自该配置。

## 系统服务

### 快捷安装

```bash
amkr --config router-config.json --install-service
```

等价于：

```bash
amkr --config router-config.json --service install
```

### `--service ACTION`

可用 ACTION：

| ACTION | Windows | Linux | 说明 |
| --- | --- | --- | --- |
| `install` | 支持 | 支持 | 注册并立即启动系统服务 |
| `install-user` | 支持 | 不支持 | 注册当前用户登录时启动的 Windows 计划任务 |
| `uninstall` | 支持 | 支持 | 停止并删除注册 |
| `start` | 支持 | 支持 | 启动已注册服务 |
| `stop` | 支持 | 支持 | 停止已注册服务 |
| `restart` | 支持 | 支持 | 重启已注册服务 |
| `status` | 支持 | 支持 | 同时显示 CLI 后台服务和系统服务状态 |
| `install-elevated` | 内部使用 | 不支持 | 已提升权限后的安装动作 |
| `uninstall-elevated` | 内部使用 | 不支持 | 已提升权限后的卸载动作 |
| `start-elevated` | 内部使用 | 不支持 | 已提升权限后的启动动作 |
| `stop-elevated` | 内部使用 | 不支持 | 已提升权限后的停止动作 |
| `restart-elevated` | 内部使用 | 不支持 | 已提升权限后的重启动作 |

常用示例：

```bash
amkr --config router-config.json --service install
amkr --config router-config.json --service status
amkr --config router-config.json --service restart
amkr --config router-config.json --service uninstall
```

Windows：

- `install` 注册计划任务 `AutoModelKeyRouter`，触发方式为开机启动，使用 `SYSTEM` 账户和 `HIGHEST` 权限。
- 当前终端没有管理员权限时，`install`、`uninstall`、`start`、`stop`、`restart` 会请求 UAC 提权。
- `install-user` 注册当前用户登录时启动的交互式 `LIMITED` 任务，通常不需要管理员权限。

Linux：

- 注册 `~/.config/systemd/user/auto-model-key-router.service`。
- `install` 执行 `systemctl --user enable --now`，并尝试启用 linger。
- linger 失败不会删除已经注册的 user service，但用户未登录时可能无法自启。

其他系统当前不支持自动注册系统服务。

## 版本检查与更新

| 参数 | 说明 |
| --- | --- |
| `--check-update` | 通过 PyPI 检查新版本，失败时回退 GitHub Release |
| `--update` | 检查并更新到最新版本 |

```bash
amkr --check-update
amkr --config router-config.json --update
```

`--check-update` 在加载配置文件前执行。`--update` 会先加载配置，并根据 pip、pipx、uv tool 或 uvx 等安装方式选择更新命令。

## 内部参数

以下参数不会显示在 `--help` 中，主要由后台进程、系统服务和 Windows 更新器调用，不建议手动使用。

| 参数 | 说明 |
| --- | --- |
| `--serve-foreground` | 在当前进程中运行 Uvicorn 服务 |
| `--restart-service-after-update` | 更新完成后按现有注册状态恢复服务 |

## 多操作参数优先级

如果同时传入多个操作参数，程序按以下顺序执行第一个匹配项：

1. `--version` 或 `--help`（由参数解析器直接处理）
2. `--check-update`
3. `--switch-model` / `--switch-key`
4. `--show-unified-model`
5. `--update`
6. `--restart-service-after-update`
7. `--show-logs`
8. `--show-address`
9. `--show-config`
10. `--stop`
11. `--status`
12. `--install-service`
13. `--service`
14. `--serve-foreground`
15. 无 `--serve` 时进入 Terminal UI
16. `--serve`

例如同时传入 `--show-config --serve` 时，只显示配置，不会启动服务。

## 退出状态

| 状态码 | 说明 |
| --- | --- |
| `0` | 命令正常完成，或由 argparse 正常显示帮助/版本 |
| `1` | 配置加载失败或统一模型切换失败 |
| `130` | 用户按下 `Ctrl+C` |

