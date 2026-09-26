package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 订阅账号资源：把「按订阅计费」的供应商（OpenCode Go、Command Code…）的剩余额度也搬到
// 账号资源看板上。
//
// 与 CPA 实例（handlers_cpa.go）的关键差异是**条目从哪来**：
//
//   - CPA 是一台独立的机器，只能靠人填地址与管理密钥，因此有 cpa_instances 这个键；
//   - 订阅是**供应商自己的一个端点**，凭据就是那把已经在配置里的 api_key。再让人手填
//     一遍地址与 key 等于把同一件事存两份，两份还会走散（改了供应商的 key，看板还在用
//     旧的）。所以订阅条目一律**从已配置的供应商派生**：认出端点是哪一家 → 拿它的 key
//     去问一次用量；供应商被删掉，条目自然就没了，不需要任何清理钩子。
//
// 由此带来两条必须写清楚的边界：
//
//  1. **不新增配置键**。派生出来的东西没有独立生命周期，也就不需要版本号、编辑器与
//     整体替换接口。看板是这份配置的一个只读投影。
//  2. **认不出就不猜**。只有 base_url 明确落在已知厂商的订阅端点上才派生条目
//     （见 opencodeGoEndpoint / commandCodeEndpoint）。同一个主机的其它端点（例如
//     OpenCode Zen 的 /zen/v1）不出条目，而不是拿它的 key 去试 —— 试出来的 401 会
//     被读成「凭据坏了」，而真相是「这不是那个端点」。
//
// 其余口径与 CPA 一致：只读、不缓存、不轮询、失败只影响自己那一条、读数**只看不拦**
// （不参与 AMKR 的 key 冷却与故障转移）。

const (
	// subscriptionResponseLimit 是单个订阅端点单次响应的读取上限。
	//
	// 这些都是用量读数（几个窗口、一堆聚合计数），比 CPA 的 auth-files 小得多，
	// 1 MiB 已经是宽松值；上限只是别让对端把我们读爆。
	subscriptionResponseLimit = 1 << 20
	// subscriptionConcurrency 是并发问订阅的上限：与 CPA 那一层同一个量级，
	// 条目数通常是个位数，限流是为了别把同一家上游的用量端点打满。
	subscriptionConcurrency = 4
	// opencodeGoAttempts 是 OpenCode Go 用量端点的尝试次数（含首次）。
	//
	// 该端点会无规律地回 `503 {"message":"Go usage is unavailable"}`——与 UA、凭据
	// 都无关，重试即可。不重试的话看板会经常性地显示「读取失败」，而那并不是用户的
	// 订阅出了问题。次数取 4：连续四次都赶上故障的概率已经足够低。
	opencodeGoAttempts = 4
	// subscriptionUserAgent 是调用订阅端点时表明身份的 UA。
	//
	// 不伪装成别的客户端：OpenCode 明确要求调用方用自己的名字标识自己，而
	// Command Code 的 /alpha/* 是非公开路由，出问题时对方至少要能从日志里认出是谁在问。
	subscriptionUserAgent = "amkr-account-resources/1.0"
)

// subscriptionRetryBackoff 是重试之间的等待：250ms、500ms、1s。
//
// 总等待 1.75s，相对 60s 的扇出预算可以忽略；再长就该让用户看到「这次没读到」而不是
// 让页面一直转。
var subscriptionRetryBackoff = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}

// subscriptionSleep 是重试之间的实际等待，抽成变量只为让用例不必真的睡够 1.75 秒。
var subscriptionSleep = time.Sleep

// subscriptionHTTPClient 是访问订阅端点的客户端。
//
// 与 cpaHTTPClient 分开：CPA 可能在 127.0.0.1，而订阅端点一定在公网，两者对重定向、
// 代理的期望不同，共用会把手改一处的效果带到另一处。超时同样交给上层 context。
var subscriptionHTTPClient = &http.Client{Timeout: cpaAccountsBudget}

// subscriptionEntry 是一个订阅在看板上的全部读数。
//
// windows / summary / signals 直接复用 CPA 那套类型（cpaQuotaWindow / cpaQuotaMetric）：
// 界面上「一个窗口 = 一个环」的渲染因此只有一份实现，订阅与 CPA 账号不会出现两套
// 百分比口径。provider_id / key_name 指回配置里的出处，看板据此回答「这是谁家的哪个 key」。
type subscriptionEntry struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Vendor     string            `json:"vendor"`
	ProviderID string            `json:"provider_id"`
	KeyName    string            `json:"key_name"`
	BaseURL    string            `json:"base_url"`
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	ObservedAt string            `json:"observed_at,omitempty"`
	Plan       string            `json:"plan,omitempty"`
	TierID     string            `json:"tier_id,omitempty"`
	Account    string            `json:"account,omitempty"`
	Status     string            `json:"status,omitempty"`
	Windows    []cpaQuotaWindow  `json:"windows"`
	Summary    []cpaQuotaMetric  `json:"summary,omitempty"`
	Signals    map[string]string `json:"signals,omitempty"`
}

