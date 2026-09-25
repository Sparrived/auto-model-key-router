package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// CPA 账号资源：把若干个 CLIProxyAPI（下称 CPA）实例的账号额度汇总到一块看板。
//
// 为什么由 AMKR 的**服务端**去访问 CPA 而不是让浏览器直连：
//
//   - CPA 的管理接口不在浏览器同源之内，也没有 CORS 头，页面直接 fetch 必被拦；
//   - management_key 是能改 CPA 配置的凭据，放进浏览器就等于放进任何一条 XSS 的
//     射程，而它需要长期保存（不像 AMKR 自己的 key 那样可以只在会话里用）。
//
// 因此这里只做一件事：拿配置里的实例清单，替调用方去问一遍，把结果归一化后回给页面。
// 它**不改 CPA 的任何状态**（只读 auth-files 与 quota/fetch），也不缓存——看板上的数
// 字与 CPA 里的一致，没有中间层可以过期。
const (
	// cpaAccountsBudget 是一次账号资源扇出的总预算。
	//
	// 这是一个同步 handler：界面在等它。账号多的实例上「每个账号问一次额度」可能很慢，
	// 所以用总预算兜住——到点还没回来的账号报「额度查询超时」，而不是让页面一直转。
	// 60 秒是给「一台实例上几十个账号」留的余量，不是期望值。
	cpaAccountsBudget = 60 * time.Second
	// cpaQuotaConcurrency 是并发拉取额度的上限：既别把对端打满，也别串行等到天荒地老。
	cpaQuotaConcurrency = 4
	// cpaResponseLimit 是单次响应的读取上限。CPA 的 auth-files 在多账号实例上可以很大，
	// 但再大也不该无限读进内存；4 MiB 对几百个账号绰绰有余。
	cpaResponseLimit = 4 << 20
)

// —— GET /api/cpa-instances ——

// handleListCPAInstances 返回配置里的 CPA 实例清单（含 management_key）。
//
// 明文回 management_key 与 /api/providers 回 api_key 是同一条取舍：这个接口要求完整
// 管理权限，而完整权限本来就能导出整份配置（/api/config/export 里全是明文上游 key）。
// 真正的边界是「谁能调到这个接口」，不是「响应里藏不藏」——藏了只会让界面没法编辑。
func (s *Server) handleListCPAInstances(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		instances, err := configops.CPAInstances(data)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "instances", Value: instances},
		))
	})
}

// —— PUT /api/cpa-instances ——

// handleReplaceCPAInstances 整体替换实例清单。
//
// 用一个 PUT 而不是 POST/PUT/DELETE 三条：界面上要改的是「这份清单」，而不是清单里的
// 某一条。三条接口会把「删掉一个实例」拆成一次额外往返，也会让「加一个、改一个」变成
// 两次读-改-写，各带一次 config_revision 校验。
func (s *Server) handleReplaceCPAInstances(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specCPAInstances, false)
		if err != nil {
			return nil, err
		}
		incoming := payload.Lookup("instances")
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.ReplaceCPAInstances(data, incoming)
		}, optString(payload, "config_revision")); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		instances, err := configops.CPAInstances(data)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "instances", Value: instances},
		))
	})
}

// —— GET /api/cpa-accounts ——

// cpaAccountsReport 是一次账号资源扇出的结果。
type cpaAccountsReport struct {
	FetchedAt string                     `json:"fetched_at"`
	Instances []cpaInstanceAccountReport `json:"instances"`
}

// cpaInstanceAccountReport 是一个实例的账号清单；实例级失败只影响它自己这一块。
type cpaInstanceAccountReport struct {
	ID         string       `json:"id"`
	Label      string       `json:"label"`
	BaseURL    string       `json:"base_url"`
	OK         bool         `json:"ok"`
	Error      string       `json:"error,omitempty"`
	ObservedAt string       `json:"observed_at,omitempty"`
	Accounts   []cpaAccount `json:"accounts"`
}

