package config

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件锁定并发上限的配置语义：解析（缺省 / null / 0 / 负数）与那条总和不变式。
//
// 不变式是这次功能里唯一一条「两个字段互相约束」的规则，也是最容易在写路径上被
// 绕过的一条：配置保存、管理接口写入、CLI 改动全部经 FromDict -> Validate，因此
// 把规则钉在这里就等于钉住了所有入口。

// providerWithKeys 造一份最小配置：一个供应商、若干 key（形如 "k1:4" 表示上限 4，
// "k2" 表示不设上限，"k3:-1" 表示负数），一个引用全部 key 的模型。
func providerWithKeys(t *testing.T, providerLimit string, keySpecs ...string) (*RouterConfig, error) {
	t.Helper()
	keys := make([]string, 0, len(keySpecs))
	targets := make([]string, 0, len(keySpecs))
	for index, spec := range keySpecs {
		name, limit, found := strings.Cut(spec, ":")
		body := fmt.Sprintf(`"api_key":"secret-%d"`, index)
		if found {
			body += fmt.Sprintf(`,"max_concurrency":%s`, limit)
		}
		keys = append(keys, fmt.Sprintf(`%q:{%s}`, name, body))
		targets = append(targets, fmt.Sprintf(`{"provider":"p","key":%q,"upstream_model":"u%d"}`, name, index))
	}
	providerFields := `"base_url":"https://a.example"`
	if providerLimit != "" {
		providerFields += fmt.Sprintf(`,"max_concurrency":%s`, providerLimit)
	}
	raw := fmt.Sprintf(`{"config_version":4,"local_api_key":"k",
		"providers":{"p":{%s,"keys":{%s}}},
		"models":{"m":{"targets":[%s]}}}`,
		providerFields, strings.Join(keys, ","), strings.Join(targets, ","))
	return FromDict(mustParse(t, raw))
}

// TestMaxConcurrencyParsesIntoSettings 钉住解析结果本身。
//
// 断言落在运行时会用到的三个字段上：供应商上限、供应商 key 上的上限、以及模型
// key（target 聚合出来的那份）上的上限——选路读的是最后这一份。
func TestMaxConcurrencyParsesIntoSettings(t *testing.T) {
	cfg, err := providerWithKeys(t, "10", "k1:4", "k2")
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	if got := cfg.Providers[0].MaxConcurrency; got != 10 {
		t.Fatalf("供应商上限 = %d，期望 10", got)
	}
	if got := cfg.Providers[0].Keys[0].MaxConcurrency; got != 4 {
		t.Fatalf("k1 上限 = %d，期望 4", got)
	}
	if got := cfg.Providers[0].Keys[1].MaxConcurrency; got != 0 {
		t.Fatalf("k2 未设上限应为 0，实际 %d", got)
	}
	if got := cfg.Models[0].Keys[0].MaxConcurrency; got != 4 {
		t.Fatalf("模型 key 上的上限 = %d，期望与供应商 key 一致（4）", got)
	}
}

// TestMaxConcurrencyNullAndZeroMeanUnlimited 钉住「不限制」的三种写法。
//
// null 必须与缺省同义：接口「清除上限」的落盘形态之一就是 null 或删键，若 null
// 当场解析失败，一份被接口写回过的配置就再也加载不起来。
func TestMaxConcurrencyNullAndZeroMeanUnlimited(t *testing.T) {
	cfg, err := providerWithKeys(t, "null", "k1:null", "k2:0")
	if err != nil {
		t.Fatalf("null/0 应被当成「不限制」: %v", err)
	}
	if cfg.Providers[0].MaxConcurrency != 0 || cfg.Providers[0].Keys[0].MaxConcurrency != 0 {
		t.Fatalf("null 应解析为 0（不限制），实际 %+v", cfg.Providers[0])
	}
	if cfg.Providers[0].Keys[1].MaxConcurrency != 0 {
		t.Fatalf("0 应保持为 0（不限制），实际 %d", cfg.Providers[0].Keys[1].MaxConcurrency)
	}
}

// TestMaxConcurrencyRejectsNegative 钉住负数报错。
//
// 与既有数值字段（冷却秒数等）的「负数夹到 0」不同：0 在这里是「不限制」，把写错的
// 负数静默变成不限制，恰好是这个功能最不该出错的方向。
func TestMaxConcurrencyRejectsNegative(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		providerLimit string
		keySpecs      []string
		want          string
	}{
		{"供应商负数", "-1", []string{"k1"}, "供应商 p 的 max_concurrency 不能为负数"},
		{"Key 负数", "", []string{"k1:-2"}, "供应商 p 的 Key k1 的 max_concurrency 不能为负数"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := providerWithKeys(t, testCase.providerLimit, testCase.keySpecs...)
			if err == nil {
				t.Fatal("期望报错")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("报错文本应含 %q，实际: %v", testCase.want, err)
			}
		})
	}
}

// TestKeyLimitSumMustNotExceedProviderLimit 钉住总和不变式。
//
// 三种边界各有意义：恰好相等合法（配额刚好分完）；超过一个就非法（承诺了上游不会
// 接受的同时请求数）；供应商没设上限时不做这条检查（没有总量可分的时候，Key 上限
// 只是各自的天花板）。
func TestKeyLimitSumMustNotExceedProviderLimit(t *testing.T) {
	if _, err := providerWithKeys(t, "10", "k1:4", "k2:6"); err != nil {
		t.Fatalf("4+6=10 恰好相等应当合法，实际: %v", err)
	}

	_, err := providerWithKeys(t, "10", "k1:6", "k2:6")
	if err == nil {
		t.Fatal("6+6=12 超过 10 应当报错")
	}
	want := "供应商 p 的 Key 并发上限之和 12 不能大于供应商并发上限 10"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("报错文本应含 %q，实际: %v", want, err)
	}

	if _, err := providerWithKeys(t, "", "k1:6", "k2:6"); err != nil {
		t.Fatalf("供应商未设上限时 Key 上限之和不受约束，实际: %v", err)
	}
}

// TestDisabledKeyLimitDoesNotCountTowardSum 钉住「只统计启用中的 Key」。
//
// 停用的 Key 不参与选路，也就不占用配额；把它算进总和会让「先停一把 Key，再把额度
// 挪给另一把」这个再合理不过的操作被拒掉。
func TestDisabledKeyLimitDoesNotCountTowardSum(t *testing.T) {
	raw := mustParse(t, `{"config_version":4,"local_api_key":"k",
		"providers":{"p":{"base_url":"https://a.example","max_concurrency":10,"keys":{
			"k1":{"api_key":"1","max_concurrency":6},
			"k2":{"api_key":"2","max_concurrency":6,"enabled":false}}}},
		"models":{"m":{"targets":[
			{"provider":"p","key":"k1","upstream_model":"u1"},
			{"provider":"p","key":"k2","upstream_model":"u2"}]}}}`)
	if _, err := FromDict(raw); err != nil {
		t.Fatalf("停用 Key 的上限不该计入总和: %v", err)
	}
}
