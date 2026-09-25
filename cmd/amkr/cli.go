package main

// 本文件移植 auto_model_key_router/main.py（202 行）的**参数解析与分支决策**。
//
// 与 main.go（进程装配与监听）分开是为了让「选哪条分支、配置被改成了什么」可以脱离
// 副作用单独断言：parseOptions / selectCommand 都是纯函数，可直接喂参数断言。
//
// # 24 个 flag 的处置
//
// 移植（21 个声明，含三个别名与两个成对开关）：
//
//	--config --host --port --show-config --show-address --show-api-key
//	--get-api-key --get-key --switch-model --switch-key --unified-target
//	--show-unified-model --version --check-update --update --serve --webui
//	--no-webui --no-ops --serve-foreground --update-helper --stop --status
//	--install-service --service
//
// 新增（自更新，见 --update 的说明）：
//
//	--update --update-helper
//
// **按产品决策砍掉**（不再定义，解析时报「flag provided but not defined」并返回 2）：
//
//	--show-logs                 只驱动 logs_tui.py（决策 7 砍掉）
//	--restart-service-after-update  update.py:613 的自更新收尾；Go 版改由
//	                            --update-helper 助手进程自动重启，不再需要这个开关
//
// 这两条现在没有任何测试守着——它们随回放语料一并删除了。
//
// 注意 `--update` 曾经也是被砍的一员（产品决策 8「取消自更新」）。该决策已被**推翻**：
// 现在恢复 `--update`，并补上 WebUI/API 入口。恢复的理由与实现见 docs/CLI.md 的
// 「版本检查与更新」一节。
//
// # 刻意保留的差异
//
//  1. **--config 的默认值与优先级**。参照实现的 `--config` 默认是一个**具体路径**
//     （`str(DEFAULT_CONFIG_PATH)`），于是 `RouterConfig.load` 里的 AMKR_CONFIG 分支
//     永远走不到；Go 侧默认留空再交给 config.ResolveConfigPath，因此是
//     「显式路径 > $AMKR_CONFIG > 默认路径」（与任务要求的 CLI > 环境变量 > 配置文件
//     一致）。该优先级由 internal/config 的 TestResolveConfigPathPrecedence 断言。
//  2. **默认（无参数）动作**。参照实现进入 dashboard.py 的 run_terminal_ui；该模块已
//     随 Python 版退役，Go 侧的默认动作由 defaultCommand 一个常量决定——见那里的说明
//     （这是一个**待产品确认**的开放决策）。
//  3. **没有 --print-config**。参照实现用 --show-config；Go 保持同名，行为也一致，
//     只是不再经过 dashboard 的富文本渲染。
//  4. **错误文案**。argparse 的用法/错误文本与 Go 的 flag 包不同，因此只保证**退出码**
//     一致（两边都是 2）。

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
	"github.com/Sparrived/auto-model-key-router/internal/formatting"
	"github.com/Sparrived/auto-model-key-router/internal/tui"
)

// serviceChoices 对应 main.py:81 的 `--service` choices（12 个）。
var serviceChoices = []string{
	"install", "install-user", "uninstall", "start", "stop", "restart", "status",
	"install-elevated", "uninstall-elevated", "start-elevated", "stop-elevated", "restart-elevated",
}

// defaultUnifiedTarget 对应 main.py:49 的 `--unified-target` 默认值。
const defaultUnifiedTarget = "default.primary"

// command 是 main.py 里那条 if/elif 链最终选中的动作。
type command int

