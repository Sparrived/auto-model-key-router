package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件钉住「实际上游调用」这一行的日志契约，以及能力探测两处级别的调整。
//
// 这一行在本次改动里承担了原先访问日志的职责：访问日志的 2xx/3xx 已经降到 Debug
// （internal/server/accesslog.go），默认 info 级别下 WebUI「服务日志」面板里能看见
// 的真实调用流水就只剩这里。因此它不是附带的调试输出，而是**契约**：少了它、或者
// 级别被改成 Debug，面板就又会回到「正常运行时几乎全空」的老问题。

// logLineFor 找出发出某条消息的那一行日志。
//
// 断言必须落到行上而不是对整个缓冲区 Contains：一次请求在重试时会写出**多行**调用
// 日志，而「失败那条没有 status 字段」这类反向断言一旦对整个缓冲区做，就会被成功
// 那条的行无条件满足，测试于是永远为真、什么也没测到。
//
// 匹配时不能死写 `msg="…"`：slog 的 TextHandler 只给**需要转义**的值加引号，
// 中文是可打印字符，因此消息原样出现在 `msg=` 之后。
func logLineFor(t *testing.T, logs, message string) string {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		index := strings.Index(line, "msg=")
		if index < 0 {
			continue
		}
		rest := strings.TrimPrefix(line[index+len("msg="):], `"`)
		// 消息之后要么是行尾、要么是空格分隔的下一个属性、要么是引号（加了引号时）。
		if rest == message || strings.HasPrefix(rest, message+" ") ||
			strings.HasPrefix(rest, message+`"`) {
			return line
		}
	}
	t.Fatalf("日志里没有 %q 这一行，实得:\n%s", message, logs)
	return ""
}

// assertLogField 断言某一行日志带有 field=want。
func assertLogField(t *testing.T, line, field, want string) {
	t.Helper()
	if !strings.Contains(line, field+"="+want) {
		t.Errorf("日志缺少 %s=%s，实得: %s", field, want, line)
	}
}

// assertLogLevel 断言某一行的级别。级别本身就是这次的验收面：改错一个级别，
// 默认配置下这一行就再也看不见了，而日志测试不会因此变红——所以必须显式断言。
func assertLogLevel(t *testing.T, line, want string) {
	t.Helper()
	if !strings.Contains(line, "level="+want) {
		t.Errorf("日志级别应为 %s，实得: %s", want, line)
	}
}

// TestRecordMetricLogsUpstreamCall 覆盖成功调用的完整字段，且 **metrics 为 nil**。
//
// metrics 为 nil 是关键的一半：调用日志刻意排在 `h.metrics == nil` 短路之前，
// 否则「没接指标库」这种更常见的自建部署反而一行调用日志都没有。
func TestRecordMetricLogsUpstreamCall(t *testing.T) {
	logger, logs := capturingLogger()
	handler := &Handler{logger: logger} // metrics 为 nil
	status := 200
	context := &RequestContext{
		Path:             "chat/completions",
		Request:          httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		ModelID:          "vendor-model",
		RequestedModelID: "alias-model",
		IsStream:         true,
		Workspace:        "team-a",
	}
	handler.recordMetric(context, config.KeyConfig{
		Name: "k1", Provider: "openai", UpstreamModel: "gpt-4o-mini",
	}, MetricRecord{
		StatusCode:   &status,
		DurationMS:   1234,
		FirstTokenMS: 250,
		Usage: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(30)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(12)},
			canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(42)},
		),
	})

	line := logLineFor(t, logs.String(), "上游调用完成")
	assertLogLevel(t, line, "INFO")
	for field, want := range map[string]string{
		"model_id":          "vendor-model",
		"requested_model":   "alias-model",
		"provider":          "openai",
		"key_name":          "k1",
		"upstream_model":    "gpt-4o-mini",
		"status":            "200",
		"stream":            "true",
		"api_format":        "chat/completions",
		"duration_ms":       "1234",
		"first_token_ms":    "250",
		"retried":           "false",
		"workspace":         "team-a",
		"prompt_tokens":     "30",
		"completion_tokens": "12",
		"total_tokens":      "42",
	} {
		assertLogField(t, line, field, want)
	}
}