// subscriptionReading 是某个厂商的采集器返回的归一化结果。
//
// 采集器只负责「把这一家的响应讲成同一套话」，不认识 HTTP 之外的东西：条目的出处、ID、
// 错误归属都由调用方补。这样加一家新厂商 = 写一个 Collect + 在 registry 里登记一行。
type subscriptionReading struct {
	Plan    string
	TierID  string
	Account string
	Status  string
	Windows []cpaQuotaWindow
	Summary []cpaQuotaMetric
	Signals map[string]string
}

// subscriptionTarget 是一条待采集的订阅：从供应商配置派生出来的「端点 + 凭据」。
type subscriptionTarget struct {
	ID         string
	Vendor     subscriptionVendor
	ProviderID string
	KeyName    string
	Endpoint   subscriptionEndpoint
	APIKey     string
}

// subscriptionVendor 是「一家订阅厂商」的登记项。
//
// 加一家厂商要做的事只有两件：写一个 Collect，在这里加一行。Match 与 Collect 分开是因为
// 端点识别必须能不付出网络代价地先做一遍——看板要在**不发任何请求**的前提下就知道
// 哪些供应商会产生订阅条目。
type subscriptionVendor struct {
	Kind string
	// Label 是给人看的厂商名。
	Label string
	// Match 判断一个已配置供应商的 base_url 是不是这一家的订阅端点。
	Match func(endpoint subscriptionEndpoint) bool
	// Collect 拿端点与凭据问一次用量。
	Collect func(ctx context.Context, target subscriptionTarget) (subscriptionReading, error)
}

// subscriptionVendors 是厂商登记表。顺序即看板上的分组顺序。
//
// 只登记**有据可查的用量端点**：
//   - OpenCode Go —— 官方文档里的 /zen/go/v1/usage（opencode 仓库 packages/console 内）；
//   - Command Code —— /alpha/billing/* 等非公开路由（见 collectors 里的出处注释）。
//
// OpenCode Zen（预付余额）刻意不在表里：它没有按 key 查询用量的接口，只有需要控制台
// 会话 Cookie + workspaceId 的网页接口，而 AMKR 里存不下也不该存那种凭据。
var subscriptionVendors = []subscriptionVendor{
	{
		Kind:    "opencode-go",
		Label:   "OpenCode Go",
		Match:   opencodeGoEndpoint,
		Collect: collectOpenCodeGo,
	},
	{
		Kind:    "commandcode",
		Label:   "Command Code",
		Match:   commandCodeEndpoint,
		Collect: collectCommandCode,
	},
}

// subscriptionEndpoint 是一个供应商 base_url 拆出来的三样东西。
//
// baseURL 用于直接拼用量路径（OpenCode Go 的用量就挂在它的推理端点旁边）；origin 用于
// 那些挂在**主机根**下的路由（Command Code 的 /alpha/* —— 供应商 base_url 里可能带着
// /provider 之类的推理路径，拼上去就错了）。
type subscriptionEndpoint struct {
	baseURL string
	origin  string
	host    string
	path    string
}

// parseSubscriptionEndpoint 拆解 base_url。宽度不够（没有 scheme/host）时返回错误，
// 由调用方把它算作「这个供应商不产生订阅条目」。
func parseSubscriptionEndpoint(baseURL string) (subscriptionEndpoint, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return subscriptionEndpoint{}, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return subscriptionEndpoint{}, fmt.Errorf("base_url 缺少 scheme 或主机: %s", baseURL)
	}
	return subscriptionEndpoint{
		baseURL: trimmed,
		// 主机统一小写（保留端口）：同一台主机的两种写法必须匹配到同一家厂商。
		origin: parsed.Scheme + "://" + strings.ToLower(parsed.Host),
		// 匹配一律用不带端口的小写主机名：配置里写 https://opencode.ai 与
		// https://opencode.ai:443 是同一个端点。
		host: strings.ToLower(parsed.Hostname()),
		path: strings.TrimRight(parsed.EscapedPath(), "/"),
	}, nil
}

// opencodeGoEndpoint 认出 OpenCode Go 的推理端点（`https://opencode.ai/zen/go/v1`）。
//
// 必须连路径一起看：同一个主机下 /zen/v1 是 Zen（预付余额，没有按 key 的用量接口），
// 把它的 key 拿去问 Go 的用量只会得到 403「OpenCode Go subscription required」，
// 而那会被读成「订阅过期了」——一个凭据问题被显示成订阅问题，比不显示更糟。
func opencodeGoEndpoint(endpoint subscriptionEndpoint) bool {
	return endpoint.host == "opencode.ai" && strings.HasPrefix(endpoint.path, "/zen/go")
}

// commandCodeEndpoint 认出 Command Code 的 Provider API 端点。
//
// 只看主机不看路径：它的用量路由挂在 /alpha/* 下（主机根），而推理端点的路径在文档里
// 是兼容 OpenAI/Anthropic 的那种（可能带 /provider、/v1），拿路径当判据会把合法配置
// 挡在外面。
func commandCodeEndpoint(endpoint subscriptionEndpoint) bool {
	return endpoint.host == "api.commandcode.ai"
}

