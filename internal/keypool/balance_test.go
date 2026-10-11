package keypool

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// TestInsufficientBalanceDetection 固化「欠费」判定的正反两面，以及判定的**依据**。
//
// 反面用例比比正面更重要：判定宽了会把一个正常账号拦在路由之外（而且最长 30 分钟），
// 判定窄了只是少一次自动限流。因此这里把**限流**这类「看起来像但必须排除」的响应体
// 逐个钉住，尤其是 Google 的 429（文案里同样有 quota 字样，但那是每分钟配额，会自己好）。
//
// 同时钉住 `BalanceBodySignal` 的取值：它是限流日志里唯一能说明「凭什么判它欠费」的字段，
// 写宽了（例如把整句话塞进去）或写窄了（例如只留一个空串）都会让事后追责变难。它对
// `200` 也照常返回信号，因为「只看正文」是它的定义，是否成立由调用方按状态码决定。
func TestInsufficientBalanceDetection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
		// wantSignal 是响应体里命中的信号（不含 402 那条状态码依据）。
		wantSignal string
	}{ // 402 不看响应体：该状态码的产品语义就是「先付款」。
		{name: "402_空体", status: 402, body: "", want: true},
		{name: "402_网关HTML", status: 402, body: "<html>Payment Required</html>", want: true},
		{
			name:       "402_DeepSeek",
			status:     402,
			body:       `{"error":{"message":"Insufficient Balance","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`,
			want:       true,
			wantSignal: "insufficient balance",
		},
		{
			// OpenAI 走 429，靠 code 与那句独有的 billing 文案区分于限流；标记与文案都在，
			// 取标记（更短更明确）。
			name:       "429_OpenAI_insufficient_quota",
			status:     429,
			body:       `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`,
			want:       true,
			wantSignal: "insufficient_quota",
		},
		{
			name:       "400_Anthropic_余额过低",
			status:     400,
			body:       `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`,
			want:       true,
			wantSignal: "credit balance is too low",
		},
		{
			name:       "402_OpenRouter",
			status:     402,
			body:       `{"error":{"message":"Insufficient credits. Add more using https://openrouter.ai/settings/credits","code":402}}`,
			want:       true,
			wantSignal: "insufficient credit",
		},
		{
			name:       "429_智谱_账户欠费",
			status:     429,
			body:       `{"error":{"code":"1113","message":"您的账户已欠费，请充值后重试"}}`,
			want:       true,
			wantSignal: "欠费",
		},
		{
			name:       "400_中文余额不足",
			status:     400,
			body:       `{"error":{"message":"余额不足，请充值"}}`,
			want:       true,
			wantSignal: "余额不足",
		},
		// 标记字段的写法不统一：空格、连字符、大小写都要归一。
		{
			name:       "标记带空格",
			status:     400,
			body:       `{"error":{"code":"Insufficient Quota"}}`,
			want:       true,
			wantSignal: "insufficient_quota",
		},
		{
			name:       "标记带连字符",
			status:     403,
			body:       `{"error":{"type":"billing-hard-limit-reached"}}`,
			want:       true,
			wantSignal: "billing_hard_limit_reached",
		},

		// --- 反面：这些都不许判成欠费 ---
		{
			name:   "429_限流",
			status: 429,
			body:   `{"error":{"message":"Rate limit reached for gpt-x on tokens per min. Please try again in 20ms.","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`,
			want:   false,
		},
		{
			// Google 的 RESOURCE_EXHAUSTED 也是 429，文案里带 quota，但是按分钟/按天
			// 的调用配额，过一会儿自己会好——按欠费限流 30 分钟是错的。
			name:   "429_Google_每分钟配额",
			status: 429,
			body:   `{"error":{"code":429,"message":"Quota exceeded for quota metric 'Generate Content API requests per minute'","status":"RESOURCE_EXHAUSTED"}}`,
			want:   false,
		},
		{
			// Gemini **免费档** 429 的真实响应体：它套用了 OpenAI 那句
			// `check your plan and billing details`，但同时给了 RESOURCE_EXHAUSTED 与
			// `Please retry in Ns`——45 秒后就自愈的限流，绝不能当欠费处理。
			// 这条用例是判定表的一条铁律：人话短语不能收「限流也会用的句子」。
			name:   "429_Google_免费档真实响应体",
			status: 429,
			body:   `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. \n Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 20, model: gemini-2.5-flash Please retry in 45.06576809s.","status":"RESOURCE_EXHAUSTED"}}`,
			want:   false,
		},
		{
			// xAI 的欠费文案（只有人话，没有标记）。
			name:       "403_xAI_额度用光",
			status:     403,
			body:       `{"error":"Your team has either used all available credits or reached its monthly spending limit."}`,
			want:       true,
			wantSignal: "used all available credits",
		},
		{
			// OpenAI 文档里的另一种标记写法。
			name:       "429_OpenAI_credit_balance_exhausted",
			status:     429,
			body:       `{"error":{"message":"Your credit balance is exhausted.","type":"credit_balance_exhausted","code":"credit_balance_exhausted"}}`,
			want:       true,
			wantSignal: "credit_balance_exhausted",
		},
		{
			name:   "401_密钥无效",
			status: 401,
			body:   `{"error":{"message":"Incorrect API key provided: sk-xxx.","type":"invalid_request_error","code":"invalid_api_key"}}`,
			want:   false,
		},
		{
			name:   "500_网关错误",
			status: 500,
			body:   `{"error":{"message":"upstream connect error or disconnect/reset before headers"}}`,
			want:   false,
		},
		{
			// 状态码 <400 时一概不判：正文里出现这些字样多半是**内容**（用户让模型
			// 解释「余额不足」是什么意思），拿它限流一个正常账号是灾难。
			// 注意 wantSignal 仍是「余额不足」：BalanceBodySignal 只回答「正文说了什么」，
			// 状态码门槛由 InsufficientBalance 把关。两层分开钉住，门槛被悄悄挪进
			// BalanceBodySignal（或被删掉）时才会有人发现。
			name:       "200_正文里出现余额不足",
			status:     200,
			body:       `{"choices":[{"message":{"content":"余额不足指的是账户没有可用余额。"}}]}`,
			want:       false,
			wantSignal: "余额不足",
		},
		{name: "400_工具参数错误", status: 400, body: `{"error":{"message":"tools: Invalid schema"}}`, want: false},
		{name: "503_空体", status: 503, body: "", want: false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := InsufficientBalance(item.status, []byte(item.body)); got != item.want {
				t.Fatalf("InsufficientBalance(%d, %s) = %v，期望 %v",
					item.status, item.body, got, item.want)
			}
			if got := BalanceBodySignal([]byte(item.body)); got != item.wantSignal {
				t.Fatalf("BalanceBodySignal(%s) = %q，期望 %q", item.body, got, item.wantSignal)
			}
		})
	}
}