// cpaAccount 是一个账号在账号资源页上的全部读数。
//
// 只搬运看板要用的字段，**原样丢弃** CPA 条目里的 id_token（JWT claims）、path、
// size 等：那些是 CPA 本地排查用的，没有必要进浏览器。
type cpaAccount struct {
	AuthIndex     string            `json:"auth_index"`
	Name          string            `json:"name"`
	Provider      string            `json:"provider"`
	AccountType   string            `json:"account_type,omitempty"`
	ProjectID     string            `json:"project_id,omitempty"`
	Label         string            `json:"label,omitempty"`
	Email         string            `json:"email,omitempty"`
	Status        string            `json:"status,omitempty"`
	StatusMessage string            `json:"status_message,omitempty"`
	Disabled      bool              `json:"disabled"`
	Unavailable   bool              `json:"unavailable"`
	Success       int               `json:"success"`
	Failed        int               `json:"failed"`
	SupportsQuota bool              `json:"supports_quota"`
	QuotaProvider string            `json:"quota_provider,omitempty"`
	Plan          string            `json:"plan,omitempty"`
	TierID        string            `json:"tier_id,omitempty"`
	Windows       []cpaQuotaWindow  `json:"windows"`
	Signals       map[string]string `json:"signals,omitempty"`
	// ModelQuotas 是按模型维度的被动额度（CPA 的 model_quotas）。claude/codex 这类
	// 「每个模型各自一组头」的 provider 会给出它，看板因此能展开到逐模型。
	ModelQuotas map[string]cpaModelQuota `json:"model_quotas,omitempty"`
	// Summary 是归一化额度里的数值项（CPA 的 summary[]）：额度插件用它回余额、积分、
	// 计费系数这类**不成窗口**的量。CPA 自己不带，只有插件或声明式探测会填。
	Summary []cpaQuotaMetric `json:"summary,omitempty"`
	// RecentRequests 是 CPA 维护的十分钟桶请求计数，看板用它画迷你趋势。
	RecentRequests []cpaRecentRequest `json:"recent_requests,omitempty"`
	// ServerTimeOffsetMs 是对端与本地时钟的差（CPA 的 serverTimeOffsetMs）：重置倒计时
	// 用本地时钟算，不校正就会整体偏一段。
	ServerTimeOffsetMs int64           `json:"server_time_offset_ms,omitempty"`
	Cooldowns          json.RawMessage `json:"cooldowns,omitempty"`
	QuotaError         string          `json:"quota_error,omitempty"`
}

// cpaQuotaMetric 是归一化额度里的一个数值项，对齐 CPA 的 pluginapi.QuotaMetric。
//
// Format/Unit/Currency 原样透传而不是在这里格式化：同一个 value 在不同单位下读法不同
// （百分比、次数、金额），把「怎么显示」放在前端，服务端只负责别把单位弄丢。
type cpaQuotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

// cpaRecentRequest 是 CPA 的十分钟请求桶。
type cpaRecentRequest struct {
	Time    string `json:"time"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`
}

// cpaModelQuota 是某个模型维度的被动额度快照。
//
// Windows 由该模型自己的信号解析出来（复用 passiveQuotaWindows）：provider 级的
// Signals 与 model_quotas 里的信号是同一套头，因此解析口径也必须是同一份代码，
// 否则同一个窗口在汇总行与展开行里会显示成两个数。
type cpaModelQuota struct {
	ObservedAt string            `json:"observed_at,omitempty"`
	Signals    map[string]string `json:"signals,omitempty"`
	Windows    []cpaQuotaWindow  `json:"windows"`
}

