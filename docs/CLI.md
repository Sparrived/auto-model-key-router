# CLI 参数文档

`amkr` 是单一静态二进制，从 GitHub Releases 下载或本地 `go build ./cmd/amkr` 产出。
它同时以 `auto-model-key-router` 这个名字提供（同一份程序的两个入口名）。

安装与升级方式见 [README 的「安装」一节](../README.md#安装)：一行脚本会校验 sha256 后
落盘，`amkr --update` 可就地自更新。

## 提示“找不到 amkr”

这是 PATH 问题：程序已经落盘，但当前终端没找到它。先确认它在哪里：

```bash
# macOS / Linux
command -v amkr

# Windows PowerShell / CMD
where.exe amkr
```

一行安装脚本默认装到 `/usr/local/bin`，没有写权限时退回 `~/.local/bin`；Windows 上装到
`%LOCALAPPDATA%\Programs\AutoModelKeyRouter\amkr.exe`。把该目录加进当前用户 PATH 后
**重新打开终端**——PATH 变化不会自动刷新已经打开的终端、IDE 或系统服务进程。

Windows PowerShell 也可以直接调用完整路径验证：

```powershell
& "$env:LOCALAPPDATA\Programs\AutoModelKeyRouter\amkr.exe" --version
```

## 基本语法

```bash
amkr [全局参数] [操作参数]
```

不提供操作参数时前台启动服务，并自动打开 WebUI：

```bash
amkr
```

日志同时输出到终端与 `log_file_path`。终端交互界面已随 Python 版退役，WebUI
（`http://127.0.0.1:8000/ui/`）是唯一的界面。

建议每次只使用一个操作参数。程序没有为这些参数建立互斥组；同时传入多个操作时，只会执行优先级最高的一个。

## 配置文件

`--config` 未指定时使用系统应用缓存目录：

| 系统 | 默认路径 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\AutoModelKeyRouter\router-config.json` |
| macOS | `~/Library/Caches/AutoModelKeyRouter/router-config.json` |
| Linux | `${XDG_CACHE_HOME:-~/.cache}/auto-model-key-router/router-config.json` |

首次加载不存在的配置时会创建配置；配置缺少 `local_api_key` 时会自动生成并写回。因此，获取 Key 的命令在配置不存在时也会创建配置文件。

> `--show-api-key`、`--get-api-key` 和 `--get-key` 会直接输出完整本地授权 Key。请勿在共享终端、命令历史、日志或 CI 输出中执行。

## 全局参数

| 参数 | 值 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-h`, `--help` | 无 | - | 显示帮助并退出 |
| `--version` | 无 | - | 显示当前版本并退出 |
| `--config PATH` | 路径 | 系统默认路径 | 指定要读取或创建的配置文件 |
| `--host HOST` | 地址 | 配置值 | 在当前 CLI 进程中覆盖监听地址 |
| `--port PORT` | 整数 | 配置值 | 在当前 CLI 进程中覆盖监听端口 |

`--host` 和 `--port` 不会写回配置文件。后台启动和系统服务会在子进程中重新读取配置文件，因此要永久修改监听地址，应通过 WebUI 设置页或直接修改配置文件。

## 启动开关

| 参数 | 说明 |
| --- | --- |
| `--webui` / `--no-webui` | 启用或关闭内置 WebUI，写入配置字段 `webui_enabled` |
| `--no-ops` | 关闭运维接口，写入配置字段 `ops_enabled` |

这两个开关都是**持久化设置**：后台启动与系统服务的子进程只带 `--config`，所以开关必须落盘才能对这三种启动方式都生效。不带开关时不动配置。

`--no-ops` 关闭的是 `/api/logs`、`/api/tool`、`/api/service/*`、`/api/integrations/*`。这些接口作用于「服务所在的这台机器」——读日志文件、启停后台进程、注册系统服务、改写本机 Claude Code / Codex 配置——容器或反向代理后面语义不成立，逐个路径拉黑又容易漏。关闭后这些路径返回 `404`，`/health` 的 `ops_enabled` 字段为 `false`；代理、`/health`、`/metrics`、WebSocket 与 `/api/settings` 等配置管理接口不受影响。

```bash
amkr --config router-config.json --no-ops
```

## 配置与日志

| 参数 | 说明 |
| --- | --- |
| `--show-config` | 显示配置摘要后退出，不启动服务 |
| `--show-address` | 显示 AMKR 的监听 IP、端口和服务地址后退出，不启动服务 |
| `--show-api-key`（别名：`--get-api-key`、`--get-key`） | 输出当前 AMKR 的本地授权 Key 后退出，不启动服务 |
| `--show-unified-model` | 显示请求模型 `unified-model` 当前指向的真实模型和 Key |

示例：

```bash
amkr --config router-config.json --show-config
amkr --config router-config.json --show-address
amkr --config router-config.json --get-key
amkr --config router-config.json --show-unified-model
```

运行日志写在配置的 `log_file_path`（前台启动同时输出到终端）；调用统计与日志明细在
WebUI 的「用量统计」与「服务日志」页。

## 统一模型切换

配置文件字段名为 `unified_model`，客户端请求时使用的虚拟模型 ID 为 `unified-model`。

| 参数 | 值 | 说明 |
| --- | --- | --- |
| `--switch-model MODEL` | 模型 ID 或别名 | 把 `unified_model` 指向已有模型；写回时规范化为真实模型 ID |
| `--switch-key KEY` | Key 名称或 `auto` | 固定使用已有且启用的 Key；`auto` 恢复自动路由 |
| `--unified-target TARGET` | `default.primary` / `default.fallback` / `image.primary` / `image.fallback` / `embeddings.primary` / `embeddings.fallback` | 选择 `--switch-model` / `--switch-key` 要修改的目标，默认 `default.primary` |
| `--show-unified-model` | 无 | 查看当前指向 |

示例：

```bash
amkr --config router-config.json --switch-model gpt-5.5
amkr --config router-config.json --switch-model gpt-5.5 --switch-key main
amkr --config router-config.json --switch-key backup
amkr --config router-config.json --switch-key auto
amkr --config router-config.json --switch-model text-embedding-3-small --unified-target embeddings.primary
amkr --config router-config.json --show-unified-model
```

行为说明：

- `--switch-key` 单独使用时，配置中必须已经存在 `unified_model`。
- 非 `default` 目标（`image`、`embeddings`）必须先配置 `primary` 才能配置 `fallback`；未配置该目标的 `primary` 时，对应请求继承 `default.primary`，且不继承 `default.fallback`。
- 切换到另一模型且没有同时提供 `--switch-key` 时，会清除原固定 Key，恢复自动路由。
- MODEL 不存在、Key 不存在或 Key 已禁用时，命令返回失败且不写入无效配置。
- 修改会原子写回配置文件；运行中的服务会在后续请求时热重载。

> 任务路由（把 `TASK_XXXXXX` 当模型名传，见 [`USAGE.md`](USAGE.md#9-任务路由)）**没有对应的命令行参数**，请在 WebUI 的**任务路由**页或管理 API 的 `/api/tasks` 中维护。它有多个字段（首选/备选模型、一组固定参数），做成命令行开关并不合适。

## 后台进程

这组参数管理 CLI 自己启动的后台进程，不等同于 Windows 计划任务或 systemd user service。

| 参数 | 说明 |
| --- | --- |
| `--serve` | 以独立后台进程启动服务（不占用当前终端） |
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
| `--check-update` | 通过 GitHub Releases 检查新版本 |
| `--update` | 检查并自更新到最新版本，然后自动重启服务 |

```bash
amkr --check-update
amkr --config router-config.json --update
```

`--check-update` 在加载配置文件前执行。`--update` 会先加载配置，然后：

1. 查 GitHub Releases 拿最新版本；已是最新则直接结束，不做任何改动。
2. 下载对应平台的产物（`amkr_<版本>_<系统>_<架构>[.exe]`）与 `checksums.txt`。
3. **校验 SHA-256 通过后才安装**。校验和不符、或 `checksums.txt` 里没有这个产物时，
   一律拒绝安装——宁可不更新，也不换上来路不明的二进制。
4. 就地替换可执行文件：先把旧的改名为 `<exe>.old`，再把新的放到原位（Windows 允许
   重命名正在运行的 exe，因此无需额外的更新器进程）。
5. 启动一个分离的收尾助手进程（即刚装好的新版本），由它等旧服务让出端口后删除
   `<exe>.old` 并按当前注册状态重启服务。

因为第 5 步由助手完成，`--update` 执行完就可以退出，服务会自行恢复。

第 1、2 步都可能因为**GitHub 直连不可达**而失败（典型报错是
`dial tcp 20.205.243.166:443: connectex: ...`，常见于只封了 `github.com` 而没有封
`api.github.com` 的网络）。此时程序不会硬等重试，而是按**直连 → 镜像加速地址**的顺序
换路：直连永远优先，只有在它失败之后才使用公共加速前缀（`gh-proxy` 形态，把原始地址
原样拼在前缀之后）。这条回退对 `--check-update`、`--update` 与 WebUI 的
`POST /api/update/check`、`POST /ui/update/apply` 都生效。

用 `AMKR_GITHUB_MIRROR` 控制这份前缀列表，取值是逗号分隔的前缀：

```bash
# 未设置：用内置的公共加速前缀兜底（普通用户的默认体验）
amkr --update

# 指向自建反代：完全掌控中转方，推荐在受限网络里长期使用
AMKR_GITHUB_MIRROR=https://mirror.example.com/ amkr --update

# 设为空串：关闭镜像回退，只走直连（例如只允许白名单出口的内网）
AMKR_GITHUB_MIRROR= amkr --update

# 也可以不改代码，直接让请求走代理（Go 的默认传输层认这两个变量）
HTTPS_PROXY=http://127.0.0.1:7890 amkr --update
```

**信任代价**：产物与校验和各自独立地走这条回退，因此只要 GitHub 有一条通，校验和拿到的
就是 GitHub 的原件；两边都只能走镜像时，`checksums.txt` 同样来自中转方，SHA-256 校验就
只剩「防传输损坏」的作用，不再是「防中转方替换」。要在受限网络里长期更新，建议把
`AMKR_GITHUB_MIRROR` 指向自己信任的反代。

公共加速前缀是第三方服务，可能限流、改址或下线；它们只影响"能不能更新"，失败时会退回
上面的更新失败提示。该变量只作用于 `amkr` 自身的版本检查与自更新，安装脚本
（`install.ps1` / `install.sh`）仍直连 GitHub。

**权限限制**：Windows 上若服务以 SYSTEM 计划任务运行，普通用户既查不到该任务
（`schtasks /Query` 会返回「拒绝访问」）也停不掉它。此时 `--update` 仍会完成替换，
但不会启动助手，并明确提示需要以管理员身份执行 `amkr --service restart`，而不会
谎称「服务将自动重启」。

自更新也可从 WebUI 触发：设置页的「检查更新」发现新版本后会显示「立即更新」按钮
（对应的端点是 `POST /ui/update/apply`，需要完整权限）。由服务自身发起的更新会在
替换完成后优雅退出，同样交给收尾助手重启。

## 内部参数

以下参数不会显示在 `--help` 中，主要由后台进程、系统服务和自更新助手调用，不建议手动使用。

| 参数 | 说明 |
| --- | --- |
| `--serve-foreground` | 在当前进程中运行服务（日志追加写入 `log_file_path`） |
| `--update-helper <路径>` | 自更新收尾助手：等待服务停止后清理旧文件并重启服务 |
| `--update-helper-stop` | 配合 `--update-helper`：要求助手主动停掉旧服务（CLI 更新时使用） |

`--update-helper` 不是给人用的入口，它由 `--update` 自己以分离进程方式启动。

## 多操作参数优先级

如果同时传入多个操作参数，程序按以下顺序执行第一个匹配项：

1. `--version` 或 `--help`（由参数解析器直接处理）
2. `--check-update`
3. `--update-helper`（自更新收尾，必须早于配置相关分支）
4. `--switch-model` / `--switch-key`
5. `--show-api-key` / `--get-api-key` / `--get-key`
6. `--show-unified-model`
7. `--update`
8. `--show-address`
9. `--show-config`
10. `--stop`
11. `--status`
12. `--install-service`
13. `--service`
14. `--serve-foreground`
15. 无操作参数时前台启动服务并打开 WebUI
16. `--serve`

例如同时传入 `--show-config --serve` 时，只显示配置，不会启动服务。

> `--restart-service-after-update` 与 `--show-logs` 已按产品决策移除（不再是参数，
> 传入会报「flag provided but not defined」并以退出码 2 结束）。前者的职责由
> `--update-helper` 助手进程承担。

## 退出状态

| 状态码 | 说明 |
| --- | --- |
| `0` | 命令正常完成，或由 argparse 正常显示帮助/版本 |
| `1` | 配置加载失败或统一模型切换失败 |
| `130` | 用户按下 `Ctrl+C` |

