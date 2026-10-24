package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// CPA 重置卡：把订阅账号里「可以主动用掉的重置次数」读出来，并允许按最快过期优先消费。
//
// 这一块和账号额度不是同一件事：
//   - 额度是「现在还剩下多少」，CPA 的 auth-files / quota-fetch 已经给了；
//   - 重置卡是「还能手动把某个限额恢复几次、分别什么时候过期」，CPA 不采集也不暴露，
//     只能借它的 api-call 用账号凭据代打上游接口。
//
// 目前只适配两家有公开客户端行为的供应商：
//   - Codex：GET /backend-api/wham/rate-limit-reset-credits，
//     POST /backend-api/wham/rate-limit-reset-credits/consume；
//   - Claude：GET /api/oauth/usage?cedar_ember=1 / ?at_wall=1，
//     POST /api/organizations/{org}/reset_rate_limits。
//
// Claude 的「Reset for free」在 Anthropic 侧可能按 surface 门禁（OAuth 面可能回
// eligible=false / reason=surface）。这里不伪造可兑换状态：上游说不 eligible 就如实展示，
// 只有它真给了 grant 才允许消费。

const (
	// cpaResetCreditsTimeout 是单个账号重置卡读写的时限。
	//
	// 它比整块看板的 60 秒预算短：一个账号的上游卡住，不该拖住其它账号的卡片。
	cpaResetCreditsTimeout = 20 * time.Second
	// cpaClaudeOAuthBeta 是 Claude 账号接口要求的 beta 头。
	cpaClaudeOAuthBeta = "oauth-2025-04-20"
	// cpaClaudeUserAgent 伪装成 Claude Code 客户端：账号接口对非 CLI 客户端更严格。
	cpaClaudeUserAgent = "claude-cli/2.1.280 (external, cli)"
	// cpaCodexUserAgent 伪装成 Codex CLI；ChatGPT 后端对 UA 不做硬校验，但保持同源更稳。
	cpaCodexUserAgent = "codex_cli_rs/0.154.0"
	// cpaCodexResetCreditsURL 是 Codex 的「重置卡明细」端点。
	cpaCodexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	// cpaClaudeOAuthUsageURL 是 Claude 的账号用量端点；重置卡探测是它的查询参数变体。
	cpaClaudeOAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"
	cpaClaudeProfileURL    = "https://api.anthropic.com/api/oauth/profile"
	cpaClaudeResetURL      = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"
)

// cpaResetCreditsReport 是 GET /api/cpa-reset-credits 的响应。
type cpaResetCreditsReport struct {
	FetchedAt string                    `json:"fetched_at"`
	Instances []cpaInstanceResetCredits `json:"instances"`
}

type cpaInstanceResetCredits struct {
	ID       string                  `json:"id"`
	Label    string                  `json:"label"`
	BaseURL  string                  `json:"base_url"`
	OK       bool                    `json:"ok"`
	Error    string                  `json:"error,omitempty"`
	Accounts []cpaAccountResetCredit `json:"accounts"`
}

type cpaAccountResetCredit struct {
	AuthIndex   string          `json:"auth_index"`
	Name        string          `json:"name"`
	Provider    string          `json:"provider"`
	Email       string          `json:"email,omitempty"`
	Status      string          `json:"status,omitempty"`
	Disabled    bool            `json:"disabled"`
	Unavailable bool            `json:"unavailable"`
	Reset       cpaResetCredits `json:"reset_credits"`
}

// cpaResetCredits 是一个账号的重置卡读数。
//
// AvailableCount 是未过期卡片上的剩余次数（不是卡片张数：Claude 的 grant 可能一次给多次；
// 也不等于此刻可兑，RedeemableCount 才是当前能消费的次数）。Credits 按最快过期排好序，
// 界面直接取第一条可用的就是「下一次该用哪张」。
type cpaResetCredits struct {
	Provider        string           `json:"provider"`
	AvailableCount  int              `json:"available_count"`
	RedeemableCount int              `json:"redeemable_count"`
	Eligible        bool             `json:"eligible"`
	Reason          string           `json:"reason,omitempty"`
	NextCreditID    string           `json:"next_credit_id,omitempty"`
	CooldownUntil   string           `json:"cooldown_until,omitempty"`
	Credits         []cpaResetCredit `json:"credits"`
	Error           string           `json:"error,omitempty"`
}