// TestBalanceCooldownBackoff 固化退避算术、半开窗口与恢复语义。
//
// 时序全部用注入的 clock 推进，不依赖真实时间：这套算术的每一个边界（窗口内、恰好
// 到期、翻倍、封顶）都是「差一秒就做错决定」的地方。
func TestBalanceCooldownBackoff(t *testing.T) {
	const base = 300.0
	now := 1000.0
	store := NewKeyHealthStore(func() float64 { return now })

	if store.IsBalanceExhausted("acct") {
		t.Fatal("没有记录时不该处于限流中")
	}

	// 第一次命中：窗口 = base。
	window, attempts := store.MarkBalanceExhausted("acct", "Insufficient Balance", base)
	if window != 300 || attempts != 1 {
		t.Fatalf("首次命中：window=%v attempts=%d，期望 300 / 1", window, attempts)
	}
	if !store.IsBalanceExhausted("acct") {
		t.Fatal("命中后应处于限流中")
	}
	// 窗口内一律限流，到期即放行（半开：放行只是「允许再试一次」，状态仍在）。
	now = 1000 + 299
	if !store.IsBalanceExhausted("acct") {
		t.Fatal("299 秒时仍应在限流窗口内")
	}
	now = 1000 + 300
	if store.IsBalanceExhausted("acct") {
		t.Fatal("300 秒时窗口应已到期（半开）")
	}

	// 再次命中：翻倍；持续命中则一路翻到上限 1800 并停在那里。
	wants := []struct {
		attempts int
		window   float64
	}{{2, 600}, {3, 1200}, {4, 1800}, {5, 1800}}
	for _, want := range wants {
		window, attempts = store.MarkBalanceExhausted("acct", "Insufficient Balance", base)
		if attempts != want.attempts || window != want.window {
			t.Fatalf("第 %d 次命中：window=%v attempts=%d，期望 %v / %d",
				want.attempts, window, attempts, want.window, want.attempts)
		}
	}
	// 状态记录保留：窗口过期后仍要能看出「这个账号最近为什么被挡」。
	state, found := store.BalanceExhausted("acct")
	if !found || state.Attempts != 5 || state.Reason != "Insufficient Balance" {
		t.Fatalf("欠费状态记录不完整：%+v（found=%v）", state, found)
	}

	// 一次成功请求即清空（这就是「在可用时自动恢复」的落点，不必等窗口到期）。
	store.ClearBalanceExhausted("acct")
	if store.IsBalanceExhausted("acct") {
		t.Fatal("清空后不该仍处于限流中")
	}
	if _, found := store.BalanceExhausted("acct"); found {
		t.Fatal("清空后不该留下欠费记录")
	}

	// base <= 0 表示关闭这项机制。
	disabled := NewKeyHealthStore(func() float64 { return now })
	if window, attempts := disabled.MarkBalanceExhausted("a", "余额不足", 0); window != 0 || attempts != 0 {
		t.Fatalf("关闭档不该产生窗口：window=%v attempts=%d", window, attempts)
	}
	if disabled.IsBalanceExhausted("a") {
		t.Fatal("关闭档不该限流")
	}

	// 没有账号标识（上游 key 为空、供应商与基址也空）时拒绝记账，避免把「所有
	// 无标识的 key」当成同一个账号一起限流。
	anonymous := NewKeyHealthStore(func() float64 { return now })
	if window, attempts := anonymous.MarkBalanceExhausted("", "", base); window != 0 || attempts != 0 {
		t.Fatalf("空标识不该记账：window=%v attempts=%d", window, attempts)
	}
}

