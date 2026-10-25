package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// LegacyConfigPath 是旧版把配置写在当前工作目录时的文件名。
//
// 它是**相对路径**：参照实现只在进程工作目录下查找，不做用户目录回退。
const LegacyConfigPath = "router-config.json"

// ConfigPathEnv 是指定配置文件路径的环境变量。
const ConfigPathEnv = "AMKR_CONFIG"

// GenerateLocalAPIKey 生成本地 API key，格式 "amkr_" + 43 字符 base64url。
//
// 对齐 config.py:251：“f"amkr_{secrets.token_urlsafe(32)}"“。32 随机字节经
// urlsafe base64 去掉 padding 后固定 43 字符（已验证）。前缀与长度都属于对外
// 契约——部署脚本会按 amkr_ 前缀识别本地 key。
func GenerateLocalAPIKey() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	// base64.RawURLEncoding 与 token_urlsafe 一致：URL 安全字母表且无 '=' 填充。
	return "amkr_" + base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// GenerateWorkspaceKey 生成工作空间面板 key，格式 "amkr_ws_" + 43 字符 base64url。
//
// 与 GenerateLocalAPIKey 同一套随机源与编码，只换了前缀：多一段 "ws_" 让运维一眼
// 分得清「这是某个空间的面板 key」与「这是本地主 key」——两者权限差一个数量级，
// 混在一起看太危险。长度因此是 50 字符，也是对外契约（应用侧会把它整串存下来）。
func GenerateWorkspaceKey() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "amkr_ws_" + base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// GenerateInferenceKey 生成工作空间推理 key，格式 "amkr_ik_" + 43 字符 base64url。
//
// 与 GenerateWorkspaceKey 同一套随机源、同样长度，只换前缀。前缀在这里意义更重：
// 面板 key 会被贴进浏览器 URL，推理 key 会被写进各个项目的环境变量，两者在运维
// 眼里必须一眼可辨——否则一次误配就是把「只读面板」当成「能刷额度」的凭据发出去。
func GenerateInferenceKey() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "amkr_ik_" + base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// GenerateAccessKey 生成访问密钥，格式 "amkr_ak_" + 43 字符 base64url。
//
// 与 GenerateInferenceKey 同一套随机源与编码，只换前缀。访问密钥会被分发给实例外部
// 的使用者，因此前缀必须让它一眼可辨——把一把受限密钥误当成 local_api_key 来对待
// （或反过来）都是严重误配。
func GenerateAccessKey() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return AccessKeyPrefix + base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// EmptyConfigDict 返回新建配置的默认内容。
//
// 字段顺序与参照实现一致：它决定落盘文件的键序（保存时用 indent=2 且不排序），
// 因此顺序变化会让「新建配置」产生字节差异。
func EmptyConfigDict() (*canonical.Value, error) {
	localAPIKey, err := GenerateLocalAPIKey()
	if err != nil {
		return nil, err
	}
	capsPath, err := DefaultEndpointCapabilitiesPath()
	if err != nil {
		return nil, err
	}
	metricsPath, err := DefaultMetricsDBPath()
	if err != nil {
		return nil, err
	}
	logPath, err := DefaultLogFilePath()
	if err != nil {
		return nil, err
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "config_version", Value: canonical.NewIntValue(CONFIG_VERSION)},
		canonical.ObjectPair{Key: "host", Value: canonical.NewString("127.0.0.1")},
		canonical.ObjectPair{Key: "port", Value: canonical.NewIntValue(8000)},
		canonical.ObjectPair{Key: "default_base_url", Value: canonical.NewString("https://api.openai.com")},
		canonical.ObjectPair{Key: "upstream_routes", Value: canonical.NewObject()},
		canonical.ObjectPair{Key: "request_timeout", Value: canonical.NewIntValue(60)},
		canonical.ObjectPair{Key: "stream_first_byte_timeout", Value: canonical.NewIntValue(60)},
		canonical.ObjectPair{Key: "stream_idle_timeout", Value: canonical.NewIntValue(60)},
		canonical.ObjectPair{Key: "max_retries", Value: canonical.NewIntValue(2)},
		canonical.ObjectPair{Key: "key_failure_threshold", Value: canonical.NewIntValue(2)},
		canonical.ObjectPair{Key: "key_cooldown_seconds", Value: canonical.NewIntValue(60)},
		canonical.ObjectPair{Key: "key_balance_cooldown_seconds", Value: canonical.NewIntValue(300)},
		canonical.ObjectPair{Key: "quota_freeze_interval_seconds", Value: canonical.NewIntValue(60)},
		canonical.ObjectPair{Key: "endpoint_capabilities_path", Value: canonical.NewString(capsPath)},
		canonical.ObjectPair{Key: "metrics_db_path", Value: canonical.NewString(metricsPath)},
		canonical.ObjectPair{Key: "log_file_path", Value: canonical.NewString(logPath)},
		canonical.ObjectPair{Key: "local_api_key", Value: canonical.NewString(localAPIKey)},
		canonical.ObjectPair{Key: "webui_enabled", Value: canonical.NewBool(true)},
		canonical.ObjectPair{Key: "ops_enabled", Value: canonical.NewBool(true)},
		canonical.ObjectPair{Key: "providers", Value: canonical.NewObject()},
		canonical.ObjectPair{Key: "models", Value: canonical.NewObject()},
	), nil
}

