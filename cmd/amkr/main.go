// Command amkr 是 AMKR 的入口：完整 CLI + 前台服务。
//
// 参数解析与分支决策在 cli.go（可脱离副作用测试），本文件负责装配与执行：
// 载入配置、装配 internal/server、监听并在中断时干净退出；以及把 CLI 动作分派给
// internal/service（后台启停、系统服务注册）与 internal/unifiedmodel（统一模型切换）。
//
// # 与参照实现（main.py，202 行）的关系
//
// 24 个 flag 的处置、被砍掉的两个 flag、以及默认动作的开放决策都写在 cli.go 的文件头。
// 本文件只补充执行层的差异：
//
//   - **--check-update 的可手动更新命令**。参照实现给的是 pip/uv 命令（update.py），
//     Go 版给的是本平台的安装脚本一行命令（Windows 是 install.ps1，其余是
//     install.sh）。渲染函数把命令作为参数（见 versionCheckLines 与
//     defaultManualUpdateCommand）。自更新恢复（`--update`）后这条提示仍然成立——
//     重跑安装脚本能装到同一份最新产物；面板文案保持原样，以免无谓地改变用户可见输出。
//   - **--show-config 不含 quick_metrics_items**（要直查 metrics.db 的原始 SQL，
//     internal/metrics 没有等价接口），见 cli.go 的说明。
//
// # 退出码
//
//	0    正常退出（含所有只读子命令）
//	1    失败（配置读不出来、切换失败、启动失败等）
//	2    参数错误（与 argparse 一致）
//	130  Ctrl+C / SIGINT（128 + 2，与参照实现被键盘中断时的退出码一致）
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	amkr "github.com/Sparrived/auto-model-key-router"
	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configservice"
	"github.com/Sparrived/auto-model-key-router/internal/logfiles"
	"github.com/Sparrived/auto-model-key-router/internal/pricing"
	"github.com/Sparrived/auto-model-key-router/internal/selfupdate"
	"github.com/Sparrived/auto-model-key-router/internal/server"
	"github.com/Sparrived/auto-model-key-router/internal/service"
	"github.com/Sparrived/auto-model-key-router/internal/tui"
	"github.com/Sparrived/auto-model-key-router/internal/unifiedmodel"
	"github.com/Sparrived/auto-model-key-router/internal/updatecheck"
)

// version 是本仓库版本号的唯一来源；发布时可用
// `-ldflags "-X main.version=..."` 覆盖（release 工作流就是这么注入 tag 的）。
var version = "6.2.0"

// shutdownTimeout 是优雅关停的上限。
//
// 参照实现交给 uvicorn 的 timeout_graceful_shutdown（service.py 里给了 10 秒），
// 这里取同一个量级：流式响应不会被强杀，但也不会让关停无限期挂着。
const shutdownTimeout = 10 * time.Second

// checkUpdateTimeout 对应 main.py:88 的 `check_latest_version(timeout=10.0)`。
const checkUpdateTimeout = 10 * time.Second

// pricingTimeout 是取回 models.dev 价格目录的上限。
//
// 比版本检查宽松得多：目录有 4.7 MB，实测冷取 4~35 秒（后者是首次 TLS 握手叠加
// 4.7 MB 下载的极端情况）。它只作用于**后台**刷新循环，不在任何请求路径上，因此放宽
// 不会拖慢界面；复验时走条件请求，命中 304 只需约 250 毫秒。
const pricingTimeout = 90 * time.Second

// defaultUsage 是参数错误时打印的简短用法（argparse 的完整用法文本无法复刻，
// 因此只保证退出码一致，见 cli.go 的差异 4）。
const defaultUsage = `用法: amkr [选项]

不带任何选项时：启动服务并自动打开 WebUI（若服务已在运行，则只打开 WebUI）。

常用选项:
  --config PATH            配置文件路径（默认 $AMKR_CONFIG 或用户配置目录）
  --no-open                启动后不自动打开浏览器（无桌面环境时用）
  --serve                  后台启动服务（默认前台启动）
  --stop / --status        停止 / 查看后台服务
  --show-config            展示配置摘要
  --show-address           展示监听地址
  --show-api-key           打印本地授权 Key
  --check-update           检查最新版本
  --update                 检查并自更新到最新版本（自动重启服务）
  --version                打印版本号
  --install-service        注册为系统服务
  --service ACTION         管理系统服务（install/start/stop/restart/status/...）
`