// TestBalanceAndCooldownAreIndependent 固化两套状态互不干扰。
//
// 普通失败（429/5xx）与欠费是两件事：前者按失败次数累计并受 300 秒上限约束，后者按
// 自己的窗口退避。混在一起的表现是「一次 500 把欠费的 30 分钟窗口冲掉」或者反过来。
func TestBalanceAndCooldownAreIndependent(t *testing.T) {
	now := 5000.0
	plain := NewKeyHealthStore(func() float64 { return now })
	// 普通失败（429）不会产生欠费状态。
	plain.MarkFailure("m", "k", intPtr(429), nil, 1, 60)
	if !plain.IsCoolingDown("m", "k") {
		t.Fatal("429 应让该 key 进入冷却")
	}
	if plain.IsBalanceExhausted("acct") {
		t.Fatal("普通失败不该产生欠费状态")
	}

	// 欠费不会写成普通冷却。
	exhausted := NewKeyHealthStore(func() float64 { return now })
	exhausted.MarkBalanceExhausted("acct", "余额不足", 300)
	if !exhausted.IsBalanceExhausted("acct") {
		t.Fatal("命中后应处于限流中")
	}
	if exhausted.IsCoolingDown("m", "k") {
		t.Fatal("欠费不该写成普通冷却")
	}

	// 两套状态共存时各清各的：清欠费不动冷却。
	both := NewKeyHealthStore(func() float64 { return now })
	both.MarkFailure("m", "k", intPtr(429), nil, 1, 60)
	both.MarkBalanceExhausted("acct", "余额不足", 300)
	both.ClearBalanceExhausted("acct")
	if both.IsBalanceExhausted("acct") {
		t.Fatal("清欠费后不该仍被限流")
	}
	if !both.IsCoolingDown("m", "k") {
		t.Fatal("清欠费不该动普通冷却")
	}

	// Reset 两者一起清（配置热重载）。
	both.MarkBalanceExhausted("acct", "余额不足", 300)
	both.Reset()
	if both.IsBalanceExhausted("acct") {
		t.Fatal("Reset 应清空欠费状态")
	}
	if both.IsCoolingDown("m", "k") {
		t.Fatal("Reset 应清空冷却状态")
	}
}