type cpaResetCredit struct {
	ID             string   `json:"id"`
	Title          string   `json:"title,omitempty"`
	Program        string   `json:"program"`
	Status         string   `json:"status"`
	RemainingCount int      `json:"remaining_count"`
	GrantedAt      string   `json:"granted_at,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	Clears         []string `json:"clears,omitempty"`
	Usable         bool     `json:"usable"`
}

// cpaResetCreditUseResult 是 POST /api/cpa-reset-credits/use 的响应。
type cpaResetCreditUseResult struct {
	Instance  string          `json:"instance"`
	AuthIndex string          `json:"auth_index"`
	Provider  string          `json:"provider"`
	RequestID string          `json:"request_id,omitempty"`
	Credit    *cpaResetCredit `json:"credit,omitempty"`
	Outcome   string          `json:"outcome"`
	Reason    string          `json:"reason,omitempty"`
	Cleared   []string        `json:"cleared"`
}

// —— GET /api/cpa-reset-credits ——

func (s *Server) handleListCPAResetCredits(w http.ResponseWriter, r *http.Request) {
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
		return canonicalFromAny(s.collectCPAResetCredits(r.Context(), instances))
	})
}

func (s *Server) collectCPAResetCredits(ctx context.Context, instances []configops.CPAInstance) cpaResetCreditsReport {
	report := cpaResetCreditsReport{Instances: make([]cpaInstanceResetCredits, len(instances))}
	ctx, cancel := context.WithTimeout(ctx, cpaAccountsBudget)
	defer cancel()

	var wait sync.WaitGroup
	for index, instance := range instances {
		wait.Add(1)
		go func() {
			defer wait.Done()
			report.Instances[index] = s.collectCPAInstanceResetCredits(ctx, instance)
		}()
	}
	wait.Wait()
	report.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return report
}

func (s *Server) collectCPAInstanceResetCredits(ctx context.Context, instance configops.CPAInstance) cpaInstanceResetCredits {
	report := cpaInstanceResetCredits{
		ID:       instance.ID,
		Label:    instance.Label,
		BaseURL:  instance.BaseURL,
		Accounts: []cpaAccountResetCredit{},
	}
	if instance.BaseURL == "" || instance.ManagementKey == "" {
		report.Error = "实例缺少 base_url 或 management_key，请先补全"
		return report
	}

	client := cpaManagementClient{baseURL: instance.BaseURL, key: instance.ManagementKey, http: cpaHTTPClient}
	var page cpaAuthFilesPage
	if err := client.do(ctx, http.MethodGet, "/v0/management/auth-files", nil, &page); err != nil {
		report.Error = err.Error()
		return report
	}
	report.OK = true

	targets := make([]cpaAccount, 0, len(page.Files))
	for _, file := range page.Files {
		provider := firstNonEmpty(file.Provider, file.Type)
		if !cpaResetCreditsSupported(provider) {
			continue
		}
		report.Accounts = append(report.Accounts, cpaAccountResetCredit{
			AuthIndex:   file.AuthIndex,
			Name:        file.Name,
			Provider:    provider,
			Email:       file.Email,
			Status:      file.Status,
			Disabled:    file.Disabled,
			Unavailable: file.Unavailable,
			Reset: cpaResetCredits{
				Provider: provider,
				Credits:  []cpaResetCredit{},
			},
		})
		targets = append(targets, cpaAccount{
			AuthIndex:        file.AuthIndex,
			Name:             file.Name,
			Provider:         provider,
			chatGPTAccountID: cpaChatGPTAccountID(file),
		})
	}
	if len(targets) == 0 {
		return report
	}

	limit := make(chan struct{}, cpaQuotaConcurrency)
	var wait sync.WaitGroup
	for index, account := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			accountCtx, cancel := context.WithTimeout(ctx, cpaResetCreditsTimeout)
			defer cancel()
			credits, err := readCPAResetCredits(accountCtx, client, account)
			if err != nil {
				report.Accounts[index].Reset.Error = err.Error()
				return
			}
			report.Accounts[index].Reset = *credits
		}()
	}
	wait.Wait()
	return report
}

// cpaResetCreditUseLocks 把同一账号的「读卡→选卡→消费」串起来，避免并发请求同时选中
// 同一张卡各自发一次 consume。锁按 (实例, auth_index) 隔离，不阻塞其它账号。
var cpaResetCreditUseLocks sync.Map

func lockCPAResetCreditUse(instanceID, authIndex string) func() {
	key := instanceID + "\x00" + authIndex
	value, _ := cpaResetCreditUseLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// —— POST /api/cpa-reset-credits/use ——

func (s *Server) handleUseCPAResetCredit(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		payload, _, err := decodePayload(r, specCPAUseResetCredit, false)
		if err != nil {
			return nil, err
		}
		instanceID := strings.TrimSpace(payload.Lookup("instance").StringValue())
		authIndex := strings.TrimSpace(payload.Lookup("auth_index").StringValue())
		creditID := strings.TrimSpace(payload.Lookup("credit_id").StringValue())
		requestID := strings.TrimSpace(payload.Lookup("request_id").StringValue())

		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		instances, err := configops.ParseCPAInstances(data)
		if err != nil {
			return nil, err
		}
		var instance *configops.CPAInstance
		for index := range instances {
			if instances[index].ID == instanceID {
				instance = &instances[index]
				break
			}
		}
		if instance == nil {
			return nil, httpErrorf(http.StatusNotFound, "CPA 实例不存在: %s", instanceID)
		}
		if instance.BaseURL == "" || instance.ManagementKey == "" {
			return nil, httpErrorf(http.StatusConflict, "实例缺少 base_url 或 management_key，请先补全")
		}

		client := cpaManagementClient{baseURL: instance.BaseURL, key: instance.ManagementKey, http: cpaHTTPClient}
		var page cpaAuthFilesPage
		if err := client.do(r.Context(), http.MethodGet, "/v0/management/auth-files", nil, &page); err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "读取 CPA 账号清单失败: %s", err.Error())
		}
		var file *cpaAuthFileEntry
		for index := range page.Files {
			if page.Files[index].AuthIndex == authIndex {
				file = &page.Files[index]
				break
			}
		}
		if file == nil {
			return nil, httpErrorf(http.StatusNotFound, "该实例下没有账号 %s", authIndex)
		}
		provider := firstNonEmpty(file.Provider, file.Type)
		if !cpaResetCreditsSupported(provider) {
			return nil, httpErrorf(http.StatusConflict, "账号 %s 的供应商 %s 不支持重置卡", authIndex, provider)
		}
		if requestID != "" && !validResetRequestID(requestID) {
			return nil, httpErrorf(http.StatusUnprocessableEntity, "request_id 只能包含字母、数字、下划线和短横线，且不超过 64 个字符")
		}

		account := cpaAccount{
			AuthIndex:        authIndex,
			Name:             file.Name,
			Provider:         provider,
			chatGPTAccountID: cpaChatGPTAccountID(*file),
		}
		// 同一账号串行读卡、选卡、消费，避免两个请求同时挑中同一张卡。
		unlock := lockCPAResetCreditUse(instanceID, authIndex)
		defer unlock()
		readCtx, cancelRead := context.WithTimeout(r.Context(), cpaResetCreditsTimeout)
		credits, err := readCPAResetCredits(readCtx, client, account)
		cancelRead()
		if err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "读取重置卡失败: %s", err.Error())
		}
		if credits.Error != "" {
			return nil, httpErrorf(http.StatusBadGateway, "读取重置卡失败: %s", credits.Error)
		}

		if creditID != "" {
			var found *cpaResetCredit
			for index := range credits.Credits {
				if credits.Credits[index].ID == creditID {
					found = &credits.Credits[index]
					break
				}
			}
			if found == nil {
				return nil, httpErrorf(http.StatusNotFound, "该账号下没有重置卡 %s", creditID)
			}
			if !found.Usable || found.RemainingCount <= 0 {
				return nil, httpErrorf(http.StatusConflict, "重置卡 %s 当前不可用（%s）", creditID, found.Status)
			}
		}

		credit := chooseResetCredit(credits.Credits, creditID)
		if credit == nil {
			result := cpaResetCreditUseResult{
				Instance:  instanceID,
				AuthIndex: authIndex,
				Provider:  provider,
				Outcome:   "no_credit",
				Reason:    credits.Reason,
				Cleared:   []string{},
			}
			return canonicalFromAny(result)
		}

		if requestID == "" {
			requestID = newResetRequestID()
		}
		consumeCtx, cancelConsume := context.WithTimeout(r.Context(), cpaResetCreditsTimeout)
		outcome, cleared, err := consumeCPAResetCredit(consumeCtx, client, account, *credit, requestID)
		cancelConsume()
		if err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "使用重置卡失败: %s", err.Error())
		}
		if cleared == nil {
			cleared = []string{}
		}
		// Juniper 的消费接口不接受幂等键；不要回显一个实际没带给上游的 request_id。
		responseRequestID := requestID
		if credit.Program == "juniper_tide" {
			responseRequestID = ""
		}
		result := cpaResetCreditUseResult{
			Instance:  instanceID,
			AuthIndex: authIndex,
			Provider:  provider,
			RequestID: responseRequestID,
			Credit:    credit,
			Outcome:   outcome,
			Cleared:   cleared,
		}
		return canonicalFromAny(result)
	})
}

// chooseResetCredit 按「最快过期优先」挑一张卡；指定 credit_id 时只认这一张。
func chooseResetCredit(credits []cpaResetCredit, creditID string) *cpaResetCredit {
	candidates := make([]cpaResetCredit, 0, len(credits))
	for _, credit := range credits {
		if !credit.Usable || credit.RemainingCount <= 0 {
			continue
		}
		if creditID != "" && credit.ID != creditID {
			continue
		}
		candidates = append(candidates, credit)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if (left.ExpiresAt == "") != (right.ExpiresAt == "") {
			return left.ExpiresAt != ""
		}
		if left.ExpiresAt != right.ExpiresAt {
			return left.ExpiresAt < right.ExpiresAt
		}
		if left.GrantedAt != right.GrantedAt {
			return left.GrantedAt < right.GrantedAt
		}
		return left.ID < right.ID
	})
	return &candidates[0]
}

func cpaResetCreditsSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

func readCPAResetCredits(ctx context.Context, client cpaManagementClient, account cpaAccount) (*cpaResetCredits, error) {
	switch strings.ToLower(strings.TrimSpace(account.Provider)) {
	case "codex":
		return readCodexResetCredits(ctx, client, account)
	case "claude":
		return readClaudeResetCredits(ctx, client, account)
	default:
		return nil, fmt.Errorf("供应商 %s 不支持重置卡", account.Provider)
	}
}

// —— Codex ——

func readCodexResetCredits(ctx context.Context, client cpaManagementClient, account cpaAccount) (*cpaResetCredits, error) {
	empty := &cpaResetCredits{Provider: "codex", Credits: []cpaResetCredit{}}
	if strings.TrimSpace(account.chatGPTAccountID) == "" {
		return nil, fmt.Errorf("CPA 条目缺少 id_token.chatgpt_account_id，无法查询 Codex 重置卡")
	}
	response, err := client.callUpstream(ctx, account.AuthIndex, http.MethodGet, cpaCodexResetCreditsURL, map[string]string{
		"Authorization":      "Bearer $TOKEN$",
		"chatgpt-account-id": account.chatGPTAccountID,
		"Accept":             "application/json",
		"User-Agent":         cpaCodexUserAgent,
	}, "")
	if err != nil {
		return nil, err
	}
	switch {
	case resetEndpointAbsent(response.StatusCode):
		empty.Eligible = false
		empty.Reason = "not_supported"
		return empty, nil
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, cpaUpstreamError(response.StatusCode, response.Body)
	}
	payload, err := decodeResetCreditsPayload(response.Body)
	if err != nil {
		return nil, err
	}

	result := &cpaResetCredits{Provider: "codex", Eligible: true, Credits: []cpaResetCredit{}}
	result.AvailableCount = resetCreditsInt(payload, "available_count", "availableCount")
	rows, _ := payload["credits"].([]any)
	expiredRows := 0
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := resetCreditsString(row, "id")
		if id == "" {
			continue
		}
		resetType := resetCreditsString(row, "reset_type", "resetType")
		status := resetCreditsString(row, "status")
		if resetType != "" && resetType != "codex_rate_limits" && resetType != "codexRateLimits" {
			continue
		}
		if status != "" && status != "available" {
			continue
		}
		expiresAt := resetCreditsTime(row, "expires_at", "expiresAt")
		expired := expiresAt != "" && !time.Now().Before(parseResetTime(expiresAt))
		if expired {
			status = "expired"
			expiredRows++
		}
		credit := cpaResetCredit{
			ID:             id,
			Title:          resetCreditsString(row, "title"),
			Program:        "codex_rate_limits",
			Status:         firstNonEmpty(status, "available"),
			RemainingCount: 1,
			GrantedAt:      resetCreditsTime(row, "granted_at", "grantedAt"),
			ExpiresAt:      expiresAt,
			Usable:         !expired,
		}
		result.Credits = append(result.Credits, credit)
	}
	sortResetCredits(result.Credits)
	// available_count 是上游的权威计数；明细接口偶尔只回部分行时，不用明细长度覆盖它。
	// 但明细里已经过期的行不能再算进「还能用」，否则界面上会多出一次点不动的次数。
	result.AvailableCount -= expiredRows
	activeRows := 0
	for _, credit := range result.Credits {
		if credit.Usable {
			activeRows++
		}
	}
	if result.AvailableCount < activeRows {
		result.AvailableCount = activeRows
	}
	if result.AvailableCount < 0 {
		result.AvailableCount = 0
	}
	result.RedeemableCount = result.AvailableCount
	return result, nil
}

// —— Claude ——

func readClaudeResetCredits(ctx context.Context, client cpaManagementClient, account cpaAccount) (*cpaResetCredits, error) {
	var cedar *cpaResetCredits
	var cedarStatus int
	var cedarErr error
	cedarSeen := false
	payload, status, err := claudeOAuthGet(ctx, client, account.AuthIndex, "cedar_ember=1&skip_spend=1")
	cedarStatus = status
	if err != nil {
		cedarErr = err
	} else if status == http.StatusOK {
		if block, ok := payload["cedar_ember"].(map[string]any); ok {
			cedar = normalizeCedarCredits(block)
			cedarSeen = true
			// 只有确实能兑换才提前返回；surface 账号可能仍有余额，或下一张卡在冷却，
			// 这两种情况都继续问 Juniper，避免漏掉会话重置。
			if cedar.RedeemableCount > 0 {
				return cedar, nil
			}
		}
	}

	juniperPayload, juniperStatus, juniperErr := claudeOAuthGet(ctx, client, account.AuthIndex, "at_wall=1&skip_spend=1")
	var juniper *cpaResetCredits
	juniperSeen := false
	if juniperErr == nil && juniperStatus == http.StatusOK {
		if block, ok := juniperPayload["juniper_tide"].(map[string]any); ok {
			juniper = normalizeJuniperCredits(block)
			juniperSeen = true
		}
	}
	if juniperSeen {
		// Cedar 的 surface 门禁是更有用的信息：Juniper 只是 control 时不要把
		// 「这张卡只在 Claude.ai 网页端可用」覆盖成一句含糊的 control。
		if cedar != nil && cedar.Reason == "surface" && juniper.RedeemableCount == 0 {
			return cedar, nil
		}
		return juniper, nil
	}
	if cedarSeen {
		// Cedar 已经成功解析；Juniper 的认证/上游错误不能伪装成「没有卡」。
		// 保留 Cedar 的读数，同时把 Juniper 的故障挂到 error 上，读接口和消费接口都能看见。
		if probeErr := claudeProbeError(juniperStatus, juniperErr); probeErr != nil {
			cedar.Error = probeErr.Error()
		}
		return cedar, nil
	}
	if probeErr := claudeProbeError(cedarStatus, cedarErr); probeErr != nil {
		return nil, probeErr
	}
	if probeErr := claudeProbeError(juniperStatus, juniperErr); probeErr != nil {
		return nil, probeErr
	}
	// 两个探测都成功返回、但都没有对应的块，才是真的「账号没有这套卡」。
	if cedarStatus == http.StatusOK || juniperStatus == http.StatusOK {
		return &cpaResetCredits{Provider: "claude", Credits: []cpaResetCredit{}, Reason: "not_offered"}, nil
	}
	if resetEndpointAbsent(cedarStatus) && resetEndpointAbsent(juniperStatus) {
		return &cpaResetCredits{Provider: "claude", Credits: []cpaResetCredit{}, Reason: "not_supported"}, nil
	}
	return nil, cpaUpstreamError(cedarStatus, "")
}

// claudeProbeError 把一次探测归一成错误：传输/解析错误原样返回；2xx 和「端点不存在」都不算错。
func claudeProbeError(status int, err error) error {
	if err != nil {
		return err
	}
	if status == 0 || status == http.StatusOK || resetEndpointAbsent(status) {
		return nil
	}
	return cpaUpstreamError(status, "")
}

// resetEndpointAbsent 判断上游状态码是否表示「这个账号接口不存在」。
//
// 旧版 Claude/Codex 没有重置卡端点，404/405/410/501 都不该被当成故障；只有 401/403/429/5xx
// 才值得让调用方看到错误。
func resetEndpointAbsent(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func claudeOAuthGet(ctx context.Context, client cpaManagementClient, authIndex, query string) (map[string]any, int, error) {
	response, err := client.callUpstream(ctx, authIndex, http.MethodGet, cpaClaudeOAuthUsageURL+"?"+query, claudeOAuthHeaders(), "")
	if err != nil {
		return nil, 0, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, nil
	}
	payload, err := decodeResetCreditsPayload(response.Body)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return payload, response.StatusCode, nil
}

func normalizeCedarCredits(block map[string]any) *cpaResetCredits {
	result := &cpaResetCredits{
		Provider: "claude",
		Eligible: resetCreditsBool(block, "eligible"),
		Reason:   resetCreditsString(block, "ineligible_reason"),
		Credits:  []cpaResetCredit{},
	}
	cooldownUntil := resetCreditsTime(block, "cooldown_until")
	cooldownActive := cooldownUntil != "" && time.Now().Before(parseResetTime(cooldownUntil))
	result.CooldownUntil = cooldownUntil
	nextID := resetCreditsString(block, "next_grant_id")
	nextFound := false
	rows, _ := block["grants"].([]any)
	for _, raw := range rows {
		grant, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := resetCreditsString(grant, "id")
		if id == "" {
			continue
		}
		if id == nextID {
			nextFound = true
		}
		remaining := resetCreditsInt(grant, "resets_left")
		if remaining < 0 {
			remaining = 0
		}
		expiresAt := resetCreditsTime(grant, "ends_at")
		expired := expiresAt != "" && !time.Now().Before(parseResetTime(expiresAt))
		paused := resetCreditsBool(grant, "paused")
		usableNow := resetCreditsBool(grant, "usable_now")
		// next_grant_id 是上游指定的「下一张可用的卡」；上游没给或给的 id 不在 grants 里，
		// 就表示当前没有可兑换项。不能把任意一张 usable_now 的卡当成可兑换——那会让
		// 「使用」按钮点下去只收到上游的 400。
		selected := nextID != "" && id == nextID
		status := "unavailable"
		switch {
		case expired:
			status = "expired"
		case remaining == 0:
			status = "redeemed"
		case paused:
			status = "paused"
		case result.Eligible && selected && usableNow && !cooldownActive:
			status = "available"
		}
		credit := cpaResetCredit{
			ID:             id,
			Title:          resetCreditsString(grant, "label"),
			Program:        "cedar_ember",
			Status:         status,
			RemainingCount: remaining,
			GrantedAt:      resetCreditsTime(grant, "starts_at"),
			ExpiresAt:      expiresAt,
			Clears:         resetCreditsStrings(grant, "clears"),
			Usable:         status == "available",
		}
		if credit.Title == "" {
			credit.Title = "Claude 限额重置"
		}
		if credit.Usable {
			result.RedeemableCount = remaining
		}
		if !expired && remaining > 0 {
			result.AvailableCount += remaining
		}
		result.Credits = append(result.Credits, credit)
	}
	if result.Eligible && nextFound {
		result.NextCreditID = nextID
	}
	sortResetCredits(result.Credits)
	if cooldownActive && result.Reason == "" {
		result.Reason = "cooldown"
	}
	if !result.Eligible && result.Reason == "" {
		result.Reason = "ineligible"
	}
	return result
}

func normalizeJuniperCredits(block map[string]any) *cpaResetCredits {
	rawEligible := resetCreditsBool(block, "eligible")
	arm := resetCreditsString(block, "arm")
	available := resetCreditsBool(block, "available")
	eligible := rawEligible && arm == "reset"
	usable := eligible && available
	reason := resetCreditsString(block, "ineligible_reason")
	if reason == "" {
		switch {
		case !rawEligible:
			reason = "ineligible"
		case arm != "reset":
			reason = "control"
		case !available:
			reason = "unavailable"
		}
	}
	result := &cpaResetCredits{
		Provider: "claude",
		Eligible: eligible,
		Reason:   reason,
		Credits:  []cpaResetCredit{},
	}
	if arm == "reset" {
		status := "unavailable"
		if usable {
			status = "available"
			result.AvailableCount = 1
			result.RedeemableCount = 1
		}
		if eligible {
			result.NextCreditID = "juniper_tide"
		}
		result.Credits = append(result.Credits, cpaResetCredit{
			ID:             "juniper_tide",
			Title:          "Claude 会话限额重置",
			Program:        "juniper_tide",
			Status:         status,
			RemainingCount: boolToInt(usable),
			ExpiresAt:      resetCreditsTime(block, "weekly_resets_at"),
			Clears:         []string{"anthropic:5h"},
			Usable:         usable,
		})
	}
	if !usable {
		result.CooldownUntil = resetCreditsTime(block, "next_available_at")
	}
	return result
}

func claudeOAuthHeaders() map[string]string {
	return map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"anthropic-beta": cpaClaudeOAuthBeta,
		"Accept":         "application/json, text/plain, */*",
		"Content-Type":   "application/json",
		"User-Agent":     cpaClaudeUserAgent,
	}
}

// —— 消费 ——

func consumeCPAResetCredit(ctx context.Context, client cpaManagementClient, account cpaAccount, credit cpaResetCredit, requestID string) (string, []string, error) {
	switch strings.ToLower(strings.TrimSpace(account.Provider)) {
	case "codex":
		return consumeCodexResetCredit(ctx, client, account, credit, requestID)
	case "claude":
		return consumeClaudeResetCredit(ctx, client, account, credit, requestID)
	default:
		return "", nil, fmt.Errorf("供应商 %s 不支持重置卡", account.Provider)
	}
}

func consumeCodexResetCredit(ctx context.Context, client cpaManagementClient, account cpaAccount, credit cpaResetCredit, requestID string) (string, []string, error) {
	body, err := json.Marshal(map[string]string{
		"redeem_request_id": requestID,
		"credit_id":         credit.ID,
	})
	if err != nil {
		return "", nil, err
	}
	response, err := client.callUpstream(ctx, account.AuthIndex, http.MethodPost, cpaCodexResetCreditsURL+"/consume", map[string]string{
		"Authorization":      "Bearer $TOKEN$",
		"chatgpt-account-id": account.chatGPTAccountID,
		"Accept":             "application/json",
		"Content-Type":       "application/json",
		"User-Agent":         cpaCodexUserAgent,
	}, string(body))
	if err != nil {
		return "", nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", nil, cpaUpstreamError(response.StatusCode, response.Body)
	}
	payload, err := decodeResetCreditsPayload(response.Body)
	if err != nil {
		return "", nil, err
	}
	outcome := normalizeResetOutcome(resetCreditsString(payload, "outcome", "code"))
	if outcome == "" {
		return "", nil, fmt.Errorf("上游响应缺少 outcome")
	}
	return outcome, nil, nil
}

func consumeClaudeResetCredit(ctx context.Context, client cpaManagementClient, account cpaAccount, credit cpaResetCredit, requestID string) (string, []string, error) {
	orgID, err := claudeOrganizationID(ctx, client, account.AuthIndex)
	if err != nil {
		return "", nil, err
	}
	var body []byte
	switch credit.Program {
	case "cedar_ember":
		body, err = json.Marshal(map[string]string{
			"program":    "cedar_ember",
			"grant_id":   credit.ID,
			"request_id": requestID,
		})
	case "juniper_tide":
		body, err = json.Marshal(map[string]string{"program": "juniper_tide"})
	default:
		return "", nil, fmt.Errorf("未知的 Claude 重置卡类型: %s", credit.Program)
	}
	if err != nil {
		return "", nil, err
	}
	target := fmt.Sprintf(cpaClaudeResetURL, orgID)
	response, err := client.callUpstream(ctx, account.AuthIndex, http.MethodPost, target, claudeOAuthHeaders(), string(body))
	if err != nil {
		return "", nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", nil, cpaUpstreamError(response.StatusCode, response.Body)
	}
	payload, err := decodeResetCreditsPayload(response.Body)
	if err != nil {
		return "", nil, err
	}
	outcome := normalizeResetOutcome(resetCreditsString(payload, "result"))
	if outcome == "" {
		return "", nil, fmt.Errorf("上游响应缺少 result")
	}
	cleared := resetCreditsStrings(payload, "cleared")
	if outcome == "reset" && len(cleared) == 0 {
		cleared = credit.Clears
	}
	return outcome, cleared, nil
}

func claudeOrganizationID(ctx context.Context, client cpaManagementClient, authIndex string) (string, error) {
	response, err := client.callUpstream(ctx, authIndex, http.MethodGet, cpaClaudeProfileURL, claudeOAuthHeaders(), "")
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", cpaUpstreamError(response.StatusCode, response.Body)
	}
	payload, err := decodeResetCreditsPayload(response.Body)
	if err != nil {
		return "", err
	}
	invalid := false
	if organization, ok := payload["organization"].(map[string]any); ok {
		if uuid := resetCreditsString(organization, "uuid"); uuid != "" {
			if validClaudeOrganizationID(uuid) {
				return uuid, nil
			}
			invalid = true
		}
	}
	if uuid := resetCreditsString(payload, "organization_uuid"); uuid != "" {
		if validClaudeOrganizationID(uuid) {
			return uuid, nil
		}
		invalid = true
	}
	if invalid {
		return "", fmt.Errorf("Claude 组织 ID 格式不合法")
	}
	return "", fmt.Errorf("Claude 账号资料里没有组织 ID，无法使用重置卡")
}

// validClaudeOrganizationID 对齐 Claude 客户端对组织 ID 的限制，避免把异常字符带进 URL 路径。
func validClaudeOrganizationID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func cpaChatGPTAccountID(entry cpaAuthFileEntry) string {
	if len(entry.IDToken) == 0 {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(entry.IDToken, &claims); err == nil {
		if id := codexAccountIDFromClaims(claims); id != "" {
			return id
		}
	}
	// 兼容某些 CPA 版本直接把原始 JWT 放进 id_token 的情况：claims 在第二段。
	var raw string
	if err := json.Unmarshal(entry.IDToken, &raw); err != nil {
		return ""
	}
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return codexAccountIDFromClaims(claims)
}

func codexAccountIDFromClaims(claims map[string]any) string {
	if id := resetCreditsString(claims, "chatgpt_account_id"); id != "" {
		return id
	}
	// OpenAI 的原始 JWT 把账号信息放在这个命名空间 claim 下。
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		return resetCreditsString(auth, "chatgpt_account_id")
	}
	return ""
}

// —— 解析与工具 ——

func (c cpaManagementClient) callUpstream(ctx context.Context, authIndex, method, target string, header map[string]string, data string) (*cpaAPICallResponse, error) {
	payload := map[string]any{
		"auth_index": authIndex,
		"method":     method,
		"url":        target,
		"header":     header,
	}
	if data != "" {
		payload["data"] = data
	}
	var response cpaAPICallResponse
	if err := c.do(ctx, http.MethodPost, "/v0/management/api-call", payload, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type cpaAPICallResponse struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func decodeResetCreditsPayload(raw string) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("上游响应不是 JSON 对象: %w", err)
	}
	return payload, nil
}

func resetCreditsString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			if text := signalText(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func resetCreditsInt(row map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := row[key].(type) {
		case float64:
			return int(value)
		case json.Number:
			parsed, _ := value.Int64()
			return int(parsed)
		case string:
			parsed, _ := strconv.Atoi(strings.TrimSpace(value))
			return parsed
		}
	}
	return 0
}

func resetCreditsBool(row map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			parsed, _ := value.(bool)
			return parsed
		}
	}
	return false
}

func resetCreditsStrings(row map[string]any, key string) []string {
	raw, _ := row[key].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := signalText(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func resetCreditsTime(row map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := row[key].(type) {
		case string:
			if parsed := resetEpochText(value); parsed != "" {
				return parsed
			}
		case float64:
			if parsed := resetEpochText(strconv.FormatInt(int64(value), 10)); parsed != "" {
				return parsed
			}
		case json.Number:
			if parsed := resetEpochText(value.String()); parsed != "" {
				return parsed
			}
		}
	}
	return ""
}

// resetEpochText 兼容上游把时间给成毫秒时间戳的写法；RFC3339 字符串原样交给 quotaTimeText。
func resetEpochText(raw string) string {
	text := strings.TrimSpace(raw)
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil && seconds > 1_000_000_000_000 {
		text = strconv.FormatInt(seconds/1000, 10)
	}
	return quotaTimeText(text)
}

func parseResetTime(text string) time.Time {
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func sortResetCredits(credits []cpaResetCredit) {
	sort.SliceStable(credits, func(i, j int) bool {
		left, right := credits[i], credits[j]
		if (left.ExpiresAt == "") != (right.ExpiresAt == "") {
			return left.ExpiresAt != ""
		}
		if left.ExpiresAt != right.ExpiresAt {
			return left.ExpiresAt < right.ExpiresAt
		}
		if left.GrantedAt != right.GrantedAt {
			return left.GrantedAt < right.GrantedAt
		}
		return left.ID < right.ID
	})
}

func normalizeResetOutcome(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "reset":
		return "reset"
	case "already_used", "already_redeemed", "alreadyredeemed":
		return "already_redeemed"
	case "not_limited", "nothing_to_reset", "nothingtoreset":
		return "nothing_to_reset"
	case "no_credit", "nocredit":
		return "no_credit"
	default:
		return strings.TrimSpace(raw)
	}
}

func validResetRequestID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func newResetRequestID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err == nil {
		return hex.EncodeToString(buffer[:])
	}
	return fmt.Sprintf("cpa-reset-%d", time.Now().UnixNano())
}

func cpaUpstreamError(status int, body string) error {
	text := strings.TrimSpace(body)
	if text == "" {
		text = "空响应"
	} else if len(text) > 300 {
		text = text[:300] + "…"
	}
	return fmt.Errorf("上游 HTTP %d: %s", status, text)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
