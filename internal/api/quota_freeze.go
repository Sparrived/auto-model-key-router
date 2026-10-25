package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/quota"
)

// 额度冻结的管理接口与 JSON 视图（设计稿 6.1 / 6.2）。
//
// 冻结表是**运行时状态**而不是配置：这两个接口只读它、或按人话解除一条，绝不写回
// 配置里的 enabled 字段。接口形状由前端直接消费，因此视图类型集中在这里，账号资源
// 响应（handlers_cpa.go）复用同一份映射，避免两个页面把同一时间字段渲染成两种写法。

// QuotaFreezeView 是一条额度冻结的对外视图。
//
// 与 quota.Freeze 的差别只有时间：内部用 time.Time，对外一律 RFC3339 UTC 字符串
// （前端按 UTC 解析再格式化成倒计时）；remaining 保留原值（0..1 的剩余比例），不在这里
// 乘 100——百分比写法属于展示层。
//
// 可选字段用 omitempty：vendor/account/instance_id 只对某些 scope 有意义，
// status/reset_at 上游可能没给，给空串比给一个编出来的时间更诚实。
type QuotaFreezeView struct {
	ProviderID  string  `json:"provider_id"`
	KeyName     string  `json:"key_name"`
	Vendor      string  `json:"vendor,omitempty"`
	Scope       string  `json:"scope"`
	Source      string  `json:"source"`
	Account     string  `json:"account,omitempty"`
	InstanceID  string  `json:"instance_id,omitempty"`
	WindowKey   string  `json:"window_key"`
	WindowLabel string  `json:"window_label"`
	Remaining   float64 `json:"remaining"`
	Status      string  `json:"status,omitempty"`
	ResetAt     string  `json:"reset_at,omitempty"`
	FrozenAt    string  `json:"frozen_at"`
	ExpiresAt   string  `json:"expires_at"`
	Reason      string  `json:"reason"`
}

// quotaFreezeView 把内部冻结记录转成接口视图。
func quotaFreezeView(freeze quota.Freeze) QuotaFreezeView {
	return QuotaFreezeView{
		ProviderID:  freeze.ProviderID,
		KeyName:     freeze.KeyName,
		Vendor:      freeze.Vendor,
		Scope:       freeze.Scope,
		Source:      freeze.Source,
		Account:     freeze.Account,
		InstanceID:  freeze.InstanceID,
		WindowKey:   freeze.WindowKey,
		WindowLabel: freeze.WindowLabel,
		Remaining:   freeze.Remaining,
		Status:      freeze.Status,
		ResetAt:     formatFreezeTime(freeze.ResetAt),
		FrozenAt:    formatFreezeTime(freeze.FrozenAt),
		ExpiresAt:   formatFreezeTime(freeze.ExpiresAt),
		Reason:      freeze.Reason,
	}
}

// quotaFreezeViews 批量转换；nil 输入返回空数组而不是 nil。
//
// 空数组与 null 在 JSON 里是两种形状，前端少一次判空就会炸：同一份响应里
// `"freezes":[]` 与 `"freezes":null` 只能有一种。
func quotaFreezeViews(freezes []quota.Freeze) []QuotaFreezeView {
	views := make([]QuotaFreezeView, 0, len(freezes))
	for _, freeze := range freezes {
		views = append(views, quotaFreezeView(freeze))
	}
	return views
}

// quotaFrozenKeyView 是「某个实例下被冻结的 Key」的最小引用形状（设计稿 6.2）。
//
// 只给坐标不给整条记录：实例区块要的是「解冻哪一把」，原因/到期在顶层 freezes 里
// 已经有一份，重复带上会让两个字段有机会说不一致。
type quotaFrozenKeyView struct {
	ProviderID string `json:"provider_id"`
	KeyName    string `json:"key_name"`
}

// quotaFreezeListResponse 是 GET /api/quota-freezes 的响应形状。
//
// 字段顺序按设计稿 6.1：fetched_at 在最前（它描述这一屏读数），随后是评估器状态，
// 最后是冻结清单。
type quotaFreezeListResponse struct {
	FetchedAt string `json:"fetched_at"`
	// 内嵌状态把 enabled / interval_seconds / last_evaluated_at / last_error
	// 平铺进来，与 `guard` 对象（账号资源响应里的同一份读数）是同一个类型。
	QuotaGuardStatus
	Freezes []QuotaFreezeView `json:"freezes"`
}

// quotaFreezeReleaseResponse 是 DELETE /api/quota-freezes/{provider_id}/{key_name}
// 成功时的响应。
type quotaFreezeReleaseResponse struct {
	Released        bool   `json:"released"`
	ProviderID      string `json:"provider_id"`
	KeyName         string `json:"key_name"`
	SuppressedUntil string `json:"suppressed_until"`
}