// TestConcurrentBalanceExclusionIsNeverLost 固化「并发下欠费排除一刻都不松」。
//
// 欠费状态是**账号级**的：一把凭据挂在多个模型上时，一个模型在选路（读
// balanceExhaustedLocked）的同时，另一个模型上的请求可能正在给它续期（写同一条目）。
// 这里让 8 个 worker 各自反复选路 + 续期 + 读诊断，任何一次选路把已被限流的账号交出去，
// 或诊断读到的计数与选路不一致，都算失败——锁纪律出问题时这几种撕裂最先暴露
// （`-race` 在本机需要 cgo，故断言不依赖竞态检测器）。MarkSuccess 的清理路径不掺进来：
// 它会**有意**放开排除，与这条「不许松开」的断言方向相反，那条路径由
// TestMarkSuccessClearsBalance 与 TestConcurrentNextKeyDoesNotRace 覆盖。
func TestConcurrentBalanceExclusionIsNeverLost(t *testing.T) {
	now := 1000.0
	pool := New(balanceTestConfig(t), nil, func() float64 { return now })
	// mirror 只有一把 key（other-credential），先把它推进限流窗口。
	if window, _ := pool.MarkBalanceExhausted("mirror", "k2", "insufficient_quota"); window <= 0 {
		t.Fatalf("准备阶段应记下限流窗口，实得 %v", window)
	}

	const workers = 8
	const perWorker = 50
	var wait sync.WaitGroup
	problems := make(chan string, workers*perWorker)
	for worker := range workers {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := range perWorker {
				// 唯一候选被限流 ⇒ 必须空手而归，且绝不能因为「集合为空」而软回退。
				if key, err := pool.NextKey("mirror", nil, nil, ""); err == nil {
					problems <- "选路交出了已限流的账号：" + key.Name
					continue
				}
				switch (worker + index) % 3 {
				case 0:
					// 另一侧（同账号的另一个模型）续期窗口。
					pool.MarkBalanceExhausted("m", "k2", "insufficient_quota")
				case 1:
					if limited, total := pool.BalanceLimitedKeys("mirror", nil); limited != total || total != 1 {
						problems <- fmt.Sprintf("诊断计数与选路不一致：limited=%d total=%d", limited, total)
					}
				default:
					if state, found := pool.BalanceState("mirror", "k2"); !found || state.Attempts < 1 || !pool.IsBalanceExhausted("mirror", "k2") {
						problems <- fmt.Sprintf("窗口内的状态读不回来：found=%v state=%+v", found, state)
					}
				}
			}
		}(worker)
	}
	wait.Wait()
	close(problems)
	for problem := range problems {
		t.Fatalf("欠费排除在并发下失效：%s", problem)
	}
}

// balanceTestConfig 是「一个账号挂在两个模型上」的配置，用于钉住账号级语义。
//
// m 与 other 用的是**同一把凭据**（api_key 相同）但 key 名不同，mirror 用的是另一把
// ——「同账号不同名」与「同名不同账号」正是 (模型, key 名) 做键时会出错的两种情况。
func balanceTestConfig(t *testing.T) *config.RouterConfig {
	t.Helper()
	return mustConfig(t, `{
		"config_version": 4, "local_api_key": "local",
		"key_balance_cooldown_seconds": 300,
		"providers": {"p": {"base_url": "https://a.example", "keys": {
			"k1": {"api_key": "shared-credential"},
			"k2": {"api_key": "other-credential"}}}},
		"models": {
			"m": {"targets": [
				{"provider": "p", "key": "k1", "upstream_model": "u1"},
				{"provider": "p", "key": "k2", "upstream_model": "u2"}],
				"routing_mode": "round_robin"},
			"other": {"targets": [
				{"provider": "p", "key": "k1", "name": "renamed", "upstream_model": "u3"}]},
			"mirror": {"targets": [
				{"provider": "p", "key": "k2", "upstream_model": "u4"}]}
		}
	}`)
}

