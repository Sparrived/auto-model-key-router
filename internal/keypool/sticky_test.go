package keypool

import (
	"fmt"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

func TestStickyKeysBoundedLRUAndTTL(t *testing.T) {
	now := 1000.0
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			{
				ID:          "claude",
				RoutingMode: "round_robin",
				Keys: []config.KeyConfig{
					{Name: "k1", Provider: "p1", APIKey: "s1", BaseURL: "https://a.test", Enabled: true},
					{Name: "k2", Provider: "p1", APIKey: "s2", BaseURL: "https://a.test", Enabled: true},
				},
			},
		},
	}
	pool := New(cfg, nil, func() float64 { return now })
	pool.maxStickyKeys = 3
	pool.stickyTTL = 60.0

	// 写入 5 个不同 affinityKey，容量应严格封顶在 3。
	for i := 0; i < 5; i++ {
		key, err := pool.NextKey("claude", nil, nil, fmt.Sprintf("conv-%d", i))
		if err != nil {
			t.Fatalf("NextKey 失败: %v", err)
		}
		pool.ReleaseKey("claude", key.Name)
	}
	if got := pool.StickyKeyCount(); got != 3 {
		t.Fatalf("stickyKeys 数量 = %d，期望封顶 3", got)
	}

	// 时间前进超过 TTL 后，旧项被清理、不再命中粘滞。
	now += 61.0
	key, err := pool.NextKey("claude", nil, nil, "conv-fresh")
	if err != nil {
		t.Fatalf("NextKey 失败: %v", err)
	}
	pool.ReleaseKey("claude", key.Name)
	if got := pool.StickyKeyCount(); got != 1 {
		t.Fatalf("过期后 stickyKeys 数量 = %d，期望仅剩 1 条新项", got)
	}
}