// —— GET /api/quota-freezes ——

// handleListQuotaFreezes 返回当前全部生效的额度冻结与评估器状态。
//
// 鉴权用完整管理权限（与 cpa-accounts 一致）：响应里是「哪把 Key 因为哪个窗口被摘掉」
// 这类运维读数，按访问密钥收窄范围没有意义——那把 key 自己看不到也改不了别的 key。
//
// 冻结表未装配（嵌入场景、或测试里只构造了 Server 裸结构）时回 503 而不是空数组：
// 「机制没开」与「一切正常、没有冻结」在界面上必须是两件事，静默空列表会让运维以为
// 额度一切正常。
func (s *Server) handleListQuotaFreezes(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		if s.QuotaFreezes == nil {
			return nil, httpErrorf(http.StatusServiceUnavailable, "额度冻结机制未装配")
		}
		status := QuotaGuardStatus{}
		if s.QuotaGuard != nil {
			status = s.QuotaGuard.Status()
		}
		return canonicalFromAny(quotaFreezeListResponse{
			FetchedAt:        time.Now().UTC().Format(time.RFC3339),
			QuotaGuardStatus: status,
			Freezes:          quotaFreezeViews(s.QuotaFreezes.Snapshot()),
		})
	})
}

// —— DELETE /api/quota-freezes/{provider_id}/{key_name} ——

// handleReleaseQuotaFreeze 手工解除一条冻结，并给该 Key 一个抑制窗口。
//
// 抑制窗口是这一步的全部意义：巡检默认 60 秒一轮，若下一轮立刻按仍在传的旧读数冻回去，
// 按钮等于没用。窗口取「原冻结的到期时间」与「现在 + quota.SuppressionFloor()」的较大者
// （设计稿 1/6.1）——前者让「本来就快到期」的冻结不会被人在最后几十秒里无限续命。
//
// 404 的两种情形分开报：Key 不在配置里是**请求写错了**（供应商/键名拼错或已删除），
// 没有冻结是**状态已变**（过期了、或被别的路径解了）。前端据此提示不同话术。
func (s *Server) handleReleaseQuotaFreeze(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		if s.QuotaFreezes == nil {
			return nil, httpErrorf(http.StatusServiceUnavailable, "额度冻结机制未装配")
		}
		providerID := strings.TrimSpace(r.PathValue("provider_id"))
		keyName := strings.TrimSpace(r.PathValue("key_name"))
		if !configHasProviderKey(cfg, providerID, keyName) {
			return nil, httpErrorf(http.StatusNotFound, "配置里没有该 Key: %s/%s", providerID, keyName)
		}
		freeze, found := s.QuotaFreezes.Get(providerID, keyName)
		if !found {
			return nil, httpErrorf(http.StatusNotFound, "该 Key 当前没有额度冻结")
		}
		now := time.Now()
		if s.QuotaGuard != nil {
			now = s.QuotaGuard.nowTime()
		}
		suppressUntil := freeze.ExpiresAt
		if floor := now.Add(quota.SuppressionFloor()); floor.After(suppressUntil) {
			suppressUntil = floor
		}
		if _, released, releaseErr := s.QuotaFreezes.Release(providerID, keyName, suppressUntil); releaseErr != nil {
			// 与冻结表的口径一致：落盘失败只记日志，不阻断选路，也不把「内存里已经解了」
			// 的这次操作改报成失败——否则用户会重复点击，而每次点击都在延长抑制窗口。
			s.logger().Warn("额度冻结解除后落盘失败",
				"provider_id", providerID, "key_name", keyName, "error", releaseErr)
		} else if !released {
			return nil, httpErrorf(http.StatusNotFound, "该 Key 当前没有额度冻结")
		}
		s.logger().Info("额度冻结已手工解除",
			"provider_id", providerID, "key_name", keyName,
			"suppressed_until", formatFreezeTime(suppressUntil))
		return canonicalFromAny(quotaFreezeReleaseResponse{
			Released:        true,
			ProviderID:      providerID,
			KeyName:         keyName,
			SuppressedUntil: formatFreezeTime(suppressUntil),
		})
	})
}

// configHasProviderKey 报告配置里是否存在这把 Key（**不要求启用**）。
//
// 停用的 Key 也允许手工解冻：冻结记录可能留在停用之前，用户有权把它清干净；真要说
// 「配置里没有」，那是连 providers.<id>.keys.<name> 都不存在。
func configHasProviderKey(cfg *config.RouterConfig, providerID, keyName string) bool {
	if cfg == nil {
		return false
	}
	for _, provider := range cfg.Providers {
		if provider.ID != providerID {
			continue
		}
		for _, key := range provider.Keys {
			if key.Name == keyName {
				return true
			}
		}
		return false
	}
	return false
}