// handleListCPAAccounts 汇总各实例的账号与额度。
//
// 鉴权用完整管理权限：响应里既有各实例的账号标识（邮箱、文件名），也有额度水位，
// 这些是运维读数而不是某一把 key 自己的用量——按 key 收窄范围在这里没有意义，
// 只会造出一个「看得到一半」的看板。
func (s *Server) handleListCPAAccounts(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		instances, err := configops.ParseCPAInstances(data)
		if err != nil {
			return nil, err
		}
		return canonicalFromAny(s.collectCPAAccounts(r.Context(), instances))
	})
}

// collectCPAAccounts 并发问一遍所有实例。
//
// 实例之间并发、实例内部串行：实例数量是个位数，而单个实例的账号可能很多，账号那一层
// 由 fillQuotaWindows 自己限流。
func (s *Server) collectCPAAccounts(ctx context.Context, instances []configops.CPAInstance) cpaAccountsReport {
	report := cpaAccountsReport{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Instances: make([]cpaInstanceAccountReport, len(instances)),
	}
	ctx, cancel := context.WithTimeout(ctx, cpaAccountsBudget)
	defer cancel()

	var wait sync.WaitGroup
	for index, instance := range instances {
		wait.Add(1)
		go func() {
			defer wait.Done()
			report.Instances[index] = s.collectCPAInstance(ctx, instance)
		}()
	}
	wait.Wait()
	return report
}

// collectCPAInstance 读一个实例的账号清单，再按需补额度。
func (s *Server) collectCPAInstance(ctx context.Context, instance configops.CPAInstance) cpaInstanceAccountReport {
	report := cpaInstanceAccountReport{
		ID:       instance.ID,
		Label:    instance.Label,
		BaseURL:  instance.BaseURL,
		Accounts: []cpaAccount{},
	}
	// 手改配置或旧版本写下的残缺实例：报在它自己这一块上，别让整个看板失败。
	if instance.BaseURL == "" || instance.ManagementKey == "" {
		report.Error = "实例缺少 base_url 或 management_key，请在上方补全后重试"
		return report
	}

	client := cpaManagementClient{baseURL: instance.BaseURL, key: instance.ManagementKey, http: cpaHTTPClient}
	var page cpaAuthFilesPage
	if err := client.do(ctx, http.MethodGet, "/v0/management/auth-files", nil, &page); err != nil {
		report.Error = err.Error()
		return report
	}

	report.OK = true
	report.ObservedAt = page.ObservedAt
	for _, file := range page.Files {
		// provider 先取出来：复合字面量里没法引用正在声明的变量，而窗口与逐模型额度
		// 都要按同一个 provider 解析。
		provider := firstNonEmpty(file.Provider, file.Type)
		account := cpaAccount{
			AuthIndex:      file.AuthIndex,
			Name:           file.Name,
			Provider:       provider,
			AccountType:    file.AccountType,
			ProjectID:      file.ProjectID,
			Label:          file.Label,
			Email:          file.Email,
			Status:         file.Status,
			StatusMessage:  file.StatusMessage,
			Disabled:       file.Disabled,
			Unavailable:    file.Unavailable,
			Success:        file.Success,
			Failed:         file.Failed,
			SupportsQuota:  file.SupportsQuota,
			QuotaProvider:  file.QuotaProvider,
			Signals:        signalTexts(file.Quota.Signals),
			ModelQuotas:    modelQuotaMap(provider, file.ModelQuotas),
			RecentRequests: file.RecentRequests,
			Cooldowns:      file.Cooldowns,
		}
		// 被动信号先落成窗口：即使额度探测失败或对端没有额度插件，看板也有东西可显示。
		account.Windows = passiveQuotaWindows(provider, signalsOf(file.Quota))
		report.Accounts = append(report.Accounts, account)
	}

	s.fillQuotaWindows(ctx, client, report.Accounts)
	return report
}