// TestRecordMetricLogsFailedUpstreamCall 覆盖失败调用：消息变成「上游调用失败」，
// 但仍然用 Info，并且**不写**没有意义或会误导的字段。
func TestRecordMetricLogsFailedUpstreamCall(t *testing.T) {
	logger, logs := capturingLogger()
	metrics := &recordingMetrics{}
	handler := &Handler{logger: logger, metrics: metrics}
	context := &RequestContext{
		Path:    "messages",
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		ModelID: "vendor-model",
		// 请求模型与生效模型相同：requested_model 不该重复出现。
		RequestedModelID: "vendor-model",
	}
	handler.recordMetric(context, config.KeyConfig{Name: "k2", Provider: "anthropic",
		UpstreamModel: "claude-3"}, MetricRecord{
		Failed: true, DurationMS: 5000,
	})

	line := logLineFor(t, logs.String(), "上游调用失败")
	// 失败也用 Info：这一次失败的真实告警已经由 logUpstreamNotOK 的 Warn 记过，
	// 这里再记 Warning 等于同一次失败打两遍告警。
	assertLogLevel(t, line, "INFO")
	assertLogField(t, line, "model_id", "vendor-model")
	assertLogField(t, line, "key_name", "k2")
	assertLogField(t, line, "upstream_model", "claude-3")
	assertLogField(t, line, "duration_ms", "5000")

	// 上游请求压根没拿到响应（StatusCode == nil）时省略 status，不写 0：
	// 0 不是合法状态码，写出来会被当成异常读数。
	if strings.Contains(line, "status=") {
		t.Errorf("没有响应时不应出现 status 字段，实得: %s", line)
	}
	// 非流式请求的「首字节」就是总耗时，写出来只是把 duration_ms 抄一遍。
	if strings.Contains(line, "first_token_ms=") {
		t.Errorf("非流式请求不应出现 first_token_ms，实得: %s", line)
	}
	// 与生效模型相同时不重复带 requested_model。
	if strings.Contains(line, "requested_model=") {
		t.Errorf("与 model_id 相同时不应出现 requested_model，实得: %s", line)
	}
	// 没有工作空间就不带该字段。
	if strings.Contains(line, "workspace=") {
		t.Errorf("工作空间为空时不应出现 workspace，实得: %s", line)
	}
	// 指标库在场时，落库与日志是两条并行输出，不能互相顶替。
	if metrics.Count() != 1 {
		t.Errorf("指标行数 = %d，期望 1", metrics.Count())
	}
}