// cliEnv 是执行层的外部依赖接缝：CLI 测试注入它以免真的起服务、连网络或改配置。
type cliEnv struct {
	// out / err 是标准输出与错误输出。
	out io.Writer
	err io.Writer
	// argv0 用于 --version 的程序名（对应 sys.argv[0]）。
	argv0 string
	// clearHistory 对应 main.py:86 的 clear_terminal_history。
	clearHistory func()
	// service 是服务管理接缝（internal/service 的 Env）。
	service *service.Env
	// checkUpdate 对应 update.check_latest_version(timeout=10.0)。
	checkUpdate func() updatecheck.Result
	// manualUpdateCommand 是 --check-update 展示的可手动更新命令。
	manualUpdateCommand string
	// switchUnified 执行统一模型切换（测试注入记录桩）。
	switchUnified func(configPath string, opts *options) (*config.RouterConfig, error)
	// serveForeground 前台启动服务，返回退出码（默认真的监听端口）。
	serveForeground func(configPath string, cfg *config.RouterConfig) int
	// launchWebUI 在服务就绪后用系统默认浏览器打开 WebUI（测试注入记录桩，
	// 免得跑测试时真的弹出浏览器）。
	launchWebUI func(cfg *config.RouterConfig, out io.Writer)
}

// webUIPath 是 WebUI 的挂载路径（internal/webui 的 MountPath 对应物）。
const webUIPath = "/ui"

// openWebUIWhenReady 等服务真正开始监听后再打开浏览器。
//
// 为什么要等：先开浏览器再起服务时，浏览器往往比服务慢，多数情况没问题——但用户点了刷新
// 却发现连不上，就会以为坏了。这里轮询 /health，最多等 10 秒。
//
// 打开失败**不算错误**：可能是无桌面环境（SSH、容器）或没装 xdg-open。此时把地址打印出来
// 让用户手动访问，比直接失败更合理。
func openWebUIWhenReady(cfg *config.RouterConfig, out io.Writer) {
	url := webUIURL(cfg)
	if waitForHealth(cfg, 10*time.Second) {
		if err := tui.OpenURL(url); err != nil {
			fmt.Fprintf(out, "amkr: 无法自动打开浏览器（%v），请手动访问 %s\n", err, url)
			return
		}
		fmt.Fprintf(out, "amkr: 已在浏览器中打开 %s\n", url)
		return
	}
	fmt.Fprintf(out, "amkr: 服务未在预期时间内就绪，请手动访问 %s\n", url)
}

// webUIURL 拼出 WebUI 地址。
//
// host 为 0.0.0.0 / :: 这类「监听全部」的地址时换成 127.0.0.1：浏览器打不开 0.0.0.0。
func webUIURL(cfg *config.RouterConfig) string {
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d%s", host, cfg.Port, webUIPath)
}

// serviceHealthy 报告本地服务是否已经在跑。
//
// 只探测一次、超时 500ms：这是一个"是否已有实例"的快速判定，不是健康检查。
func serviceHealthy(cfg *config.RouterConfig) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port))
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return true
}