const (
	// commandForeground 是**默认**动作（无参数）：前台启动服务。
	//
	// 参照实现在这里进 dashboard.run_terminal_ui；dashboard.py 按决策 7 砍掉后，
	// Go 侧最保守、最可逆的默认是「照 --serve-foreground 那样前台把服务跑起来」：
	// 它不新增任何界面、不产生后台残留，与既有 cmd/amkr 的行为一致。
	//
	// 这是一个**待确认的产品决策**，备选方案：
	//   a) 打印用法（--help）并退出 2——最保守，但把「直接跑起来」的能力弄丢了；
	//   b) 打印配置摘要（--show-config 的内容）——只读，但没启动服务；
	//   c) 前台启动服务（当前实现）——WebUI 成为唯一界面，与决策 7 的动机一致。
	commandForeground command = iota
	// commandBackground 对应 `--serve`：后台启动。
	commandBackground
	// commandCheckUpdate 对应 `--check-update`（在配置加载之前，main.py:87-89）。
	commandCheckUpdate
	// commandUpdate 对应 `--update`：检查并自更新到最新版本。
	commandUpdate
	// commandUpdateHelper 是 `--update-helper`（隐藏）：自更新的收尾助手进程。
	commandUpdateHelper
	// commandShowAddress 对应 `--show-address`。
	commandShowAddress
	// commandShowAPIKey 对应 `--show-api-key`（含两个别名）。
	commandShowAPIKey
	// commandShowUnifiedModel 对应 `--show-unified-model`。
	commandShowUnifiedModel
	// commandSwitchUnified 对应 `--switch-model` / `--switch-key`。
	commandSwitchUnified
	// commandShowConfig 对应 `--show-config`。
	commandShowConfig
	// commandStop 对应 `--stop`。
	commandStop
	// commandStatus 对应 `--status`。
	commandStatus
	// commandInstallService 对应 `--install-service`。
	commandInstallService
	// commandManageService 对应 `--service <action>`。
	commandManageService
)

// String 返回动作短名，便于失败信息可读。
func (c command) String() string {
	switch c {
	case commandForeground:
		return "foreground"
	case commandBackground:
		return "background-start"
	case commandCheckUpdate:
		return "check-update"
	case commandUpdate:
		return "update"
	case commandUpdateHelper:
		return "update-helper"
	case commandShowAddress:
		return "show-address"
	case commandShowAPIKey:
		return "show-api-key"
	case commandShowUnifiedModel:
		return "show-unified-model"
	case commandSwitchUnified:
		return "switch-unified"
	case commandShowConfig:
		return "show-config"
	case commandStop:
		return "stop"
	case commandStatus:
		return "background-status"
	case commandInstallService:
		return "install-service"
	case commandManageService:
		return "system-service"
	}
	return "unknown"
}

// options 是解析后的参数集合，字段与 main.py 的 argparse 命名空间一一对应。
type options struct {
	configPath string
	// noOpen 关闭「无参数启动后自动打开 WebUI」。
	noOpen bool
	host   string
	port   int
	// hostSet / portSet 对应 Python 的 `if args.host:` / `if args.port:`：**假值不覆盖**
	// （因此 `--port 0` 与 `--host ""` 都不会生效，main.py:129-132）。
	hostSet bool
	portSet bool

	showConfig       bool
	showAddress      bool
	showAPIKey       bool
	switchModel      *string
	switchKey        *string
	unifiedTarget    string
	showUnifiedModel bool
	showVersion      bool
	checkUpdate      bool
	// update 对应 `--update`：检查并自更新。
	update bool
	// updateHelper 对应 `--update-helper`（隐藏）：它是 `--update` 换完文件后启动的
	// 收尾进程，**不是**给用户用的入口。取值是被挪开的旧二进制路径。
	updateHelper    string
	updateHelperSet bool
	// updateHelperStop 对应 `--update-helper-stop`（隐藏）：告诉助手旧服务需要它主动
	// 停掉。仅 CLI 触发的更新带上（见 internal/selfupdate.HelperStopFlag）。
	updateHelperStop bool
	serve            bool
	webui            *bool
	ops              *bool
	serveForeground  bool
	stop             bool
	status           bool
	installService   bool
	serviceAction    string
	serviceSet       bool
}

// triFlag 实现「同名 dest 的两个开关，后出现的生效」（argparse 的 store_true /
// store_false 共享 dest 时的语义）。
//
// IsBoolFlag 让 `--webui` 不需要显式值；`--no-webui=false` 这种显式赋值在 argparse
// 里是错误（store_false 不收值），Go 侧接受——这是刻意的宽松差异，不影响正向语义。
type triFlag struct {
	target **bool
	value  bool
}