// subscriptionTargets 从供应商配置派生出待采集的订阅条目。
//
// **每个启用的 key 一条**：订阅是绑在凭据上的（同一家订阅可能有两把 key，各自是独立的
// 额度池），把同供应商的多个 key 合成一条就必须挑一把来问，那等于替用户丢数据。
//
// 停用的 key 不出条目：它在 AMKR 里已经不参与调度，替它去看用量只会让看板列出一批
// 「有额度但根本没用」的账号。key 为空的行同样跳过——那是配置被手改坏的状态，报出来
// 只是噪音（真要修，供应商页自己会报）。
func subscriptionTargets(cfg *config.RouterConfig) []subscriptionTarget {
	targets := make([]subscriptionTarget, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		endpoint, err := parseSubscriptionEndpoint(provider.BaseURL)
		if err != nil {
			continue
		}
		vendor, ok := subscriptionVendorFor(endpoint)
		if !ok {
			continue
		}
		for _, key := range provider.Keys {
			name := strings.TrimSpace(key.Name)
			if name == "" || !key.Enabled || strings.TrimSpace(key.APIKey) == "" {
				continue
			}
			targets = append(targets, subscriptionTarget{
				// 条目 ID 就是配置里的坐标：看板上写着的 ID 能直接拿去供应商页找。
				ID:         provider.ID + "/" + name,
				Vendor:     vendor,
				ProviderID: provider.ID,
				KeyName:    name,
				Endpoint:   endpoint,
				APIKey:     key.APIKey,
			})
		}
	}
	return targets
}

// subscriptionVendorFor 找出这个端点属于哪一家。
func subscriptionVendorFor(endpoint subscriptionEndpoint) (subscriptionVendor, bool) {
	for _, vendor := range subscriptionVendors {
		if vendor.Match(endpoint) {
			return vendor, true
		}
	}
	return subscriptionVendor{}, false
}

// collectSubscriptions 读出配置里所有订阅条目的用量。
//
// 配置由调用方传入（它就是鉴权那一步已经解析好的那一份）：再解析一次不仅多一遍
// 全量解析，还会让「配置坏了」这个失败在这里被当成订阅自己的问题——而配置坏了的话
// 鉴权就已经失败了，根本走不到这一步。
func (s *Server) collectSubscriptions(ctx context.Context, cfg *config.RouterConfig) []subscriptionEntry {
	targets := subscriptionTargets(cfg)
	entries := make([]subscriptionEntry, len(targets))
	if len(targets) == 0 {
		return entries
	}

	ctx, cancel := context.WithTimeout(ctx, cpaAccountsBudget)
	defer cancel()

	limit := make(chan struct{}, subscriptionConcurrency)
	var wait sync.WaitGroup
	for index := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			entries[index] = collectSubscription(ctx, targets[index])
		}()
	}
	wait.Wait()
	return entries
}

// collectSubscription 问一个条目，并把它自己的失败留在它自己身上。
func collectSubscription(ctx context.Context, target subscriptionTarget) subscriptionEntry {
	entry := subscriptionEntry{
		ID:         target.ID,
		Kind:       target.Vendor.Kind,
		Vendor:     target.Vendor.Label,
		ProviderID: target.ProviderID,
		KeyName:    target.KeyName,
		BaseURL:    target.Endpoint.baseURL,
		Windows:    []cpaQuotaWindow{},
	}
	reading, err := target.Vendor.Collect(ctx, target)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.OK = true
	entry.ObservedAt = time.Now().UTC().Format(time.RFC3339)
	entry.Plan = reading.Plan
	entry.TierID = reading.TierID
	entry.Account = reading.Account
	entry.Status = reading.Status
	if len(reading.Windows) > 0 {
		entry.Windows = reading.Windows
	}
	entry.Summary = reading.Summary
	entry.Signals = reading.Signals
	return entry
}

// —— OpenCode Go ——

// openCodeGoUsage 对齐 GET /zen/go/v1/usage 的响应。
//
// 顶层是 `{"usage": {"rolling": …, "weekly": …, "monthly": …}}`，每个窗口是
// `{status, percent, resetsAt}`（opencode 仓库 packages/console/app/src/routes/zen/go/
// v1/usage.ts 的 formatUsage）。同时认顶层 rollingUsage/weeklyUsage/monthlyUsage 这个
// 别名形状：同一个端点在较早的实现里用过它，而认两种写法只是多三个字段，比在上游改回
// 旧形状时让看板整块空掉便宜。
type openCodeGoUsage struct {
	Usage *struct {
		Rolling *openCodeGoWindow `json:"rolling"`
		Weekly  *openCodeGoWindow `json:"weekly"`
		Monthly *openCodeGoWindow `json:"monthly"`
	} `json:"usage"`
	RollingUsage *openCodeGoWindow `json:"rollingUsage"`
	WeeklyUsage  *openCodeGoWindow `json:"weeklyUsage"`
	MonthlyUsage *openCodeGoWindow `json:"monthlyUsage"`
}