// waitForHealth 轮询 /health 直到有响应或超时。
func waitForHealth(cfg *config.RouterConfig, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port))
		if err == nil {
			_ = response.Body.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// defaultManualUpdateCommand 返回当前平台推荐的手动更新命令。
//
// 参照实现给的是 pip/uv 命令；Go 版从 v5.0.0 起的分发方式是「预编译二进制 + 安装
// 脚本」，因此这里让用户重跑安装脚本（脚本自己会取最新 release 并校验 sha256），
// 与 README 的安装一节一致。仍然用 `go install` 的源码用户可以直接照做，只是不再
// 由面板提示——让大多数二进制安装的用户看到一条不需要 Go 工具链的命令更重要。
//
// 自更新（`--update`）恢复后这条命令依然有效，因此没有改掉它：重跑安装脚本能装到同一
// 份最新产物，用的也是 README 安装一节展示的那个脚本。
func defaultManualUpdateCommand() string {
	if runtime.GOOS == "windows" {
		return "irm https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.ps1 | iex"
	}
	return "curl -fsSL https://raw.githubusercontent.com/Sparrived/auto-model-key-router/master/scripts/install.sh | sh"
}

func main() {
	os.Exit(runCLI(os.Args, os.Stdout, os.Stderr, nil))
}

// runCLI 是 main.py 的 main() 等价物，返回进程退出码。
//
// overrides 非 nil 时用于测试注入（nil 表示真实依赖）。
func runCLI(argv []string, stdout, stderr io.Writer, overrides *cliEnv) int {
	ctx := overrides
	if ctx == nil {
		ctx = &cliEnv{}
	}
	if ctx.out == nil {
		ctx.out = stdout
	}
	if ctx.err == nil {
		ctx.err = stderr
	}
	if ctx.argv0 == "" {
		if len(argv) > 0 {
			ctx.argv0 = argv[0]
		} else {
			ctx.argv0 = "amkr"
		}
	}
	if ctx.clearHistory == nil {
		ctx.clearHistory = tui.ClearTerminalHistory
	}
	if ctx.service == nil {
		ctx.service = service.DefaultEnv()
	}
	if ctx.checkUpdate == nil {
		ctx.checkUpdate = func() updatecheck.Result {
			fetch := updatecheck.HTTPFetcher(&http.Client{Timeout: checkUpdateTimeout})
			return updatecheck.CheckLatestVersion(fetch, version, checkUpdateTimeout)
		}
	}
	if ctx.manualUpdateCommand == "" {
		ctx.manualUpdateCommand = defaultManualUpdateCommand()
	}
	if ctx.switchUnified == nil {
		ctx.switchUnified = switchUnified
	}
	if ctx.serveForeground == nil {
		ctx.serveForeground = serveForeground
	}
	if ctx.launchWebUI == nil {
		ctx.launchWebUI = openWebUIWhenReady
	}

	opts, err := parseOptions(argv[1:], ctx.err)
	if err != nil {
		fmt.Fprintf(ctx.err, "%s: %v\n%s", progName(ctx.argv0), err, defaultUsage)
		return 2
	}
	if opts.showVersion {
		fmt.Fprintf(ctx.out, "%s %s\n", progName(ctx.argv0), version)
		return 0
	}
	// main.py:85-86：机器可读的 key 输出不能混入终端控制序列。
	if !opts.showAPIKey {
		ctx.clearHistory()
	}
	terminal := newTerminal(ctx.out)
	command := selectCommand(opts)

	if command == commandCheckUpdate {
		terminal.Print(versionCheckPanel(ctx.checkUpdate(), ctx.manualUpdateCommand))
		return 0
	}
	// 收尾助手在**加载配置之前**处理不了：它要按配置判断服务注册形态。但它也绝不该走
	// 下面那串「写配置 / 加载配置 / 分派」的常规路径（那会再去抢端口）。因此这里单独
	// 前置处理，并把配置路径解析交给它自己。
	if command == commandUpdateHelper {
		return runUpdateHelper(opts.updateHelper, opts.configPath, opts.updateHelperStop, ctx.err)
	}

	resolved, err := config.ResolveConfigPath(opts.configPath)
	if err != nil {
		printErrorPanel(terminal, err, "配置加载失败")
		return 1
	}
	// WebUI/运维开关是持久化设置：三个启动路径（前台/后台/系统服务）都读同一份
	// 配置，写成配置项才能保证 --webui 对后台启动也生效（main.py:95-103）。
	if opts.webui != nil {
		if err := updateConfigFlag(resolved, "webui_enabled", *opts.webui); err != nil {
			printErrorPanel(terminal, err, "配置写入失败")
			return 1
		}
	}
	if opts.ops != nil {
		if err := updateConfigFlag(resolved, "ops_enabled", *opts.ops); err != nil {
			printErrorPanel(terminal, err, "配置写入失败")
			return 1
		}
	}

	loaded, err := config.Load(resolved)
	if err != nil {
		printErrorPanel(terminal, err, "配置加载失败")
		return 1
	}
	loaded = opts.configOverrides(loaded)

	switch command {
	case commandSwitchUnified:
		updated, err := ctx.switchUnified(resolved, opts)
		if err != nil {
			printErrorPanel(terminal, err, "统一模型切换失败")
			return 1
		}
		terminal.Print(unifiedModelPanel(updated, "统一模型已切换", "green"))
		return 0
	case commandShowAPIKey:
		fmt.Fprintln(ctx.out, loaded.LocalAPIKey)
		return 0
	case commandShowUnifiedModel:
		terminal.Print(unifiedModelPanel(loaded, "统一模型", "cyan"))
		return 0
	case commandShowAddress:
		terminal.Print(tui.SectionPanel(routerAddressText(loaded), "AMKR 地址", "cyan"))
		return 0
	case commandShowConfig:
		printConfigSummary(terminal, loaded)
		return 0
	case commandUpdate:
		// --update 需要配置路径（收尾助手要按同一份配置重启服务），因此排在配置解析之后。
		executable, err := os.Executable()
		if err != nil {
			printErrorPanel(terminal, err, "更新失败")
			return 1
		}
		result, err := runSelfUpdate(ctx.checkUpdate, executable, resolved, loaded)
		if err != nil {
			printErrorPanel(terminal, err, "更新失败")
			return 1
		}
		terminal.Print(updatePanel(result))
		return 0
	case commandStop:
		terminal.Print(ctx.service.StopBackground(loaded))
		return 0
	case commandStatus:
		terminal.Print(ctx.service.BackgroundStatusPanel(loaded, resolved))
		return 0
	case commandInstallService, commandManageService:
		action := opts.serviceArgument()
		if action == "status" {
			terminal.Print(ctx.service.ServiceStatusPanel(loaded, resolved))
			return 0
		}
		panel, err := ctx.service.ManageSystemService(resolved, action)
		if err != nil {
			printErrorPanel(terminal, err, "系统服务操作失败")
			return 1
		}
		terminal.Print(panel)
		return 0
	case commandBackground:
		panel, err := ctx.service.StartBackground(resolved, loaded)
		if err != nil {
			printErrorPanel(terminal, err, "后台服务启动失败")
			return 1
		}
		terminal.Print(panel)
		return 0
	default: // commandForeground
		// 无参数默认动作：把服务跑起来，并**打开 WebUI**——终端界面已随 Python 版退役，
		// WebUI 是唯一的界面，用户敲一条 `amkr` 就该看到它。
		//
		// 显式 `--serve-foreground` 不打开浏览器：那是服务注册（Windows 计划任务 /
		// systemd unit）调用的路径，既没有桌面会话，弹浏览器也毫无意义。
		// 服务**已经在跑**（例如已注册为系统服务）时，`amkr` 只负责打开 WebUI：
		// 不再去抢端口——用户敲这条命令要的是界面，不是一次注定失败的绑定。
		if !opts.serveForeground && serviceHealthy(loaded) {
			url := webUIURL(loaded)
			if opts.noOpen {
				fmt.Fprintln(ctx.out, url)
			} else if err := tui.OpenURL(url); err != nil {
				fmt.Fprintf(ctx.out, "amkr: 无法打开浏览器（%v），请手动访问 %s\n", err, url)
			}
			return 0
		}
		if !opts.serveForeground && !opts.noOpen {
			go ctx.launchWebUI(loaded, ctx.out)
		}
		// main.py:108-109：后台启动会带 AMKR_LOG_ARCHIVED=1，避免二次归档。
		if _, _, err := ctx.service.ArchiveLogForForeground(loaded); err != nil {
			fmt.Fprintf(ctx.err, "amkr: 归档日志失败: %v\n", err)
		}
		return ctx.serveForeground(resolved, loaded)
	}
}

// newTerminal 构造一个写到 out 的终端（宽度沿用 COLUMNS，与 tui.Console 同规则）。
func newTerminal(out io.Writer) *tui.Terminal {
	terminal := tui.NewTerminal()
	terminal.SetOutput(out)
	return terminal
}

// updateConfigFlag 走 ConfigService 的读-改-写路径（与 Python 同一把按路径锁）。
func updateConfigFlag(configPath, key string, value bool) error {
	_, err := configservice.New(configPath).Update(func(data *canonical.Value) error {
		data.SetKey(key, canonical.NewBool(value))
		return nil
	})
	return err
}

// switchUnified 对应 main.py:134-142 的统一模型切换。
//
// key 传 "auto" 时用 nil 表示「恢复自动路由」（main.py:140）。
func switchUnified(configPath string, opts *options) (*config.RouterConfig, error) {
	var keyName *string
	if opts.switchKey != nil {
		if *opts.switchKey != "auto" {
			keyName = opts.switchKey
		}
	}
	return unifiedmodel.SwitchUnifiedTarget(
		configPath, opts.unifiedTarget, opts.switchModel, keyName, opts.switchKey != nil)
}

// printErrorPanel 复刻 main.py:126 的错误面板（红色，标题为场景名）。
func printErrorPanel(terminal *tui.Terminal, err error, title string) {
	terminal.Print(tui.SectionPanel(fmt.Sprintf("[red]%s[/red]", err.Error()), title, "red"))
}

// unifiedModelPanel 渲染统一模型摘要面板。
//
// 参照实现用 dashboard.unified_model_status_panel（已按决策 7 砍掉）；这里是等价纯文本。
func unifiedModelPanel(cfg *config.RouterConfig, title, color string) tui.Renderable {
	return tui.SectionPanel(strings.Join(unifiedModelSummaryLines(cfg), "\n"), title, color)
}

// printConfigSummary 是 --show-config 的输出：运行概览 + 公网风险 + 模型配置表。
func printConfigSummary(terminal *tui.Terminal, cfg *config.RouterConfig) {
	healthy := service.DefaultEnv().IsServiceHealthy(cfg.Host, cfg.Port, true)
	width, _ := terminal.Size()
	summary := configSummaryLine(cfg, healthy, width)
	terminal.Print(tui.SectionPanel(summary, "运行概览", "cyan"))
	if cfg.Host == "0.0.0.0" {
		terminal.Print(tui.SectionPanel(publicWarningText, "公网开放风险", "red"))
	}
	rows := configModelRows(cfg)
	cells := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells = append(cells, row)
	}
	terminal.Print(tui.SectionPanel(tui.Table{
		Columns: []tui.TableColumn{{Width: 28}, {Width: 36}, {Width: 10}, {Width: 6}},
		Rows:    cells,
	}, "模型配置", "blue"))
}

