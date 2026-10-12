package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// stringOr 复刻 Python 的 “str(raw.get(key, default))“。
//
// 键缺失时返回默认值；键存在时一律渲染成字符串（None 变 "None"）。注意与
// stringOrFallback 不同：这里不做空值回落。
func stringOr(value *canonical.Value, fallback string) string {
	if value == nil {
		return fallback
	}
	return value.PyStr()
}

// boolOr 复刻 Python 的 “bool(raw.get(key, default))“。
func boolOr(value *canonical.Value, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return value.Truthy()
}

// intOr 复刻 Python 的 “int(raw.get(key, default))“。
//
// 成功路径覆盖 int(8080)、int(8080.0)、int("45")、int(True)；失败时按契约地位
// 分流：ValueError（如 int("soon")）的文本会经 management API 回给用户，属对外
// 契约，须逐字复刻；TypeError 只要求失败关闭。
func intOr(value *canonical.Value, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	parsed, err := canonical.ToInt(value)
	if err != nil {
		var valueErr *canonical.ValueError
		if errors.As(err, &valueErr) {
			return 0, &ConfigError{Message: valueErr.Message}
		}
		return 0, errInternal("%s", err.Error())
	}
	return int(parsed), nil
}

// maxConcurrencyOr 解析一个并发上限。
//
// 缺省、null 与 0 都表示「不限制」：null 是接口「清除上限」的落盘形态之一，
// 必须与缺省同等对待，否则一份被接口写回 null 的配置会当场加载失败。
// 负数在解析阶段就拒绝：静默夹到 0 等于把一个写错的上限变成「不限制」，
// 那是这个功能最不该出错的方向。
//
// label 是出错时用来定位的完整描述（如「供应商 openai 的 Key main」）。
func maxConcurrencyOr(value *canonical.Value, label string) (int, error) {
	if value == nil || value.IsNull() {
		return 0, nil
	}
	limit, err := intOr(value, 0)
	if err != nil {
		return 0, err
	}
	if limit < 0 {
		return 0, errf("%s 的 max_concurrency 不能为负数", label)
	}
	return limit, nil
}

// floatOr 复刻 Python 的 “float(raw.get(key, default))“。
func floatOr(value *canonical.Value, fallback float64) (float64, error) {
	if value == nil {
		return fallback, nil
	}
	parsed, err := canonical.ToFloat(value)
	if err != nil {
		var valueErr *canonical.ValueError
		if errors.As(err, &valueErr) {
			return 0, &ConfigError{Message: valueErr.Message}
		}
		return 0, errInternal("%s", err.Error())
	}
	return parsed, nil
}

// pathOr 复刻 “str(raw.get(primary) or raw.get(secondary) or fallback)“。
//
// 空字符串、None、0 等假值都会继续回落到下一个候选。
func pathOr(raw *canonical.Value, primary, secondary, fallback string) string {
	if value := raw.Lookup(primary); value.Truthy() {
		return value.PyStr()
	}
	if secondary != "" {
		if value := raw.Lookup(secondary); value.Truthy() {
			return value.PyStr()
		}
	}
	return fallback
}

// itoa 是 strconv.Itoa 的短别名。
func itoa(value int) string { return strconv.Itoa(value) }

// DefaultCacheDir 返回平台的缓存目录。
//
// 对齐 config.py:19：三平台各有一套约定，且 Windows 优先读 LOCALAPPDATA。
// 目录名的大小写与拼写都属于兼容面的一部分——它决定既有用户的
// metrics.sqlite3 与 endpoint-capabilities.json 放在哪里。
func DefaultCacheDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = os.Getenv("APPDATA")
		}
		if root != "" {
			return filepath.Join(root, "AutoModelKeyRouter"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "AppData", "Local", "AutoModelKeyRouter"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Caches", "AutoModelKeyRouter"), nil
	default:
		if root := os.Getenv("XDG_CACHE_HOME"); root != "" {
			return filepath.Join(root, "auto-model-key-router"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache", "auto-model-key-router"), nil
	}
}

// DefaultConfigPath 返回默认配置文件路径。
func DefaultConfigPath() (string, error) {
	cacheDir, err := DefaultCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "router-config.json"), nil
}

// DefaultMetricsDBPath 返回默认指标库路径。
func DefaultMetricsDBPath() (string, error) {
	cacheDir, err := DefaultCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "metrics.sqlite3"), nil
}

// DefaultEndpointCapabilitiesPath 返回默认探测缓存路径。
func DefaultEndpointCapabilitiesPath() (string, error) {
	cacheDir, err := DefaultCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "endpoint-capabilities.json"), nil
}

// DefaultLogFilePath 返回默认日志路径。
func DefaultLogFilePath() (string, error) {
	cacheDir, err := DefaultCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "server.log"), nil
}