// openCodeGoWindow 是一个窗口的读数。percent 用指针接：**0 是合法读数**（这个窗口一点
// 没用），用零值区分不了「没用过」与「上游没给」。
type openCodeGoWindow struct {
	Status       string   `json:"status"`
	Percent      *float64 `json:"percent"`
	UsagePercent *float64 `json:"usagePercent"`
	ResetsAt     string   `json:"resetsAt"`
	ResetAt      string   `json:"resetAt"`
	ResetInSec   *float64 `json:"resetInSec"`
}

// opencodeGoWindowSpecs 是三个窗口的固定顺序与话术。
//
// 窗口名走 quotaWindowLabel 那套（5h/weekly/monthly → 5 小时/7 天/30 天），与 CPA 的
// 窗口在界面上是同一种写法。上游给的比例是**已用**百分数，看板要的是剩余，这里翻一次。
//
// pick 走下面三个取值方法：整个 usage 段可能缺失（上游换了形状、或反代回了空体），
// 直接 u.Usage.Rolling 会在那种响应上 panic 掉整个扇出。
var opencodeGoWindowSpecs = []struct {
	key    string
	window string
	pick   func(openCodeGoUsage) *openCodeGoWindow
}{
	{key: "rolling", window: "5h", pick: openCodeGoUsage.rollingWindow},
	{key: "weekly", window: "weekly", pick: openCodeGoUsage.weeklyWindow},
	{key: "monthly", window: "monthly", pick: openCodeGoUsage.monthlyWindow},
}

// 三个取值方法都同时认 `usage.{name}` 与顶层的 `{name}Usage` 别名。
func (u openCodeGoUsage) rollingWindow() *openCodeGoWindow {
	if u.Usage == nil {
		return u.RollingUsage
	}
	return firstOpenCodeGoWindow(u.Usage.Rolling, u.RollingUsage)
}

func (u openCodeGoUsage) weeklyWindow() *openCodeGoWindow {
	if u.Usage == nil {
		return u.WeeklyUsage
	}
	return firstOpenCodeGoWindow(u.Usage.Weekly, u.WeeklyUsage)
}

func (u openCodeGoUsage) monthlyWindow() *openCodeGoWindow {
	if u.Usage == nil {
		return u.MonthlyUsage
	}
	return firstOpenCodeGoWindow(u.Usage.Monthly, u.MonthlyUsage)
}

// openCodeGoWindowError 是 401/403 的响应体：`{type, error:{type, message}}`。
//
// 单独解一层是因为这两种状态码的含义完全不同，而它们的 message 恰好是最贴切的说明：
// 401 "Unauthorized" 是 key 不对，403 "OpenCode Go subscription required." 是这把 key
// 所在的工作区没订阅 Go。把两者折叠成「HTTP 401/403」等于把最有用的那句话丢掉。
type openCodeGoWindowError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func firstOpenCodeGoWindow(values ...*openCodeGoWindow) *openCodeGoWindow {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// collectOpenCodeGo 读 OpenCode Go 的用量。
func collectOpenCodeGo(ctx context.Context, target subscriptionTarget) (subscriptionReading, error) {
	var payload openCodeGoUsage
	// 用量端点就挂在推理端点旁边：配置里的 /zen/go/v1 加 /usage 正是官方文档给出的
	// https://opencode.ai/zen/go/v1/usage。不写死整个 URL，换了自建反代也能用。
	usageURL := target.Endpoint.baseURL + "/usage"
	if err := subscriptionGetJSON(ctx, usageURL, target.APIKey, opencodeGoAttempts, &payload); err != nil {
		return subscriptionReading{}, errors.New(opencodeGoErrorText(err))
	}
	return openCodeGoReading(payload), nil
}

// opencodeGoErrorText 把失败翻成能照做的中文。
func opencodeGoErrorText(err error) string {
	var httpErr *subscriptionHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized:
			return firstNonEmpty(httpErr.Payload, "凭据无效（401）——这把 key 不是 OpenCode Go 的订阅 key")
		case http.StatusForbidden:
			return firstNonEmpty(httpErr.Payload, "该工作区没有 OpenCode Go 订阅（403）")
		}
	}
	return err.Error()
}

// openCodeGoReading 把响应翻成窗口。
//
// 认不出的窗口名不猜、比例为空的窗口不编：上游少给一个窗口时看板上少一个环，好过编一个
// 「还剩 100%」——那会让人以为额度还在，而实际上什么都没读到。
func openCodeGoReading(payload openCodeGoUsage) subscriptionReading {
	reading := subscriptionReading{Windows: make([]cpaQuotaWindow, 0, len(opencodeGoWindowSpecs))}
	for _, spec := range opencodeGoWindowSpecs {
		raw := spec.pick(payload)
		if raw == nil {
			continue
		}
		percent, ok := raw.usedPercent()
		if !ok {
			continue
		}
		window := cpaQuotaWindow{
			Key:       "opencode-go/" + spec.key,
			Label:     quotaWindowLabel(spec.window),
			Window:    spec.window,
			Remaining: clampFraction(1 - percent/100),
			ResetAt:   raw.resetAt(),
			// 与 CPA 的现场额度同源口径：这是刚问出来的，不是从别处抄来的信号。
			Source: "quota",
		}
		if strings.EqualFold(strings.TrimSpace(raw.Status), "rate-limited") {
			window.Status = "rejected"
		}
		reading.Windows = append(reading.Windows, window)
	}
	return reading
}