// versionCheckPanel 渲染版本检查结果（update.py:557 的 render_version_check_result）。
func versionCheckPanel(result updatecheck.Result, manualCommand string) tui.Renderable {
	lines, style := versionCheckLines(result, manualCommand)
	return tui.SectionPanel(strings.Join(lines, "\n"), "版本检查", style)
}

// versionCheckLines 是版本检查面板的纯行文本。
//
// manualCommand 作为参数：具体命令按平台决定（见 defaultManualUpdateCommand），渲染层
// 只负责排版，不关心命令来自哪里。
func versionCheckLines(result updatecheck.Result, manualCommand string) ([]string, string) {
	if result.Error != nil {
		return []string{
			fmt.Sprintf("当前版本: [bold]%s[/bold]", escapeMarkup(result.CurrentVersion)),
			fmt.Sprintf("检查失败: [red]%s[/red]", escapeMarkup(*result.Error)),
			fmt.Sprintf("GitHub Releases: [bold]%s[/bold]", updatecheck.GitHubReleasesURL),
		}, "red"
	}
	latest := "-"
	if result.LatestVersion != nil && *result.LatestVersion != "" {
		latest = *result.LatestVersion
	}
	lines := []string{
		fmt.Sprintf("当前版本: [bold]%s[/bold]", escapeMarkup(result.CurrentVersion)),
		fmt.Sprintf("最新版本: [bold]%s[/bold]", escapeMarkup(latest)),
	}
	if result.Source != nil && *result.Source != "" {
		lines = append(lines, fmt.Sprintf("检查来源: [bold]%s[/bold]", escapeMarkup(*result.Source)))
	}
	if result.ReleaseURL != nil && *result.ReleaseURL != "" {
		lines = append(lines, fmt.Sprintf("发布页面: [bold]%s[/bold]", escapeMarkup(*result.ReleaseURL)))
	}
	if result.UpdateAvailable() {
		lines = append(lines,
			"状态: [yellow]发现新版本，可手动更新。[/yellow]",
			"手动更新命令:",
			fmt.Sprintf("[bold]%s[/bold]", escapeMarkup(manualCommand)),
		)
		return lines, "yellow"
	}
	lines = append(lines, "状态: [green]当前已是最新版本。[/green]")
	return lines, "green"
}