// fillQuotaWindows 给声明了 supports_quota 的账号现场问一次额度，覆盖被动信号的结果。
//
// 覆盖而不是合并：两边算的都是同一件事（这个窗口还剩多少），而 quota/fetch 是现场问
// 出来的、被动信号可能已经是几小时前的快照，混在一起会出现「一半旧一半新」的进度条。
func (s *Server) fillQuotaWindows(ctx context.Context, client cpaManagementClient, accounts []cpaAccount) {
	targets := make([]int, 0, len(accounts))
	for index := range accounts {
		// auth_index 是 quota/fetch 唯一的入参，缺了就没法问——CPA 会回 400。
		if accounts[index].SupportsQuota && accounts[index].AuthIndex != "" {
			targets = append(targets, index)
		}
	}
	if len(targets) == 0 {
		return
	}

	limit := make(chan struct{}, cpaQuotaConcurrency)
	var wait sync.WaitGroup
	for _, index := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			var response cpaQuotaProbeResponse
			if err := client.do(ctx, http.MethodPost, "/v0/management/quota/fetch",
				map[string]string{"auth_index": accounts[index].AuthIndex}, &response); err != nil {
				// 只在**什么都没得到**时才报错：有被动窗口的账号，探测失败（对端没装
				// 额度插件会回 501）不影响看板，报出来只是噪音。
				if len(accounts[index].Windows) == 0 {
					accounts[index].QuotaError = err.Error()
				}
				return
			}
			// 探测只要成功就采纳整份结果，而不是「有窗口才采纳」：窗口、数值项与订阅
			// 档位是同一次响应里的三样东西，只回余额、不回窗口的插件（计费类插件常见）
			// 否则会被判成「什么都没给」。
			if windows := quotaProbeWindows(response); len(windows) > 0 {
				accounts[index].Windows = windows
				accounts[index].QuotaError = ""
			}
			accounts[index].Plan = quotaPlan(response)
			accounts[index].TierID = quotaTierID(response)
			accounts[index].Summary = response.Summary
			accounts[index].ServerTimeOffsetMs = response.ServerTimeOffsetMs
		}()
	}
	wait.Wait()
}

// quotaProbeWindows 把 CPA 归一化额度拍平成窗口列表。
//
// 拍平而不是保留 groups 嵌套：界面上一行账号就是一串窗口，多一层嵌套只会把 CPA 的形状
// 泄漏进页面代码。但**组名要跟着窗口走**——Antigravity 的 Gemini 与 Claude/GPT 各有
// 一套 5 小时 + 周期额度，不带上组名，两组窗口看起来就是四条重名的条。
//
// label 取窗口名（归一成「5 小时」这类话术），上游给的那句说明放 description：以前
// label 优先取 description，于是「You have used some of your weekly limit, it will
// fully refresh in 5 days, 9 hours.」会被当成标签画在进度条旁边，而同一页的另一条
// 写着「5h」——同一种东西两种读法。
func quotaProbeWindows(response cpaQuotaProbeResponse) []cpaQuotaWindow {
	windows := make([]cpaQuotaWindow, 0, len(response.Groups))
	for groupIndex, group := range response.Groups {
		groupName := strings.TrimSpace(group.DisplayName)
		for bucketIndex, bucket := range group.Buckets {
			window := strings.TrimSpace(bucket.Window)
			label := quotaWindowLabel(window)
			if label == "" {
				label = groupName
			}
			if label == "" {
				label = fmt.Sprintf("窗口 %d", bucketIndex+1)
			}
			windows = append(windows, cpaQuotaWindow{
				Key:         fmt.Sprintf("quota/%d/%d", groupIndex, bucketIndex),
				Label:       label,
				Window:      window,
				Group:       groupName,
				Description: strings.TrimSpace(bucket.Description),
				Remaining:   clampFraction(bucket.RemainingFraction),
				ResetAt:     quotaTimeText(bucket.ResetTime),
				Source:      "quota",
			})
		}
	}
	return windows
}