// usedPercent 取 0..100 的已用百分数。
//
// 只认官方那一种量纲：上游用 Math.floor(min(100, usage/limit*100)) 算出整数百分数。
// 不去「兼容」0..1 的比例——那要靠数值大小猜量纲，而 0.5 既可能是 0.5% 也可能是 50%；
// 猜错方向会把一个快用尽的订阅画成几乎没用。宁可这个窗口不显示。
func (w openCodeGoWindow) usedPercent() (float64, bool) {
	value := w.Percent
	if value == nil {
		value = w.UsagePercent
	}
	if value == nil {
		return 0, false
	}
	// 超过 100 夹到 100 而不是丢弃：超了恰恰是最该显示的一档。
	return clampPercentValue(*value), true
}

// resetAt 取重置时间。resetsAt 是绝对时间（上游按 resetInSec 现算），resetAt 是别名；
// 两者都没有但给了 resetInSec 时按本地时钟折算——那只在上游改了字段名时才会走到。
func (w openCodeGoWindow) resetAt() string {
	if text := quotaTimeText(firstNonEmpty(w.ResetsAt, w.ResetAt)); text != "" {
		return text
	}
	if w.ResetInSec != nil && *w.ResetInSec > 0 {
		return time.Now().UTC().Add(time.Duration(*w.ResetInSec) * time.Second).Format(time.RFC3339)
	}
	return ""
}

// —— Command Code ——

// commandCodePlans 是套餐标识到人话名的映射（最长前缀优先）。
//
// 与 worker.js 的 KNOWN_PLANS 同源。planId 本身可能是 `individual-pro-v1` 这种带版本
// 后缀的串，所以必须按前缀匹配、且长的优先——否则 `individual-pro` 会先把
// `individual-pro-v1` 吃掉，把 80 的档显示成 30 的档。
//
// 只映射**名字**，不映射每月额度：额度由 /alpha/billing/credits 直接给出，把静态表里的
// 数字也画出来就等于同一件事有两个来源，而它们迟早会对不上（上游改额度不改 planId）。
var commandCodePlans = []struct {
	prefix string
	name   string
}{
	{prefix: "individual-provider", name: "Provider"},
	{prefix: "individual-goat", name: "GOAT"},
	{prefix: "individual-ultra", name: "Ultra"},
	{prefix: "individual-pro-v1", name: "Pro"},
	{prefix: "individual-max", name: "Max"},
	{prefix: "individual-pro", name: "Pro"},
	{prefix: "individual-go", name: "Go"},
	{prefix: "teams-pro", name: "Teams Pro"},
}

func init() {
	// 按前缀长度倒序排一次，而不是靠上面那张表的手写顺序：手写顺序在一次插入之后就
	// 悄悄错了，而错误的表现是「档位显示成另一个档位」，不会有任何报错。
	sort.Slice(commandCodePlans, func(i, j int) bool {
		return len(commandCodePlans[i].prefix) > len(commandCodePlans[j].prefix)
	})
}

// commandCodePlan 按 planId 找出套餐名。
func commandCodePlan(planID string) (string, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(planID), "_", "-"))
	for _, plan := range commandCodePlans {
		if strings.HasPrefix(normalized, plan.prefix) {
			return plan.name, true
		}
	}
	return "", false
}