// TestNextKeySkipsBalanceExhaustedAccount 固化「欠费账号自动限流」的选路行为。
func TestNextKeySkipsBalanceExhaustedAccount(t *testing.T) {
	now := 1000.0
	pool := New(balanceTestConfig(t), nil, func() float64 { return now })

	window, attempts := pool.MarkBalanceExhausted("m", "k1", "Insufficient Balance")
	if window != 300 || attempts != 1 {
		t.Fatalf("窗口/次数：%v / %d，期望 300 / 1", window, attempts)
	}

	// m 上只剩 k2 可选：连选 4 次都不该回到 k1。
	for range 4 {
		key, err := pool.NextKey("m", nil, nil, "")
		if err != nil {
			t.Fatalf("选择失败: %v", err)
		}
		if key.Name != "k2" {
			t.Fatalf("欠费账号仍被选中：%q", key.Name)
		}
		pool.ReleaseKey("m", key.Name)
	}

	// 账号级：`other` 里的 key 名是 renamed（同一把凭据）也被挡——欠费说的是账号，
	// 不是某条路由。
	if _, err := pool.NextKey("other", nil, nil, ""); !errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("同一账号的另一个模型应无可用 key，实际 err=%v", err)
	}
	// 另一把凭据不受影响。
	if key, err := pool.NextKey("mirror", nil, nil, ""); err != nil || key.Name != "k2" {
		t.Fatalf("无关账号被误伤：key=%v err=%v", key.Name, err)
	}

	// 窗口到期后（半开）它重新进入候选。
	now = 1000 + 300
	seen := map[string]bool{}
	for range 4 {
		key, err := pool.NextKey("m", nil, nil, "")
		if err != nil {
			t.Fatalf("到期后选择失败: %v", err)
		}
		seen[key.Name] = true
		pool.ReleaseKey("m", key.Name)
	}
	if !seen["k1"] {
		t.Fatalf("窗口到期后欠费账号应重新参与轮转，实际只见到 %v", seen)
	}
}

// TestBalanceExclusionIsNotSoft 固化「欠费不参与软回退」这条唯一例外。
//
// 冷却过滤是软的（全都冷却时仍放行，既定降级策略）；欠费不是——放行一次就等于每个
// 请求都试一次，自动限流也就无从谈起。两者共存时，冷却的 key 仍可放行，欠费的不可。
func TestBalanceExclusionIsNotSoft(t *testing.T) {
	// 单 key：普通冷却仍走软回退（既有契约不变）。
	single := mustConfig(t, `{
		"config_version": 4, "local_api_key": "local",
		"key_failure_threshold": 1,
		"providers": {"p": {"base_url": "https://a.example", "keys": {"k1": {"api_key": "c1"}}}},
		"models": {"m": {"targets": [{"provider": "p", "key": "k1", "upstream_model": "u1"}]}}
	}`)
	cooling := New(single, nil, func() float64 { return 1000 })
	cooling.MarkFailure("m", "k1", intPtr(429), nil)
	if !cooling.IsCoolingDown("m", "k1") {
		t.Fatal("429 应让该 key 进入冷却")
	}
	if key, err := cooling.NextKey("m", nil, nil, ""); err != nil || key.Name != "k1" {
		t.Fatalf("冷却中的 key 仍应被软回退放行：key=%v err=%v", key.Name, err)
	}

	// 同样这把 key 被判欠费之后：不再放行。
	limited := New(single, nil, func() float64 { return 1000 })
	limited.MarkBalanceExhausted("m", "k1", "余额不足")
	if _, err := limited.NextKey("m", nil, nil, ""); !errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("欠费账号不该被软回退放行，实际 err=%v", err)
	}
}

// TestMarkSuccessClearsBalance 固化「一次成功即恢复」。
func TestMarkSuccessClearsBalance(t *testing.T) {
	now := 1000.0
	pool := New(balanceTestConfig(t), nil, func() float64 { return now })

	pool.MarkBalanceExhausted("m", "k1", "余额不足")
	if !pool.IsBalanceExhausted("m", "k1") {
		t.Fatal("记账后应处于限流中")
	}
	// 成功来自**同一账号的另一个模型**也算数：凭据有钱了，就是有钱了。
	pool.MarkSuccess("other", "renamed")
	if pool.IsBalanceExhausted("m", "k1") {
		t.Fatal("成功请求应清空该账号的欠费状态")
	}
	if limited, total := pool.BalanceLimitedKeys("m", nil); limited != 0 || total != 2 {
		t.Fatalf("清空后限流计数应归零：limited=%d total=%d", limited, total)
	}
}

// TestBalanceLimitedKeysCountsCandidates 固化诊断计数与选路口径一致。
func TestBalanceLimitedKeysCountsCandidates(t *testing.T) {
	now := 1000.0
	pool := New(balanceTestConfig(t), nil, func() float64 { return now })
	if limited, total := pool.BalanceLimitedKeys("m", nil); limited != 0 || total != 2 {
		t.Fatalf("未限流时：limited=%d total=%d，期望 0 / 2", limited, total)
	}
	pool.MarkBalanceExhausted("m", "k1", "余额不足")
	if limited, total := pool.BalanceLimitedKeys("m", nil); limited != 1 || total != 2 {
		t.Fatalf("限流一个账号后：limited=%d total=%d，期望 1 / 2", limited, total)
	}
}