// escapeMarkup 对应 rich.markup.escape：把值里的方括号转义，避免被当标记吞掉。
func escapeMarkup(value string) string {
	return strings.ReplaceAll(value, "[", `\[`)
}

// checkUpdate 查一次最新版本（服务端自更新用它）。
func checkUpdate() updatecheck.Result {
	fetch := updatecheck.HTTPFetcher(&http.Client{Timeout: checkUpdateTimeout})
	return updatecheck.CheckLatestVersion(fetch, version, checkUpdateTimeout)
}

// serveForeground 写 PID 文件后监听端口（原 main.go 的 run，加上了 PID/日志处理）。
func serveForeground(configPath string, loaded *config.RouterConfig) int {
	env := service.DefaultEnv()
	cleanup, err := env.ForegroundPIDHook(loaded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "amkr: 写 PID 文件失败: %v\n", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// 清掉上次自更新遗留的 <exe>.old：收尾助手删它时旧进程可能还映射着那个映像（更新
	// 自身的就是它），那种情况下删除会失败。此刻持有者已经退出，再试一次必然成功。
	// 尽力而为，失败不影响启动。
	if executable, err := os.Executable(); err == nil {
		selfupdate.CleanStale(executable)
	}

	// 日志落点：参照实现的 uvicorn_log_config 把**所有**日志（应用侧、访问、服务器
	// 自身）写进 config.log_file_path，运维接口 /api/logs 读的就是这个文件。迁移时
	// 这一环漏了，Go 侧只往 stderr 写，于是 log_file_path 恒为空、WebUI 的「服务日志」
	// 面板永远是空的——这正是本次修复的根因。
	//
	// 前台启动**不归档**旧日志：对应的归档动作由调用方在更早处完成
	// （ArchiveLogForForeground，且 AMKR_LOG_ARCHIVED=1 时不重复归档），这里以追加
	// 方式打开即可。
	sink, err := logfiles.Open(loaded.LogFilePath)
	if err != nil {
		// 日志文件打不开不该拦住服务：参照实现里 logging.FileHandler 的失败同样只是
		// 让那一路日志丢失，进程照常启动。
		fmt.Fprintf(os.Stderr, "amkr: 打开日志文件 %s 失败: %v\n", loaded.LogFilePath, err)
		sink = nil
	}
	defer func() {
		if sink != nil {
			_ = sink.Close()
		}
	}()

	// 自更新由服务进程自己发起时（WebUI/API 的「立即更新」），换完文件后由本进程优雅
	// 退出、收尾助手接管重启。因此这里要能"从外部"请求关停，并且让出端口。
	//
	// **为什么服务端反而没有 CLI 那个权限问题**：助手是本服务进程的子进程，继承它的
	// 权限。服务以 SYSTEM 计划任务运行时，助手同样是 SYSTEM，因此 `schtasks` 查得到、
	// 也 Run 得起来——CLI 路径（普通用户）才受"Access is denied"限制。
	shutdownRequested := make(chan struct{})
	options := server.Options{
		ConfigPath:  configPath,
		Config:      loaded,
		Version:     version,
		WebUIAssets: amkr.WebUIAssets,
		// 价格目录（models.dev）走真实网络：只有在这里显式选择，测试才不会被联网
		// 影响（见 server.Options.PricingFetch）。
		PricingFetch: pricing.HTTPFetcher(&http.Client{Timeout: pricingTimeout}),
		// 自更新：StopOld=false（本进程就是被替换的服务，自己退出即可，见 Perform 的
		// 说明），并把自己关停的能力交给它。
		SelfUpdate: func(executable string) (selfupdate.Result, error) {
			return selfupdate.Perform(selfupdate.Options{
				Check:      checkUpdate,
				Executable: executable,
				ConfigPath: configPath,
				StopOld:    false,
				Client:     updateHTTPClient(),
				Spawn:      selfUpdateSpawner(service.DefaultEnv()),
			})
		},
		RequestShutdown: func() {
			// 只关一次：重复 close 会 panic。
			select {
			case <-shutdownRequested:
			default:
				close(shutdownRequested)
			}
		},
	}
	if sink != nil {
		options.Logger = sink.App
		options.AccessLogger = sink.Access
	}
	app, err := server.New(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "amkr: 装配服务失败: %v\n", err)
		return 1
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "amkr: 关停时释放资源失败: %v\n", closeErr)
		}
	}()

	address := net.JoinHostPort(loaded.Host, strconv.Itoa(loaded.Port))
	httpServer := &http.Server{
		Addr:    address,
		Handler: app.Handler(),
		// 不设 ReadTimeout/WriteTimeout：流式响应可能长时间只推少量字节
		// （参照实现的流式语义是「没有总时长上限」，见 internal/runtime/streaming.go）。
		// 各阶段的超时由 proxy/upstream 自己的窗口负责。
	}

	// 信号处理：Ctrl+C（os.Interrupt）与 SIGTERM 都走同一条优雅关停路径。
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 启动行写日志文件，对应 uvicorn.error 的 `Uvicorn running on http://…`。
	//
	// 只在日志不可用**或** stderr 是终端时才同时打印到 stderr，避免同一行落进日志
	// 文件两遍：后台启动路径（internal/service/spawn.go）把子进程的 stdout/stderr
	// 重定向到**同一个**日志文件，若这里无条件打印，日志里就会先是 Go 原样的
	// `amkr 4.1.0 监听 …`、再来一行 Python 格式的同义行。Windows 计划任务路径没有
	// 重定向，无条件打印则纯粹是把这行丢掉。
	startup := fmt.Sprintf("amkr %s 监听 http://%s", version, address)
	if sink != nil {
		sink.Server.Info(startup)
	}
	if sink == nil || isTerminal(os.Stderr) {
		fmt.Fprintln(os.Stderr, startup)
	}

	failures := make(chan error, 1)
	go func() {
		failures <- httpServer.ListenAndServe()
	}()

	// 信号与「自更新请求关停」合并成一个通道：waitForStop 只需要知道"该停了"。
	// shutdownRequested 由 /ui/update/apply 在响应写出后关闭。
	interrupted := make(chan struct{})
	go func() {
		select {
		case <-signals.Done():
		case <-shutdownRequested:
		}
		close(interrupted)
	}()

	return waitForStop(httpServer, failures, interrupted, os.Stderr)
}