// quotaPlan 取订阅档位名：plan 为空时退回 tierName，两者都没有就是空串。
func quotaPlan(response cpaQuotaProbeResponse) string {
	if response.Subscription == nil {
		return ""
	}
	return firstNonEmpty(response.Subscription.Plan, response.Subscription.TierName)
}

// quotaTierID 取订阅档位的机器名（如 `free-tier`）。
//
// 与 plan 分开存：plan 是给人看的（可能被各家翻译成不同说法），tierId 是上游的稳定标识，
// 看板拿它做「同一个套餐的不同叫法」对齐，混进 plan 就再也分不出哪个是标识了。
func quotaTierID(response cpaQuotaProbeResponse) string {
	if response.Subscription == nil {
		return ""
	}
	return strings.TrimSpace(response.Subscription.TierID)
}

// modelQuotaMap 把 CPA 的 model_quotas 转成看板形状，并给每个模型解析出窗口。
//
// 复用 passiveQuotaWindows 而不是另写一份解析：model_quotas 里装的就是同一族响应头，
// 两处口径一旦分叉，同一个窗口在汇总行与展开行里会显示成两个数。
func modelQuotaMap(provider string, raw map[string]cpaQuotaObservation) map[string]cpaModelQuota {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]cpaModelQuota, len(raw))
	for model, entry := range raw {
		name := strings.TrimSpace(model)
		if name == "" {
			continue
		}
		out[name] = cpaModelQuota{
			ObservedAt: entry.ObservedAt,
			Signals:    signalTexts(entry.Signals),
			Windows:    passiveQuotaWindows(provider, signalsOf(entry)),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// signalsOf 从被动快照里取信号表。
func signalsOf(quota cpaQuotaObservation) map[string]any {
	if len(quota.Signals) == 0 {
		return nil
	}
	return quota.Signals
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// —— CPA 管理面客户端 ——

// cpaHTTPClient 是访问各 CPA 实例管理面的客户端。
//
// 复用同一个客户端：它自带连接池，而看板一次扇出会发很多请求。超时设成总预算，
// 真正的时限由调用方的 context 决定（那是整次扇出共享的）。
//
// 不设 CheckRedirect / Transport：CPA 通常在 127.0.0.1 或内网，默认传输已足够，
// 而 Go 默认不代理 localhost（httpproxy 对 loopback 有内建豁免），不会被 HTTP_PROXY
// 劫走。
var cpaHTTPClient = &http.Client{Timeout: cpaAccountsBudget}

// cpaManagementClient 只读地访问一个 CPA 实例的管理面。
type cpaManagementClient struct {
	baseURL string
	key     string
	http    *http.Client
}

// do 发一次管理面请求并把响应解进 out。
//
// 只发 Authorization 一种凭据头：CPA 同时认 X-Management-Key，但同一把 key 出现在两个
// 头里没有任何好处，只会多一处泄漏点（上游日志、抓包都多一份）。
func (c cpaManagementClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("构造请求失败: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接 CPA: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, cpaResponseLimit))
	if err != nil {
		return fmt.Errorf("读取 CPA 响应失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, cpaErrorText(raw))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("CPA 响应不是合法 JSON: %w", err)
	}
	return nil
}

// cpaErrorText 从 CPA 的错误响应里取可读文本。
//
// CPA 的错误体统一是 {"error": "..."}（见 internal/api/handlers/management/
// plugin_quota.go 的各处 c.JSON），取不到就退回原始文本——把「501 空体」显示成
// 「HTTP 501: 空响应」也比显示一个光秃秃的状态码强。
func cpaErrorText(raw []byte) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && strings.TrimSpace(envelope.Error) != "" {
		return strings.TrimSpace(envelope.Error)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "空响应"
	}
	if len(text) > 200 {
		// 按字节截断可能切断一个多字节字符，但这里只是错误提示，不值得为它引入
		// 按 rune 截断的额外代码路径。
		return text[:200] + "…"
	}
	return text
}

// —— CPA 响应形状 ——

// cpaAuthFilesPage 是 GET /v0/management/auth-files 的响应。
//
// 只声明用得到的字段：CPA 的条目还有 path/size/id_token 等一长串，json 解码会忽略
// 未声明的键，而搬运它们到浏览器没有任何用处。
type cpaAuthFilesPage struct {
	ObservedAt string             `json:"observed_at"`
	Files      []cpaAuthFileEntry `json:"files"`
}

type cpaAuthFileEntry struct {
	AuthIndex     string              `json:"auth_index"`
	Name          string              `json:"name"`
	Type          string              `json:"type"`
	Provider      string              `json:"provider"`
	AccountType   string              `json:"account_type"`
	ProjectID     string              `json:"project_id"`
	Label         string              `json:"label"`
	Email         string              `json:"email"`
	Status        string              `json:"status"`
	StatusMessage string              `json:"status_message"`
	Disabled      bool                `json:"disabled"`
	Unavailable   bool                `json:"unavailable"`
	Success       int                 `json:"success"`
	Failed        int                 `json:"failed"`
	SupportsQuota bool                `json:"supports_quota"`
	QuotaProvider string              `json:"quota_provider"`
	Quota         cpaQuotaObservation `json:"quota"`
	// ModelQuotas 只有该 provider 支持按模型观测时才出现（CPA 的 modelQuotas 分支）。
	ModelQuotas map[string]cpaQuotaObservation `json:"model_quotas"`
	// RecentRequests 是 CPA 自己维护的十分钟桶，随 auth-files 一起回，不需要额外请求。
	RecentRequests []cpaRecentRequest `json:"recent_requests"`
	Cooldowns      json.RawMessage    `json:"cooldowns"`
}

// cpaQuotaObservation 是 CPA 采到的被动额度快照。
//
// Signals 用 map[string]any 而不是 map[string]string：CPA 那边确实是 map[string]string，
// 但一处类型不符会让整份账号清单解码失败，而这里的用途只是展示——不值得为类型假设
// 赌上整个看板。
type cpaQuotaObservation struct {
	ObservedAt string         `json:"observed_at"`
	Signals    map[string]any `json:"signals"`
}

// cpaQuotaProbeResponse 对齐 CPA 的 pluginapi.QuotaFetchResponse。
//
// 字段名是 camelCase，与 CPA 的 JSON tag 一致（它同时接受 snake_case，这里只发不解析，
// 因此认一种就够）。
//
// ServerTimeOffsetMs 是 CPA 与它对上游时钟的差，看板的重置倒计时按本地时钟算，带上它
// 才能把倒计时摆正；Summary 是「不成窗口的数值项」（余额、积分、计费系数），CPA 自己
// 不产生，只有额度插件或声明式探测会填，所以它可能为空而窗口非空，反之亦然。
type cpaQuotaProbeResponse struct {
	Subscription *struct {
		Plan     string `json:"plan"`
		TierName string `json:"tierName"`
		TierID   string `json:"tierId"`
	} `json:"subscription"`
	Summary            []cpaQuotaMetric `json:"summary"`
	ServerTimeOffsetMs int64            `json:"serverTimeOffsetMs"`
	Groups             []struct {
		DisplayName string `json:"displayName"`
		Buckets     []struct {
			Window            string  `json:"window"`
			RemainingFraction float64 `json:"remainingFraction"`
			ResetTime         string  `json:"resetTime"`
			Description       string  `json:"description"`
		} `json:"buckets"`
	} `json:"groups"`
}

// canonicalFromAny 把一个 Go 值转成响应用的 canonical.Value。
//
// 绕这一圈（marshal 再 parse）而不是手搭 canonical 对象：账号资源是个多层嵌套结构，
// 手搭要写几十行 SetKey，而结构体上的 json tag 本来就已经把形状说清楚了。
func canonicalFromAny(value any) (*canonical.Value, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical.Parse(encoded)
}
