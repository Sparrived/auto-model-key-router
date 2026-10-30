package quota

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Freeze 是一条额度冻结记录：某把上游 Key 因某个额度窗口耗尽而被自动摘出选路。
//
// 它是**运行时状态**，不是配置：配置里的 providers.<id>.keys.<name>.enabled 表达
// 「人工停用」，本结构表达「额度过期/自动恢复」，两者绝不互相写回。
type Freeze struct {
	// ProviderID / KeyName 是被冻结的那把 Key 的配置位置。
	ProviderID string `json:"provider_id"`
	KeyName    string `json:"key_name"`
	// Vendor 是厂商/订阅名（OpenCode Go、Claude…），供界面显示。
	Vendor string `json:"vendor,omitempty"`
	// Scope 是判定来源的层级：subscription（订阅条目） / cpa_instance（实例级联动） /
	// upstream（上游响应头 L3 信号）。
	Scope string `json:"scope"`
	// Source 是触发冻结的数据通道：account_resource（账号资源采集） / upstream_header。
	Source string `json:"source"`
	// Account 是触发冻结的账号标识（邮箱 / 文件名 / auth_index 等，可空）。
	Account string `json:"account,omitempty"`
	// InstanceID 是关联的 CPA 实例 ID（Scope=cpa_instance 时非空）。
	InstanceID string `json:"instance_id,omitempty"`
	// WindowKey / WindowLabel 描述触发冻结的窗口。
	WindowKey   string `json:"window_key"`
	WindowLabel string `json:"window_label"`
	// Remaining 是触发时的剩余比例（0..1）；Status 是上游原话（可空）。
	Remaining float64 `json:"remaining"`
	Status    string  `json:"status,omitempty"`
	// Trigger 是触发类型：hard（硬性耗尽） / threshold（低于底限）。
	Trigger string `json:"trigger,omitempty"`
	// Threshold 是触发时的底限（0..1；Trigger=threshold 时有值）。
	Threshold float64 `json:"threshold,omitempty"`
	// ResetAt 是窗口重置时间；零值表示上游没给。
	ResetAt time.Time `json:"reset_at,omitempty"`
	// FrozenAt 是首次冻结时刻（后续刷新保留），ExpiresAt 是到期自动解冻时刻。
	FrozenAt  time.Time `json:"frozen_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Reason 是一句人话，界面直接显示。
	Reason string `json:"reason"`
}

// freezeVersion 是状态文件的版本号。
//
// 与配置不同，这份文件是**纯运行时状态**：版本变化只用于将来迁移，读到不认识的版本
// 按空表处理（宁可丢冻结也不要把一份解析不了的文件当成有效状态）。
const freezeVersion = 1

// fallbackFreezeTTL 是拿不到窗口重置时间时的冻结时长。
//
// 不能无限期：额度端点长期不可达时，一个错误的判断会让 Key 永久消失。10 分钟后
// 自动解冻，下一轮巡检若仍观测到耗尽会重新冻结（那时通常已经拿到新的 reset_at）。
const fallbackFreezeTTL = 10 * time.Minute

// suppressionFloor 是手工解冻的最短抑制窗口。
//
// 手工解冻的意义是「我知道还有额度 / 我要先跑一次」，若下一轮巡检（默认 60 秒后）
// 立刻按旧读数冻回去，按钮等于没用。至少压住 10 分钟，再交由「窗口是否真的恢复」
// 决定是否重新冻结。
const suppressionFloor = 10 * time.Minute

// freezeFile 是持久化文件的形状。
type freezeFile struct {
	Version      int                `json:"version"`
	UpdatedAt    string             `json:"updated_at,omitempty"`
	Freezes      []Freeze           `json:"freezes"`
	Suppressions []suppressionEntry `json:"suppressions"`
}

// suppressionEntry 是一条手工解冻的抑制窗口。
type suppressionEntry struct {
	ProviderID string `json:"provider_id"`
	KeyName    string `json:"key_name"`
	Until      string `json:"until"`
}

// Store 是额度冻结表的持久化容器；可被选路热路径、后台巡检与 HTTP handler 并发使用。
//
// 持久化口径与 keypool.CapabilityStore 一致：文件不存在或损坏都按空表处理，落盘失败
// 只返回 error 交给调用方记日志，绝不因此让选路失败。
type Store struct {
	path  string
	clock func() time.Time

	mu           sync.Mutex
	loaded       bool
	freezes      map[string]Freeze
	suppressions map[string]time.Time
}

// NewStore 构造冻结表；path 为空表示只驻内存（嵌入/测试场景）。
func NewStore(path string) *Store {
	return NewStoreWithClock(path, time.Now)
}

// NewStoreWithClock 同上，clock 注入便于确定性测试。
func NewStoreWithClock(path string, clock func() time.Time) *Store {
	if clock == nil {
		clock = time.Now
	}
	return &Store{
		path:         path,
		clock:        clock,
		freezes:      map[string]Freeze{},
		suppressions: map[string]time.Time{},
	}
}

// Path 返回状态文件路径（空串表示只驻内存）。
func (s *Store) Path() string { return s.path }

// freezeKey 是冻结表的主键：同一把 Key 在任何时刻最多一条冻结记录。
func freezeKey(providerID, keyName string) string {
	return providerID + "\x00" + keyName
}

// sortFreezes 按 (provider_id, key_name) 排序，快照、清理与落盘共用同一口径。
func sortFreezes(freezes []Freeze) {
	sort.Slice(freezes, func(i, j int) bool {
		if freezes[i].ProviderID != freezes[j].ProviderID {
			return freezes[i].ProviderID < freezes[j].ProviderID
		}
		return freezes[i].KeyName < freezes[j].KeyName
	})
}

// Snapshot 返回当前未过期的冻结，按 (provider_id, key_name) 排序。
func (s *Store) Snapshot() []Freeze {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	now := s.clock()
	result := make([]Freeze, 0, len(s.freezes))
	for _, freeze := range s.freezes {
		if now.Before(freeze.ExpiresAt) {
			result = append(result, freeze)
		}
	}
	sortFreezes(result)
	return result
}

// Get 返回某把 Key 当前生效的冻结记录（已过期视为不存在）。
func (s *Store) Get(providerID, keyName string) (Freeze, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	freeze, found := s.freezes[freezeKey(providerID, keyName)]
	if !found || !s.clock().Before(freeze.ExpiresAt) {
		return Freeze{}, false
	}
	return freeze, true
}

// IsFrozen 报告某把 Key 当前是否处于冻结状态（选路热路径用，必须便宜）。
func (s *Store) IsFrozen(providerID, keyName string) bool {
	_, found := s.Get(providerID, keyName)
	return found
}

// FrozenReason 返回冻结的人话原因；未冻结返回空串。
//
// 与 IsFrozen 分开是为了让选路失败时的错误文案能说清「为什么这把 Key 不可用」
// （keypool 只依赖一个最小接口，不直接持有本包的 Store 类型）。
func (s *Store) FrozenReason(providerID, keyName string) string {
	freeze, found := s.Get(providerID, keyName)
	if !found {
		return ""
	}
	return freeze.Reason
}

// FrozenTrigger 返回冻结类型（hard / threshold）；未冻结返回空串。
//
// keypool 用它决定模型白名单能否豁免：只有提前冻结可豁免，硬性耗尽永远不能。
// 旧版本写入的记录没有 Trigger 字段，按硬性冻结处理（空串 != threshold），宁可少
// 放行也不能把已经耗尽的额度继续打出去。
func (s *Store) FrozenTrigger(providerID, keyName string) string {
	freeze, found := s.Get(providerID, keyName)
	if !found {
		return ""
	}
	return freeze.Trigger
}

// Upsert 新增或刷新一条冻结记录；返回是否真的发生了变化（调用方据此决定是否打日志）。
//
// 同一把 Key 只保留一条：刷新窗口、原因等字段，但**沿用仍生效记录的首次
// FrozenAt**（界面上的「自 X 起冻结」不该被每轮巡检推后）。ExpiresAt 是内置计时器目标，
// 已生效时只允许换成本次候选里**更早**的时刻：更晚或零值（没意见）都保留原值，避免
// 上游长期回旧读数时把解冻时刻无限延后。旧记录已过期时按全新一次冻结处理：评估链路
// 每轮末尾才 Prune，过期记录可能还留在表里，若无条件沿用，界面会显示出很久以前的
// 起始时间。若该 Key 处于手工解冻抑制窗口内，则忽略本次冻结（返回 false）——抑制优先。
func (s *Store) Upsert(freeze Freeze) (bool, error) {
	freeze.ProviderID = strings.TrimSpace(freeze.ProviderID)
	freeze.KeyName = strings.TrimSpace(freeze.KeyName)
	if freeze.ProviderID == "" || freeze.KeyName == "" {
		return false, errors.New("额度冻结记录缺少 provider_id / key_name")
	}
	now := s.clock()
	if freeze.FrozenAt.IsZero() {
		freeze.FrozenAt = now
	}
	// 状态文件与接口都按 UTC 口径；调用方给本地时间时在持久化边界统一。
	freeze.ResetAt = freeze.ResetAt.UTC()
	freeze.FrozenAt = freeze.FrozenAt.UTC()
	freeze.ExpiresAt = freeze.ExpiresAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	key := freezeKey(freeze.ProviderID, freeze.KeyName)
	if until, found := s.suppressions[key]; found && now.Before(until) {
		return false, nil
	}
	if existing, found := s.freezes[key]; found && now.Before(existing.ExpiresAt) {
		freeze.FrozenAt = existing.FrozenAt
		if freeze.ExpiresAt.IsZero() || !freeze.ExpiresAt.Before(existing.ExpiresAt) {
			freeze.ExpiresAt = existing.ExpiresAt
		}
		if sameFreeze(existing, freeze) {
			return false, nil
		}
	}
	if freeze.ExpiresAt.IsZero() {
		freeze.ExpiresAt = now.Add(fallbackFreezeTTL).UTC()
	}
	s.freezes[key] = freeze
	return true, s.saveLocked()
}

// sameFreeze 比较两条记录的内容；时间按瞬时比较，避免调用方带单调时钟读数时误判为变化。
func sameFreeze(a, b Freeze) bool {
	a.ResetAt, b.ResetAt = a.ResetAt.Round(0).UTC(), b.ResetAt.Round(0).UTC()
	a.FrozenAt, b.FrozenAt = a.FrozenAt.Round(0).UTC(), b.FrozenAt.Round(0).UTC()
	a.ExpiresAt, b.ExpiresAt = a.ExpiresAt.Round(0).UTC(), b.ExpiresAt.Round(0).UTC()
	return a == b
}

// Release 手工解除一条冻结；返回被解除的记录。
//
// suppressUntil 是抑制窗口的截止时间：窗口内巡检不会把同一把 Key 再冻结回去。
// 不存在（或已过期）的冻结返回 (zero, false, nil)——调用方据此回 404 而不是当成功。
// 这种情况下**不写任何状态**：抑制窗口只属于「刚被解冻的那次冻结」，凭空补一条会把
// 下一轮合法的冻结压掉，第二次 DELETE 还会误删已有的抑制。
func (s *Store) Release(providerID, keyName string, suppressUntil time.Time) (Freeze, bool, error) {
	key := freezeKey(providerID, keyName)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	now := s.clock()
	freeze, found := s.freezes[key]
	if !found || !now.Before(freeze.ExpiresAt) {
		return Freeze{}, false, nil
	}
	delete(s.freezes, key)
	if suppressUntil.After(now) {
		s.suppressions[key] = suppressUntil
	} else {
		delete(s.suppressions, key)
	}
	return freeze, true, s.saveLocked()
}

// Suppressed 报告某把 Key 是否处于手工解冻的抑制窗口内。
func (s *Store) Suppressed(providerID, keyName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	until, found := s.suppressions[freezeKey(providerID, keyName)]
	return found && s.clock().Before(until)
}

// SuppressionUntil 返回抑制窗口的截止时间（供 API 回应手工解冻的结果）。
func (s *Store) SuppressionUntil(providerID, keyName string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	until, found := s.suppressions[freezeKey(providerID, keyName)]
	if !found || !s.clock().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// Prune 清理失效状态：已到期的冻结、valid 为假的冻结（配置里已不存在/已人工停用）、
// 已过期的抑制窗口。返回被清掉的冻结，供调用方打日志。
func (s *Store) Prune(valid func(providerID, keyName string) bool) ([]Freeze, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	now := s.clock()
	removed := []Freeze{}
	changed := false
	for key, freeze := range s.freezes {
		if now.Before(freeze.ExpiresAt) && (valid == nil || valid(freeze.ProviderID, freeze.KeyName)) {
			continue
		}
		delete(s.freezes, key)
		removed = append(removed, freeze)
		changed = true
	}
	for key, until := range s.suppressions {
		if !now.Before(until) {
			delete(s.suppressions, key)
			changed = true
		}
	}
	// 只清掉过期抑制（没有冻结被清）时也要落盘：否则重启后那条本该失效的抑制会从
	// 文件里复活，把下一次合法冻结又压住。
	if !changed {
		return nil, nil
	}
	sortFreezes(removed)
	return removed, s.saveLocked()
}

// ExpireDue 删除所有已到解冻时刻的冻结并落盘（内置计时器到点由后台调用）。
// 返回按 (provider_id, key_name) 排序的已删记录；没有到期记录时不落盘。
func (s *Store) ExpireDue() ([]Freeze, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	now := s.clock()
	removed := make([]Freeze, 0)
	for key, freeze := range s.freezes {
		if now.Before(freeze.ExpiresAt) {
			continue
		}
		delete(s.freezes, key)
		removed = append(removed, freeze)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	sortFreezes(removed)
	return removed, s.saveLocked()
}

// NextExpiry 返回所有记录里最早的解冻时刻；没有记录时 ok=false。
func (s *Store) NextExpiry() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	var deadline time.Time
	found := false
	for _, freeze := range s.freezes {
		if !found || freeze.ExpiresAt.Before(deadline) {
			deadline = freeze.ExpiresAt
			found = true
		}
	}
	return deadline, found
}

// ensureLoadedLocked 首次访问时读盘；文件缺失或损坏按空表处理（不返回错误）。
//
// 调用方必须持锁。
func (s *Store) ensureLoadedLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	content, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file freezeFile
	if err := json.Unmarshal(content, &file); err != nil {
		return
	}
	if file.Version != freezeVersion {
		return
	}
	for _, freeze := range file.Freezes {
		if freeze.ProviderID == "" || freeze.KeyName == "" {
			continue
		}
		s.freezes[freezeKey(freeze.ProviderID, freeze.KeyName)] = freeze
	}
	for _, entry := range file.Suppressions {
		if entry.ProviderID == "" || entry.KeyName == "" {
			continue
		}
		until, err := time.Parse(time.RFC3339, entry.Until)
		if err != nil {
			continue
		}
		s.suppressions[freezeKey(entry.ProviderID, entry.KeyName)] = until
	}
}

// saveLocked 原子落盘；path 为空时只驻内存。调用方必须持锁。
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	file := freezeFile{
		Version:      freezeVersion,
		UpdatedAt:    s.clock().UTC().Format(time.RFC3339),
		Freezes:      make([]Freeze, 0, len(s.freezes)),
		Suppressions: make([]suppressionEntry, 0, len(s.suppressions)),
	}
	for _, freeze := range s.freezes {
		file.Freezes = append(file.Freezes, freeze)
	}
	sortFreezes(file.Freezes)
	for key, until := range s.suppressions {
		providerID, keyName, _ := strings.Cut(key, "\x00")
		file.Suppressions = append(file.Suppressions, suppressionEntry{
			ProviderID: providerID,
			KeyName:    keyName,
			Until:      until.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(file.Suppressions, func(i, j int) bool {
		if file.Suppressions[i].ProviderID != file.Suppressions[j].ProviderID {
			return file.Suppressions[i].ProviderID < file.Suppressions[j].ProviderID
		}
		return file.Suppressions[i].KeyName < file.Suppressions[j].KeyName
	})
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".quota-freezes-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, s.path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

// SuppressionFloor 返回手工解冻的最短抑制时长（导出给 API 层算 suppressUntil）。
func SuppressionFloor() time.Duration { return suppressionFloor }