// isTerminal 判断 w 是不是字符设备（交互式终端）。
//
// 只用于决定「这行要不要同时给人看」：日志文件已由 sink 保证，控制台输出是锦上
// 添花，判错了也不影响日志内容的正确性。
func isTerminal(w *os.File) bool {
	info, err := w.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// waitForStop 等待「监听失败」与「收到中断」两件事之一，返回进程退出码。
//
// 单独抽出来是为了让 130 这条路径可测：测试进程里无法真的给自己发 Ctrl+C
// （Go 在 Windows 上不支持 os.Process.Signal(os.Interrupt)），但把「中断通道已关闭」
// 作为输入就能逐条断言三个退出码。
func waitForStop(httpServer *http.Server, failures <-chan error, interrupted <-chan struct{}, out io.Writer) int {
	select {
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(out, "amkr: 监听 %s 失败: %v\n", httpServer.Addr, err)
			return 1
		}
		return 0
	case <-interrupted:
		fmt.Fprintln(out, "amkr: 收到中断信号，正在关停……")
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			// 超时不代表启动失败：强制关闭是最后手段，但仍按中断语义返回 130。
			fmt.Fprintf(out, "amkr: 优雅关停超时: %v\n", err)
			_ = httpServer.Close()
		}
		return 130
	}
}