func (f triFlag) String() string { return "" }

func (f triFlag) Set(text string) error {
	value := f.value
	if text == "false" {
		value = false
	}
	*f.target = &value
	return nil
}

func (f triFlag) IsBoolFlag() bool { return true }

// parseOptions 解析参数；解析失败返回错误（调用方按 argparse 的惯例返回 2）。
//
// 传入 argv 时不带程序名（与 Go 的 flag 包一致）；usage 写到 errOut。
func parseOptions(argv []string, errOut io.Writer) (*options, error) {
	opts := &options{unifiedTarget: defaultUnifiedTarget}
	flags := flag.NewFlagSet("amkr", flag.ContinueOnError)
	flags.SetOutput(errOut)
	// 关闭 Go 自带的 usage 打印，错误文案由调用方统一给出（argparse 的文本无法复刻）。
	flags.Usage = func() {}

	flags.StringVar(&opts.configPath, "config", "",
		"配置文件路径；留空则依次尝试 $AMKR_CONFIG 与默认用户配置目录")
	flags.StringVar(&opts.host, "host", "", "覆盖配置中的监听地址")
	flags.IntVar(&opts.port, "port", 0, "覆盖配置中的监听端口")
	flags.BoolVar(&opts.showConfig, "show-config", false, "只展示配置摘要，不启动服务")
	flags.BoolVar(&opts.showAddress, "show-address", false, "查询 AMKR 的监听 IP、端口和服务地址")
	flags.BoolVar(&opts.showAPIKey, "show-api-key", false, "获取当前 AMKR 的本地授权 Key")
	flags.BoolVar(&opts.showAPIKey, "get-api-key", false, "show-api-key 的别名")
	flags.BoolVar(&opts.showAPIKey, "get-key", false, "show-api-key 的别名")
	flags.Func("switch-model", "切换统一模型指向的已有模型或别名", func(value string) error {
		opts.switchModel = &value
		return nil
	})
	flags.Func("switch-key", "切换统一模型使用的已有 key；传 auto 恢复自动路由", func(value string) error {
		opts.switchKey = &value
		return nil
	})
	flags.StringVar(&opts.unifiedTarget, "unified-target", defaultUnifiedTarget, "选择要修改的 unified 路由目标")
	flags.BoolVar(&opts.showUnifiedModel, "show-unified-model", false, "查看统一模型当前指向")
	flags.BoolVar(&opts.showVersion, "version", false, "打印版本号后退出")
	flags.BoolVar(&opts.checkUpdate, "check-update", false, "通过 GitHub Releases 检查最新版本")
	flags.BoolVar(&opts.update, "update", false, "检查并自更新到最新版本")
	flags.Func("update-helper", "自更新收尾助手（内部参数）：清理旧文件并按注册状态重启服务", func(value string) error {
		opts.updateHelper = value
		opts.updateHelperSet = true
		return nil
	})
	flags.BoolVar(&opts.updateHelperStop, "update-helper-stop", false,
		"自更新收尾助手（内部参数）：旧服务需要助手主动停掉")
	flags.BoolVar(&opts.serve, "serve", false, "后台启动服务，不占用当前终端")
	flags.Var(triFlag{target: &opts.webui, value: true}, "webui", "启用内置 WebUI")
	flags.Var(triFlag{target: &opts.webui, value: false}, "no-webui", "关闭 WebUI")
	flags.Var(triFlag{target: &opts.ops, value: false}, "no-ops", "关闭运维接口")
	flags.BoolVar(&opts.serveForeground, "serve-foreground", false, "前台启动服务（内部参数）")
	flags.BoolVar(&opts.noOpen, "no-open", false, "启动后不自动打开浏览器")
	flags.BoolVar(&opts.stop, "stop", false, "停止后台服务")
	flags.BoolVar(&opts.status, "status", false, "查看后台服务状态")
	flags.BoolVar(&opts.installService, "install-service", false, "注册为 Windows/Linux 内置服务")
	flags.Func("service", "管理 Windows/Linux 内置服务", func(value string) error {
		opts.serviceAction = value
		opts.serviceSet = true
		return nil
	})

	if err := flags.Parse(argv); err != nil {
		return nil, err
	}
	if flags.NArg() > 0 {
		return nil, fmt.Errorf("不支持的位置参数: %s", strings.Join(flags.Args(), " "))
	}
	// 记录是否显式给了 --host/--port（对应 Python 的假值判定）。
	flags.Visit(func(visited *flag.Flag) {
		switch visited.Name {
		case "host":
			opts.hostSet = true
		case "port":
			opts.portSet = true
		}
	})
	if opts.serviceSet && !containsString(serviceChoices, opts.serviceAction) {
		return nil, fmt.Errorf("--service 的取值无效: %s", opts.serviceAction)
	}
	if !containsString(configops.UnifiedTargets, opts.unifiedTarget) {
		return nil, fmt.Errorf("--unified-target 的取值无效: %s", opts.unifiedTarget)
	}
	return opts, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// selectCommand 对应 main.py:87-195 的 if/elif 链，顺序必须逐条一致。
//
// --webui/--no-webui/--no-ops 是**前置写配置**，不参与这条链，见 applyFlagsToConfig。
func selectCommand(opts *options) command {
	switch {
	case opts.checkUpdate:
		return commandCheckUpdate
	case opts.updateHelperSet:
		// 内部助手：它由 `--update` 启动，绝不与其它动作组合使用，因此放在最前面判定，
		// 免得被某个「也有副作用」的分支抢先。
		return commandUpdateHelper
	case opts.switchModel != nil || opts.switchKey != nil:
		return commandSwitchUnified
	case opts.showAPIKey:
		return commandShowAPIKey
	case opts.showUnifiedModel:
		return commandShowUnifiedModel
	case opts.update:
		// 对应 docs/CLI.md 优先级表第 6 条：在 --show-unified-model 之后、
		// --show-address 之前。
		return commandUpdate
	case opts.showAddress:
		return commandShowAddress
	case opts.showConfig:
		return commandShowConfig
	case opts.stop:
		return commandStop
	case opts.status:
		return commandStatus
	case opts.installService:
		return commandInstallService
	case opts.serviceSet:
		return commandManageService
	case opts.serveForeground:
		return commandForeground
	case !opts.serve:
		// main.py:192-193：无 --serve 时进 run_terminal_ui（dashboard，已砍）。
		return defaultCommand()
	default:
		return commandBackground
	}
}

// defaultCommand 是**无参数默认动作**的唯一开关点。
//
// 开放决策（见文件头差异 2）：参照实现进 dashboard TUI，Go 侧改为前台启动服务。
// 换默认动作只需要改这一个常量。
func defaultCommand() command { return commandForeground }

// serviceArgument 返回 --service 动作；--install-service 等价于 install（main.py:183）。
func (o *options) serviceArgument() string {
	if o.installService {
		return "install"
	}
	return o.serviceAction
}

// configOverrides 复刻 main.py:129-132 的 host/port 覆盖（含「假值不覆盖」的怪癖）。
func (o *options) configOverrides(cfg *config.RouterConfig) *config.RouterConfig {
	if !o.hostSet && !o.portSet {
		return cfg
	}
	updated := *cfg
	if o.host != "" {
		updated.Host = o.host
	}
	if o.port != 0 {
		updated.Port = o.port
	}
	return &updated
}

// —— 纯渲染（可脱离副作用比较的部分） ——

// routerAddressText 对应 main.py:20 的 router_address_text。
//
// 与 Python 一致：host 里含冒号且未加方括号时补方括号（IPv6 字面量）。
func routerAddressText(cfg *config.RouterConfig) string {
	urlHost := cfg.Host
	if strings.Contains(urlHost, ":") && !strings.HasPrefix(urlHost, "[") {
		urlHost = "[" + urlHost + "]"
	}
	return fmt.Sprintf("监听 IP: [bold]%s[/bold]\n监听端口: [bold]%d[/bold]\n服务地址: [bold]http://%s:%d[/bold]",
		cfg.Host, cfg.Port, urlHost, cfg.Port)
}

// configSummaryItems 对应 dashboard.config_renderables 里的「运行概览」条目
// （dashboard.py:886-896）。
//
// 与参照实现的差异：**不含 quick_metrics_items**（dashboard.py:832-862 的请求/成功率/
// Token/RPM/TPM）。那段要直查 metrics.db 的原始 SQL，而 internal/metrics 没有暴露等价
// 查询（只暴露按时间窗聚合的 Snapshot），复刻它必须改 internal/metrics——超出本任务的
// 允许范围。WebUI 与 /api/metrics 已覆盖同一信息。
//
// healthy 作为参数传入：它需要探测 /health（副作用）。
//
// 原先还有一个 visitorInstalled 参数与「访客 Key / 访客可用」两行 —— 随访客模式一并
// 删除，改报**访问密钥**的数量（那是现在唯一分发给外部使用者的受限凭据）。
func configSummaryItems(cfg *config.RouterConfig, healthy bool) []string {
	modelCount := len(cfg.Models)
	keyCount := 0
	upstreams := map[string]struct{}{}
	for _, model := range cfg.Models {
		keyCount += len(model.Keys)
		for _, key := range model.Keys {
			if key.BaseURL != "" {
				upstreams[key.BaseURL] = struct{}{}
			}
		}
	}
	serviceStatus := "[yellow]○[/yellow]"
	if healthy {
		serviceStatus = "[green]●[/green]"
	}
	authStatus := "[yellow]✗[/yellow]"
	if cfg.LocalAPIKey != "" {
		authStatus = "[green]✓[/green]"
	}
	items := []string{
		fmt.Sprintf("[dim]监听[/dim] [bold]%s:%d[/bold]", cfg.Host, cfg.Port),
		fmt.Sprintf("[dim]服务[/dim] %s", serviceStatus),
		fmt.Sprintf("[dim]鉴权[/dim] %s", authStatus),
		fmt.Sprintf("[dim]模型[/dim] [bold cyan]%d[/bold cyan]", modelCount),
		fmt.Sprintf("[dim]Key[/dim] [bold green]%d[/bold green]", keyCount),
		fmt.Sprintf("[dim]上游[/dim] [bold magenta]%d[/bold magenta]", len(upstreams)),
	}
	if len(cfg.AccessKeys) > 0 {
		enabled := 0
		for _, accessKey := range cfg.AccessKeys {
			if accessKey.Enabled {
				enabled++
			}
		}
		items = append(items,
			fmt.Sprintf("[dim]访问密钥[/dim] [bold green]%d[/bold green]", enabled),
		)
	}
	return items
}

// configSummaryLine 复刻「运行概览」那行文本的渲染（dashboard.py:898-902）。
//
// Python 用 Text(no_wrap=True, overflow="ellipsis") 把带标记的条目拼成一行；Go 侧
// 按既定策略降级为纯文本（去掉标记）后套用同样的 no_wrap+ellipsis 版式。
func configSummaryLine(cfg *config.RouterConfig, healthy bool, width int) string {
	plain := tui.StripMarkup(strings.Join(configSummaryItems(cfg, healthy), "  "))
	lines := tui.Text{Value: plain, NoWrap: true, OverflowEllipsis: true}.RenderLines(width)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// configModelRows 对应 dashboard.py:912-944 的「模型配置」表行。
//
// 只产出**行数据**：Go 的表格用固定列宽，与 rich 的 expand 列宽分配不同（internal/tui
// 的既定差异），因此只保证单元格内容一致，版式由渲染层自行决定。
//
// 与参照实现的差异：隐藏别名列已随该概念一并移除（可调用名只有模型 ID 与 aliases）。
func configModelRows(cfg *config.RouterConfig) [][]string {
	rows := [][]string{}
	for _, model := range cfg.Models {
		displayNames := "-"
		if len(model.Aliases) > 0 {
			displayNames = strings.Join(model.Aliases, "\n")
		}
		rows = append(rows, []string{
			formatting.ShortText(model.ID, 28),
			formatting.ShortText(displayNames, 36),
			routingModeText(model.RoutingMode),
			fmt.Sprintf("%d", len(model.Keys)),
		})
	}
	if len(cfg.Models) == 0 {
		rows = append(rows, []string{"未配置", "-", "-", "0"})
	}
	return rows
}

// routingModeText 对应 dashboard.py:931-935 的路由模式文案。
func routingModeText(mode string) string {
	switch mode {
	case "round_robin":
		return "分流"
	case "priority":
		return "优先级"
	case "only_first":
		return "仅首个"
	default:
		return "分流"
	}
}

// publicWarningText 对应 dashboard.py:905 的「公网开放风险」面板内容。
const publicWarningText = "[bold red]⚠ 当前监听地址为 0.0.0.0，服务会接受所有可达网络的连接。[/bold red]\n" +
	"[red]如果机器暴露在公网或未受信任网络中，请务必启用本地鉴权、限制防火墙访问，并避免泄露上游 API Key。[/red]"

// —— --switch-model / --switch-key 与 --show-unified-model 的纯文本摘要 ——
//
// 参照实现用 dashboard.unified_model_status_panel（dashboard.py:283-318）渲染；
// 该模块按决策 7 砍掉，这里用**等价纯文本**（迁移方案的既定降级策略）保留同名信息：
// 请求模型、目标模型、使用 Key，以及熔断/图像/嵌入三个附加计划。

// unifiedModelSummaryLines 产出统一模型摘要的纯文本行。
func unifiedModelSummaryLines(cfg *config.RouterConfig) []string {
	if cfg.UnifiedModel == nil {
		return []string{
			fmt.Sprintf("[yellow]尚未配置 %s。[/yellow]", unifiedModelID),
			"请选择已有模型和 Key。",
		}
	}
	plan := cfg.UnifiedModel
	lines := []string{
		fmt.Sprintf("请求模型: [bold cyan]%s[/bold cyan]", unifiedModelID),
		fmt.Sprintf("目标模型: [bold]%s[/bold]", plan.Default.Primary.Model),
		fmt.Sprintf("使用 Key: [bold green]%s[/bold green]", orAutoRoute(plan.Default.Primary.Key)),
	}
	if plan.Default.Fallback != nil {
		lines = append(lines,
			fmt.Sprintf("熔断模型: [bold]%s[/bold]", plan.Default.Fallback.Model),
			fmt.Sprintf("熔断 Key: [bold green]%s[/bold green]", orAutoRoute(plan.Default.Fallback.Key)),
		)
	}
	for _, entry := range []struct {
		label string
		plan  *config.RoutePlan
	}{
		{"图像", plan.Image},
		{"嵌入", plan.Embeddings},
	} {
		if entry.plan == nil {
			continue
		}
		lines = append(lines,
			fmt.Sprintf("%s模型: [bold]%s[/bold]", entry.label, entry.plan.Primary.Model),
			fmt.Sprintf("%s Key: [bold green]%s[/bold green]", entry.label, orAutoRoute(entry.plan.Primary.Key)),
		)
	}
	return lines
}

// unifiedModelID 对应 config.UNIFIED_MODEL_ID（config.py 的 "unified-model"）。
const unifiedModelID = "unified-model"

// orAutoRoute 复刻 `plan.key or "自动路由"`。
func orAutoRoute(key string) string {
	if key == "" {
		return "自动路由"
	}
	return key
}

// progName 复刻 `Path(sys.argv[0]).stem`（main.py:33）。
func progName(argv0 string) string {
	base := filepath.Base(argv0)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