// collectCommandCode 读 Command Code 的用量。
//
// 四个端点先后各问一次（都是小响应），并且**分别容错**：任何一个失败都只记进 signals，
// 只要还有一样读到了就把条目当成成功。这与上游那套用法完全一致——它自己也是这么做的，
// 因为这几个路由是各自独立的内部接口，一起挂掉的概率远低于其中一个漂移。
func collectCommandCode(ctx context.Context, target subscriptionTarget) (subscriptionReading, error) {
	reading := subscriptionReading{Windows: []cpaQuotaWindow{}}
	failures := make([]string, 0, 4)
	signals := map[string]string{}

	var whoami map[string]any
	if err := subscriptionGetJSON(ctx, target.Endpoint.origin+"/alpha/whoami", target.APIKey, 1, &whoami); err != nil {
		// 401/403 在这里就能定案：key 无效的话，后面三个也一定是同样的结果，
		// 没必要再发三次，也不该把「凭据坏了」稀释成四行一样的错。
		if isSubscriptionAuthError(err) {
			return subscriptionReading{}, errors.New("凭据无效——请确认这把 key 是 commandcode.ai 的账号 key")
		}
		failures = append(failures, "whoami: "+err.Error())
	} else {
		// user 可能在顶层，也可能裹在 data 里；两处都试，取不到就留空
		// （账号名是便利信息，缺了不该让条目失败）。
		user := subObject(whoami, "user")
		if user == nil {
			user = subObject(subObject(whoami, "data"), "user")
		}
		reading.Account = firstNonEmpty(subString(user, "userName"), subString(user, "username"), subString(user, "name"))
	}

	var credits map[string]any
	planIDFromCredits := ""
	if err := subscriptionGetJSON(ctx, target.Endpoint.origin+"/alpha/billing/credits", target.APIKey, 1, &credits); err != nil {
		failures = append(failures, "billing/credits: "+err.Error())
	} else {
		credit := subObject(credits, "credits")
		if credit == nil {
			credit = subObject(subObject(credits, "data"), "credits")
		}
		limits := subObject(credits, "windowLimits")
		if limits == nil {
			limits = subObject(subObject(credits, "data"), "windowLimits")
		}
		reading.Summary = append(reading.Summary, commandCodeCreditMetrics(credit)...)
		reading.Windows = append(reading.Windows, commandCodeWindows(limits)...)
		planIDFromCredits = firstNonEmpty(subString(credit, "planId"), subString(credit, "plan_id"))
		for _, name := range []string{"limited", "exceeded", "belowThreshold", "creditThreshold"} {
			if text := subScalarText(limits, name); text != "" {
				signals[name] = text
				continue
			}
			if text := subScalarText(credit, name); text != "" {
				signals[name] = text
			}
		}
	}

	var subscription map[string]any
	if err := subscriptionGetJSON(ctx, target.Endpoint.origin+"/alpha/billing/subscriptions", target.APIKey, 1, &subscription); err != nil {
		failures = append(failures, "billing/subscriptions: "+err.Error())
	} else {
		detail := subObject(subscription, "data")
		if detail == nil {
			detail = subObject(subscription, "subscription")
		}
		// planId 可能在 credits 里也可能在 subscriptions 里，而 subscriptions 是那个
		// 会失败的端点——所以先记下 credits 给的，再用 subscriptions 覆盖。
		planID := firstNonEmpty(subString(detail, "planId"), subString(detail, "plan_id"), planIDFromCredits)
		reading.TierID = planID
		if name, ok := commandCodePlan(planID); ok {
			reading.Plan = name
		} else {
			// 认不出的档位原样显示 planId：编一个名字会让人以为 AMKR 认识这个套餐。
			reading.Plan = planID
		}
		if status := subString(detail, "status"); status != "" {
			signals["status"] = status
		}
		if end := subscriptionTimeText(detail["currentPeriodEnd"]); end != "" {
			signals["currentPeriodEnd"] = end
		}
		if detail["cancelAtPeriodEnd"] == true {
			signals["cancelAtPeriodEnd"] = "true"
		}
	}

	var usage map[string]any
	if err := subscriptionGetJSON(ctx, target.Endpoint.origin+"/alpha/usage/summary", target.APIKey, 1, &usage); err != nil {
		failures = append(failures, "usage/summary: "+err.Error())
	} else {
		summary := subObject(usage, "data")
		if summary == nil {
			summary = usage
		}
		reading.Summary = append(reading.Summary, commandCodeUsageMetrics(summary)...)
	}

	// 四样全挂才算这个条目失败：把每一样都报出来，才能一眼看出是网络不通（全挂）还是
	// 某条内部路由漂移了（只挂一条）。
	if len(failures) == 4 {
		return subscriptionReading{}, errors.New("所有 commandcode.ai 端点都无法访问：" + strings.Join(failures, "；"))
	}
	if len(failures) > 0 {
		signals["读取失败"] = strings.Join(failures, "；")
	}
	if len(signals) > 0 {
		reading.Signals = signals
	}
	return reading, nil
}

// commandCodeCreditMetrics 把积分读成看板上的数值项。
//
// 三项分开显示而不是合成一个「总额」：订阅额度、已购积分、赠送积分的消耗顺序与过期规则
// 都不一样，加在一起就再也分不出哪一份先没了。单位沿用上游的 credit，不折算成金额——
// 折算率不在这些响应里。
func commandCodeCreditMetrics(credits map[string]any) []cpaQuotaMetric {
	if credits == nil {
		return nil
	}
	specs := []struct{ key, label string }{
		{key: "monthlyCredits", label: "订阅积分"},
		{key: "purchasedCredits", label: "已购积分"},
		{key: "freeCredits", label: "赠送积分"},
	}
	metrics := make([]cpaQuotaMetric, 0, len(specs))
	for _, spec := range specs {
		if value, ok := subNumber(credits, spec.key, snakeCase(spec.key)); ok {
			metrics = append(metrics, cpaQuotaMetric{
				Key: spec.key, Label: spec.label, Value: value, Unit: "credit",
			})
		}
	}
	return metrics
}