// TestProbeLogLevels 钉住能力探测两处级别调整。
//
// 「端点存在」是每个 key × 每种端点都会探一次的基础日志，降到 Debug；「端点不存在」
// 是探测得出的正常结论（供应商没开这个端点），不是故障，用 Info 留痕；而探测**失败**
// （请求都没发出去）仍是 Warn，由 TestNativeProbeTransportErrorIsUnsupported 覆盖行为。
func TestProbeLogLevels(t *testing.T) {
	const body = `{"model":"vendor-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

	t.Run("端点存在降为 Debug", func(t *testing.T) {
		logger, logs := capturingLogger()
		env := newTestEnv(t, nativeFirstConfig(), Options{
			Logger: logger, BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		// 401 表示「端点存在但未授权」，是探测判定的正结果。
		env.route("/v1/messages",
			jsonStep(401, `{"error":"unauthorized"}`),
			jsonStep(200, `{"type":"message","content":[]}`))
		env.request(http.MethodPost, "messages", body, nil)

		assertLogLevel(t, logLineFor(t, logs.String(), "原生端点存在"), "DEBUG")
	})

	t.Run("端点不存在为 Info", func(t *testing.T) {
		logger, logs := capturingLogger()
		env := newTestEnv(t, nativeFirstConfig(), Options{
			Logger: logger, BodyPolicy: BodyPolicyPython, Multipart: MultipartPython})
		env.route("/v1/messages", jsonStep(404, `{"error":"not found"}`))
		env.route("/v1/chat/completions",
			jsonStep(200, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
		env.request(http.MethodPost, "messages", body, nil)

		assertLogLevel(t, logLineFor(t, logs.String(), "原生端点不存在，按不支持处理"), "INFO")
	})
}

// TestRecordMetricLogsFailureWithoutStore 覆盖「失败 + 没有指标库」这一格。
//
// 成功路径与失败路径各自都有人在测，但这一格是两条轴的交点：调用日志必须在
// `h.metrics == nil` 的短路之前，所以哪怕没接指标库、这次调用也失败了，仍然是
// **一行都不能少**——否则自建部署（不配 metrics_db_path）上最容易出问题的那类
// 请求恰好什么痕迹都不留。
func TestRecordMetricLogsFailureWithoutStore(t *testing.T) {
	logger, logs := capturingLogger()
	handler := &Handler{logger: logger} // metrics 为 nil
	context := &RequestContext{
		Path:    "chat/completions",
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		ModelID: "vendor-model",
	}
	handler.recordMetric(context, config.KeyConfig{Name: "k1", Provider: "openai"},
		MetricRecord{Failed: true, DurationMS: 12})

	line := logLineFor(t, logs.String(), "上游调用失败")
	assertLogLevel(t, line, "INFO")
	assertLogField(t, line, "duration_ms", "12")
}

// TestRecordMetricUsageDialects 钉住 token 用量的两套同义键名与兜底。
//
// 上游 usage 是原样取出来的、没有归一化：OpenAI 系给 prompt_tokens/completion_tokens，
// Anthropic 系给 input_tokens/output_tokens。只认前一套的话，走原生端点
// （/v1/messages）的调用在日志里会一个 token 字段都没有——而那正是本服务的一等路径。
//
// 同时钉住「读不到就不写」：非对象、空对象、字段解析不出整数时都不带字段，
// 绝不用 0 冒充读数。
func TestRecordMetricUsageDialects(t *testing.T) {
	cases := []struct {
		name   string
		usage  *canonical.Value
		want   map[string]string
		absent []string
	}{
		{
			name: "OpenAI 三键",
			usage: canonical.NewObjectOf(
				canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(30)},
				canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(12)},
				canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(42)},
			),
			want: map[string]string{"prompt_tokens": "30", "completion_tokens": "12", "total_tokens": "42"},
		},
		{
			// Anthropic 路径：没有 total_tokens，由两个分量相加得出。
			name: "Anthropic 两键",
			usage: canonical.NewObjectOf(
				canonical.ObjectPair{Key: "input_tokens", Value: canonical.NewIntValue(11)},
				canonical.ObjectPair{Key: "output_tokens", Value: canonical.NewIntValue(7)},
			),
			want: map[string]string{"prompt_tokens": "11", "completion_tokens": "7", "total_tokens": "18"},
		},
		{
			// 只有分量、没有 total 的情况才兜底；这里连分量都缺，就一直空着。
			name:   "只有 total",
			usage:  canonical.NewObjectOf(canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(42)}),
			want:   map[string]string{"total_tokens": "42"},
			absent: []string{"prompt_tokens=", "completion_tokens="},
		},
		{
			name:   "usage 非对象",
			usage:  canonical.NewString("n/a"),
			absent: []string{"prompt_tokens=", "completion_tokens=", "total_tokens="},
		},
		{
			name:   "空对象",
			usage:  canonical.NewObject(),
			absent: []string{"prompt_tokens=", "completion_tokens=", "total_tokens="},
		},
		{
			// 字段存在但解析不出整数（非数字串）：当成读不到。注意数字串是可解析的
			// ——AsInt 复刻 Python 的 int("42")，因此这里必须用非数字串才能覆盖该分支。
			name: "字段类型不对",
			usage: canonical.NewObjectOf(
				canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewString("很多")},
				canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewString("n/a")},
			),
			absent: []string{"prompt_tokens=", "total_tokens="},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logger, logs := capturingLogger()
			handler := &Handler{logger: logger}
			context := &RequestContext{
				Path:    "messages",
				Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
				ModelID: "vendor-model",
			}
			handler.recordMetric(context, config.KeyConfig{Name: "k1", Provider: "anthropic"},
				MetricRecord{DurationMS: 5, Usage: testCase.usage})

			line := logLineFor(t, logs.String(), "上游调用完成")
			for field, want := range testCase.want {
				assertLogField(t, line, field, want)
			}
			for _, absent := range testCase.absent {
				if strings.Contains(line, absent) {
					t.Errorf("不应出现 %s，实得: %s", absent, line)
				}
			}
		})
	}
}
