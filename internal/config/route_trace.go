package config

import (
	"strconv"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

const (
	DefaultRouteTraceRetentionHours = 24
	MinRouteTraceRetentionHours     = 1
	MaxRouteTraceRetentionHours     = 168
)

// RouteTraceConfig 是入站请求准入时读取的追踪配置快照。
type RouteTraceConfig struct {
	Enabled        bool
	RetentionHours int
}

// ParseRouteTraceConfig 严格读取可选设置。旧配置不需要补写该字段。
func ParseRouteTraceConfig(raw *canonical.Value) (RouteTraceConfig, error) {
	cfg := RouteTraceConfig{RetentionHours: DefaultRouteTraceRetentionHours}
	if raw == nil {
		return cfg, nil
	}
	if !raw.IsObject() {
		return RouteTraceConfig{}, errf("route_trace 必须是对象")
	}
	for _, key := range raw.Obj.Keys() {
		value := raw.Lookup(key)
		switch key {
		case "enabled":
			enabled, ok := value.AsBool()
			if !ok {
				return RouteTraceConfig{}, errf("route_trace.enabled 必须是布尔值")
			}
			cfg.Enabled = enabled
		case "retention_hours":
			if canonical.PyTypeName(value) != "int" {
				return RouteTraceConfig{}, errf("route_trace.retention_hours 必须是整数")
			}
			hours, err := strconv.Atoi(value.Num)
			if err != nil || hours < MinRouteTraceRetentionHours || hours > MaxRouteTraceRetentionHours {
				return RouteTraceConfig{}, errf("route_trace.retention_hours 必须在 %d–%d 小时之间", MinRouteTraceRetentionHours, MaxRouteTraceRetentionHours)
			}
			cfg.RetentionHours = hours
		default:
			return RouteTraceConfig{}, errf("route_trace 不支持字段 %q", key)
		}
	}
	return cfg, nil
}

// Value 返回设置回显及导入导出共用的规范化对象。
func (cfg RouteTraceConfig) Value() *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(cfg.Enabled)},
		canonical.ObjectPair{Key: "retention_hours", Value: canonical.NewIntValue(int64(cfg.RetentionHours))},
	)
}