// commandCodeUsageMetrics 把聚合计数读成数值项。
func commandCodeUsageMetrics(summary map[string]any) []cpaQuotaMetric {
	if summary == nil {
		return nil
	}
	specs := []struct{ key, label, unit string }{
		{key: "totalCount", label: "请求数", unit: "次"},
		{key: "failedCount", label: "失败数", unit: "次"},
		{key: "totalTokens", label: "总 token", unit: "token"},
		{key: "totalCost", label: "总花费", unit: "credit"},
	}
	metrics := make([]cpaQuotaMetric, 0, len(specs))
	for _, spec := range specs {
		if value, ok := subNumber(summary, spec.key, snakeCase(spec.key)); ok {
			metrics = append(metrics, cpaQuotaMetric{
				Key: spec.key, Label: spec.label, Value: value, Unit: spec.unit,
			})
		}
	}
	return metrics
}

// commandCodeWindows 把 windowLimits 下的窗口翻成额度条。
//
// 上限为 0（或缺失）时**不产出窗口**：remaining 要靠 used/cap 才能算，cap 为 0 时算出来
// 是 +Inf 或负数，画到环上不是 0% 就是 100%，两个都是在编数据。
func commandCodeWindows(limits map[string]any) []cpaQuotaWindow {
	if limits == nil {
		return nil
	}
	specs := []struct {
		key    string
		window string
		names  []string
	}{
		{key: "fiveHour", window: "5h", names: []string{"fiveHour", "five_hour", "rolling5h", "5h"}},
		{key: "weekly", window: "weekly", names: []string{"weekly", "week"}},
	}
	windows := make([]cpaQuotaWindow, 0, len(specs))
	for _, spec := range specs {
		raw := subFirstObject(limits, spec.names...)
		if raw == nil {
			continue
		}
		used, usedOK := subNumber(raw, "used", "usage", "usedCredits", "used_credits")
		cap, capOK := subNumber(raw, "cap", "limit", "capCredits", "cap_credits")
		if !usedOK || !capOK || cap <= 0 {
			continue
		}
		window := cpaQuotaWindow{
			Key:       "commandcode/" + spec.key,
			Label:     quotaWindowLabel(spec.window),
			Window:    spec.window,
			Remaining: clampFraction(1 - used/cap),
			ResetAt:   subscriptionTimeText(firstValue(raw, "resetAt", "reset_at", "resetsAt")),
			Source:    "quota",
		}
		// exceeded 缺失时按 used >= cap 兜底：上游的两个字段本就是同一个意思的两种写法。
		exceeded := raw["exceeded"] == true || used >= cap
		if exceeded {
			window.Status = "rejected"
		}
		windows = append(windows, window)
	}
	return windows
}

// —— 订阅采集的公共件 ——

// subscriptionHTTPError 是一次订阅请求的失败，带上状态码与响应体里的可读文本。
//
// 单独建模（而不是 fmt.Errorf 一行）是因为调用方要按状态码分支：401/403 是「凭据/订阅
// 不对」（不该重试、也不该继续问别的端点），5xx/429 是「对端暂时不行」（该重试）。
type subscriptionHTTPError struct {
	StatusCode int
	Payload    string
	Message    string
}

func (e *subscriptionHTTPError) Error() string { return e.Message }

// isSubscriptionAuthError 判断这次失败是不是凭据问题。
func isSubscriptionAuthError(err error) bool {
	var httpErr *subscriptionHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden
	}
	return false
}

// subscriptionGetJSON 发一次 GET 并把响应解进 out。
func subscriptionGetJSON(ctx context.Context, endpoint, apiKey string, attempts int, out any) error {
	url := endpoint
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// 退避数组比尝试次数短时按最后一档等待，而不是睡 0：突然连打只会把
			// 对端的瞬时故障放大成持续故障。
			index := attempt - 1
			if index >= len(subscriptionRetryBackoff) {
				index = len(subscriptionRetryBackoff) - 1
			}
			if index >= 0 {
				subscriptionSleep(subscriptionRetryBackoff[index])
			}
		}
		lastErr = subscriptionGetOnce(ctx, url, apiKey, out)
		if lastErr == nil {
			return nil
		}
		// context 到期就没必要再试：后面的尝试都会立刻失败，而调用方在等。
		if ctx.Err() != nil || !subscriptionRetryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// subscriptionRetryable 判断这次失败值不值得重试。
func subscriptionRetryable(err error) bool {
	var httpErr *subscriptionHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		// 503 是 OpenCode Go 用量端点最常见的那种「暂时不行」，429 是限流，
		// 5xx 是上游自己出错——都值得再问一次。
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	// 网络层错误（连不上、TLS 握手失败、context 之外的读超时）不可重试：
	// 它们重试的收益很低，而每次重试都要占用 60s 预算里的一段。
	return false
}