// LoadConfigData 读取配置文件；文件不存在时写入默认配置并返回它。
//
// 对齐 config.py:279。注意「不存在就创建」是刻意行为：首次启动会在磁盘上留下
// 一份含随机 local_api_key 的配置，删掉文件再启动会换一把新 key。
func LoadConfigData(path string) (*canonical.Value, error) {
	content, err := os.ReadFile(path)
	if err == nil {
		value, parseErr := canonical.ParseString(string(content))
		if parseErr != nil {
			return nil, parseErr
		}
		return value, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	data, err := EmptyConfigDict()
	if err != nil {
		return nil, err
	}
	if err := SaveConfigData(path, data); err != nil {
		return nil, err
	}
	return data, nil
}

// SaveConfigData 原子写入配置：临时文件 + os.replace，带小退避重试。
//
// 对齐 config.py:287。两个细节都属于兼容面：
//   - 序列化用 indent=2 且**不排序**，所以字段顺序即写入顺序（这点与
//     canonical.Dumps 的排序路径不同，必须用 DumpsIndent）；
//   - 末尾补一个换行。
func SaveConfigData(path string, data *canonical.Value) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	suffix, err := randomHex(6)
	if err != nil {
		return err
	}
	temporaryPath := filepath.Join(filepath.Dir(path),
		fmt.Sprintf(".%s.%s.tmp", filepath.Base(path), suffix))

	// finally：无论成功失败都清理临时文件，避免在配置目录留下垃圾。
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
			// 清理失败不影响主流程结果（与参照实现一致）。
			_ = removeErr
		}
	}()

	content := canonical.DumpsIndent(data, 2) + "\n"
	if err := os.WriteFile(temporaryPath, []byte(translateNewlines(content)), 0o644); err != nil {
		return err
	}
	return replaceWithRetry(temporaryPath, path)
}

// translateNewlines 复刻 Python “Path.write_text“ 的换行转换。
//
// Python 以文本模式打开文件时 newline=None，写入会把 "\n" 翻译成 os.linesep：
// Windows 上是 "\r\n"，类 Unix 上是 "\n"。Go 的 os.WriteFile 始终写原始字节，
// 因此必须显式补上这一步，否则同一次保存会产生与 Python 不同的字节（Windows 下
// 每个换行少一个 \r），用户在两版之间切换时配置文件内容会来回抖动。
//
// 注意只翻译 JSON 文本**结构**上的换行：字符串内部真正的换行在 JSON 里是转义的
// 两个字符（反斜杠 + n），不匹配这里的 "\n"，因此不受影响，与 Python 一致。
func translateNewlines(content string) string {
	if runtime.GOOS != "windows" {
		return content
	}
	return strings.ReplaceAll(content, "\n", "\r\n")
}

// replaceWithRetry 重试 os.Rename，缓解 Windows 上杀软扫描或句柄未释放导致的
// 瞬时占用（对齐 config.py:300，4 次尝试、0.02s 起倍增）。
func replaceWithRetry(source, target string) error {
	delay := 20 * time.Millisecond
	const attempts = 4
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := os.Rename(source, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts-1 {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return lastErr
}

// ResolveConfigPath 决定实际使用的配置文件路径。
//
// 优先级（对齐 config.py:651）：显式 path > AMKR_CONFIG 环境变量 > 默认路径；
// 默认路径不存在但工作目录下有旧版 router-config.json 时，把它复制到默认位置。
func ResolveConfigPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	if envPath := os.Getenv(ConfigPathEnv); envPath != "" {
		return envPath, nil
	}
	defaultPath, err := DefaultConfigPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath, nil
	}
	legacyContent, err := os.ReadFile(LegacyConfigPath)
	if err != nil {
		// 旧文件不存在是正常情况：直接用默认路径（首次启动会创建）。
		return defaultPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(defaultPath, legacyContent, 0o644); err != nil {
		return "", err
	}
	return defaultPath, nil
}

// Load 解析配置文件并返回校验后的配置。
//
// 对齐 config.py:852：版本落后时把迁移结果写回磁盘（就地升级），因此首次用新版
// 本启动会改写配置文件——这是预期行为，升级前应备份。
func Load(path string) (*RouterConfig, error) {
	configPath, err := ResolveConfigPath(path)
	if err != nil {
		return nil, err
	}
	raw, err := LoadConfigData(configPath)
	if err != nil {
		return nil, err
	}
	version, err := configVersionOf(raw)
	if err != nil {
		return nil, err
	}
	if version != CONFIG_VERSION {
		migrated, err := MigrateConfigData(raw)
		if err != nil {
			return nil, err
		}
		if err := SaveConfigData(configPath, migrated); err != nil {
			return nil, err
		}
		raw = migrated
	}
	config, err := FromDict(raw)
	if err != nil {
		return nil, err
	}
	return config, nil
}

// randomHex 返回 n 字节的十六进制文本（长度 2n）。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ParseConfigText 解析配置文件文本，供测试与导入路径复用。
func ParseConfigText(text string) (*canonical.Value, error) {
	return canonical.ParseString(strings.TrimSpace(text))
}