// subscriptionGetOnce 是单次请求。
func subscriptionGetOnce(ctx context.Context, endpoint, apiKey string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", subscriptionUserAgent)

	response, err := subscriptionHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接订阅端点: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, subscriptionResponseLimit))
	if err != nil {
		return fmt.Errorf("读取订阅响应失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		// 非 JSON 的错误体（网关的 HTML 页面很常见）不能当成解析失败丢掉：那会把
		// 「对端 502」显示成「响应不是合法 JSON」，看的人会去查自己的配置。
		payload := subscriptionErrorText(raw)
		return &subscriptionHTTPError{
			StatusCode: response.StatusCode,
			Payload:    payload,
			Message:    fmt.Sprintf("HTTP %d: %s", response.StatusCode, payload),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("订阅响应不是合法 JSON: %w", err)
	}
	return nil
}

// subscriptionErrorText 从错误响应里取可读文本。
//
// 依次试三种形状：`{"error":{"message":…}}`（OpenCode 的 AuthError）、
// `{"message":…}`（OpenCode 那个 503）、`{"error":"…"}`；都不成立就退回原始文本。
func subscriptionErrorText(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "空响应"
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
		// Message 与 Detail 是各家 5xx 常见的两种字段名。
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if nested := struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}{}; len(envelope.Error) > 0 && json.Unmarshal(envelope.Error, &nested) == nil {
			if strings.TrimSpace(nested.Message) != "" {
				return strings.TrimSpace(nested.Message)
			}
		}
		if strings.TrimSpace(envelope.Message) != "" {
			return strings.TrimSpace(envelope.Message)
		}
		if strings.TrimSpace(envelope.Detail) != "" {
			return strings.TrimSpace(envelope.Detail)
		}
		if len(envelope.Error) > 0 {
			var plain string
			if json.Unmarshal(envelope.Error, &plain) == nil && strings.TrimSpace(plain) != "" {
				return strings.TrimSpace(plain)
			}
		}
	}
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}

// —— 宽松取值件 ——
//
// Command Code 的路由是非公开的，字段名在 camelCase 与 snake_case 之间摇摆过。用带默认值
// 的小工具逐个字段尝试两种写法，而不是给每个字段写一个结构体：结构体一旦对不上就是一个
// 零值，而零值在额度语境里几乎总是"看起来合法"的假数据（0 次请求、0 积分、剩 0%）。

// subObject 取一个子对象；不是对象时返回 nil。
func subObject(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	value, ok := raw[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}

// subFirstObject 按候选名依次找第一个对象。
func subFirstObject(raw map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value := subObject(raw, key); value != nil {
			return value
		}
	}
	return nil
}

// subString 取一个字符串字段。
func subString(raw map[string]any, keys ...string) string {
	if raw == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// subNumber 取一个数值字段。
//
// 除了 JSON 解码给出的 float64，也认整数与数字形态的字符串：同一个 helper 会被用在
// 「从 JSON 解出来的 map」与「用例里手搭的 map」两种输入上，而后者装的是 Go 的 int。
// 只认 float64 的话，那些分支在用例里会静默变成"字段不存在"。
func subNumber(raw map[string]any, keys ...string) (float64, bool) {
	if raw == nil {
		return 0, false
	}
	for _, key := range keys {
		switch value := raw[key].(type) {
		case float64:
			return value, true
		case float32:
			return float64(value), true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		case json.Number:
			if parsed, err := value.Float64(); err == nil {
				return parsed, true
			}
		case string:
			if parsed, ok := finiteFloat(value); ok {
				return parsed, true
			}
		}
	}
	return 0, false
}

// subScalarText 把一个标量渲染成文本，供 signals 原样显示。
func subScalarText(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	switch value := raw[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case bool:
		return fmt.Sprintf("%t", value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return ""
	}
}

// firstValue 按候选名取第一个非 nil 的原始值。
func firstValue(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

// subscriptionTimeText 解析重置时间。
//
// 比 quotaTimeText 多认一种：**epoch 毫秒**。Command Code 的 resetAt 有时是毫秒数，而
// 毫秒被当成秒解析会得到公元五万年——界面上就成了一条永远不重置的额度。
func subscriptionTimeText(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case float64:
		return epochText(value)
	case int:
		return epochText(float64(value))
	case int64:
		return epochText(float64(value))
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return epochText(parsed)
		}
		return ""
	case string:
		if parsed, ok := finiteFloat(value); ok {
			return epochText(parsed)
		}
		return quotaTimeText(value)
	default:
		return ""
	}
}

// epochText 把 epoch 秒/毫秒写成 RFC3339。
func epochText(value float64) string {
	if value <= 0 {
		return ""
	}
	// 1e12 是分界线：秒级时间戳要到公元 33658 年才会超过它。
	if value >= 1e12 {
		value /= 1000
	}
	return time.Unix(int64(value), 0).UTC().Format(time.RFC3339)
}

// snakeCase 把 camelCase 写成 snake_case，用于逐个字段尝试两种写法。
func snakeCase(name string) string {
	var builder strings.Builder
	for index, char := range name {
		if char >= 'A' && char <= 'Z' {
			if index > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(char - 'A' + 'a')
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// clampPercentValue 把 0..100 的百分数夹进范围。
func clampPercentValue(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
