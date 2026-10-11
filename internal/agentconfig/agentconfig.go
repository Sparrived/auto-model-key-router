// 本文件承载 agent_config.py 的公开 API：路径推导、Configure / Rollback /
// GetStatus、备份状态与原子写入。包级说明与差异清单见 doc.go。

package agentconfig

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// AgentLegacyKey 是「旧实现写入过的单模型钉死键」及其应恢复的取值。
type AgentLegacyKey struct {
	Name  string
	Value string
}

// AgentLegacyPlan 描述旧实现（按 unified-model / native 模式写入）留下的单模型
// 钉死键在新一轮集成里该怎么处置。
//
// 判据只有一个：**集成前的原始内容**（备份里的 original_content，或首次集成时的
// 当前文件）。原始内容里没有这个键 ⇒ 它是 AMKR 自己写进去的，删除；原始内容里有
// ⇒ 按原值恢复——绝不因为「取消路由模式」就丢掉用户自己的取值。两份清单都按固定
// 键序，保证写出的文件逐字节可预期。
type AgentLegacyPlan struct {
	Restore []AgentLegacyKey
	Remove  []string
}

// claudeLegacyModelKeys 是旧实现按「统一模型」模式写进 Claude Code 的四个变量。
var claudeLegacyModelKeys = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
}

// codexLegacyModelKeys 是旧实现按「统一模型」模式写进 Codex 的三个键。
var codexLegacyModelKeys = []string{"model", "review_model", "model_reasoning_effort"}

// planClaudeLegacy 按 Claude Code 的 env 段计算遗留键处置计划。
func planClaudeLegacy(known bool, original *canonical.Value) AgentLegacyPlan {
	return planLegacyKeys(known, original, claudeLegacyModelKeys, func(doc *canonical.Value, key string) (string, bool) {
		return doc.Lookup("env").Lookup(key).StringValue(), doc.Lookup("env").Lookup(key) != nil
	})
}

// planCodexLegacy 按 Codex 的顶层键计算遗留键处置计划。
func planCodexLegacy(known bool, original *canonical.Value) AgentLegacyPlan {
	return planLegacyKeys(known, original, codexLegacyModelKeys, func(doc *canonical.Value, key string) (string, bool) {
		return doc.Lookup(key).StringValue(), doc.Lookup(key) != nil
	})
}

// planLegacyKeys 是两种 Agent 共用的判定：快照里有的恢复、没有的删除、不知道的
// （备份损坏 / 原始内容不是合法 JSON）什么都不动。
func planLegacyKeys(
	known bool,
	original *canonical.Value,
	keys []string,
	read func(doc *canonical.Value, key string) (string, bool),
) AgentLegacyPlan {
	if !known || original == nil || !original.IsObject() {
		return AgentLegacyPlan{}
	}
	plan := AgentLegacyPlan{}
	for _, name := range keys {
		if value, present := read(original, name); present {
			plan.Restore = append(plan.Restore, AgentLegacyKey{Name: name, Value: value})
			continue
		}
		plan.Remove = append(plan.Remove, name)
	}
	return plan
}

// legacyOriginalDoc 解析「集成前的原始内容」，供遗留键判定使用。
//
// 返回 (文档, 是否知道原始内容)：备份内容损坏或原始内容不是合法 JSON 对象时
// known 为 false，调用方据此**什么都不动**（保守优先于猜测）。
func legacyOriginalDoc(known bool, content []byte) (*canonical.Value, bool) {
	if !known {
		return nil, false
	}
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		// 集成前文件不存在：所有遗留键都是 AMKR 写进去的。
		return canonical.NewObject(), true
	}
	parsed, err := canonical.Parse(trimmed)
	if err != nil || !parsed.IsObject() {
		return nil, false
	}
	return parsed, true
}

// 支持的 Agent 标识（agent_config.py:24-27）。
const (
	// ClaudeCode 是 Claude Code CLI。
	ClaudeCode = "claude-code"
	// Codex 是 OpenAI Codex CLI。
	Codex = "codex"
	// PiAgent 是 Pi agent。
	PiAgent = "pi-agent"
)

// 路由模式（native / unified-model）已取消：集成一律把 AMKR 当前可用的全部模型
// 注入客户端，由用户在客户端自己的模型选择器里切换。参照实现里的
// SUPPORTED_AGENT_MODES（agent_config.py:28-30）因此在 Go 侧没有对应常量。

const (
	// codexProviderID 是 AMKR 在 Codex 配置里占用的 provider 名
	// （agent_config.py:31 的 CODEX_PROVIDER_ID）。注意大小写：它与用户自带的
	// `[model_providers.openai]` 是**两个不同的表**，参照实现刻意不覆盖后者。
	codexProviderID = "OpenAI"
	// codexCatalogFileName 是 AMKR 写在 ~/.codex/ 下的模型目录文件名。
	//
	// Codex 的 config.toml 用 model_catalog_json 指向它，模型选择器从这里读取
	// AMKR 当前可用的全部模型；它由 AMKR 独占，备份与回退都和 auth.json 一起处理。
	codexCatalogFileName = "amkr-models.json"
	// piProviderID 是 AMKR 在 Pi agent 配置里占用的 provider 名。
	piProviderID = "amkr"
	// piContextWindow 是写进 Pi agent 的上下文窗口（agent_config.py:33）。
	piContextWindow = 262144
	// backupStateVersion 是备份文件格式版本（agent_config.py:170）。
	//
	// 3 = 不再记录 `mode`：集成不再有路由模式，一律注入全部可用模型。
	backupStateVersion = 3
	// backupDirName 是默认备份目录名（agent_config.py:83）。
	backupDirName = "agent-config-backups"
)

// ConfigError 是 Agent 配置操作失败的错误，对应 agent_config.py:36 的
// AgentConfigError。错误文本会经管理接口回给用户，属对外契约。
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

// PyErrorName 返回参照实现里该异常的类名，供上层复刻
// `f"{type(exc).__name__}: {exc}"`（ops_api.py:265）。
//
// 没有它时上层只能退化成 Go 的 `%T`，那会给出 "*agentconfig.ConfigError" 这种带包路径的
// 形式，与参照实现的 `AgentConfigError: ...` 不同。internal/api 的接缝按这个可选接口取值
// （见其 pyErrorNamer）。
func (e *ConfigError) PyErrorName() string { return "AgentConfigError" }

// errf 构造 ConfigError。
func errf(format string, args ...any) error {
	return &ConfigError{Message: fmt.Sprintf(format, args...)}
}

// DisplayName 返回 Agent 的展示名（agent_config.py:61）。
func DisplayName(agent string) string {
	switch agent {
	case ClaudeCode:
		return "Claude Code"
	case Codex:
		return "Codex"
	case PiAgent:
		return "Pi Agent"
	}
	return agent
}

// Options 是 Configure / Rollback / GetStatus 的可注入参数。
//
// BaseDir 替代参照实现里的 Path.home()：所有默认路径都由它推导。测试必须把它
// 指向临时目录，否则会写进开发者真实的用户配置目录。
//
// TargetPath / BackupPath 对应参照实现里的同名可选参数；为空时按 Agent 推导。
// BackupDir 对应 agent_backup_path 的 backup_dir 参数，为空时用平台缓存目录。
type Options struct {
	BaseDir    string
	TargetPath string
	BackupPath string
	BackupDir  string
}

// Status 对应 agent_config.py:40 的 AgentConfigStatus。
//
// 取消路由模式后不再有 Mode 字段：「当前配置是否就是 AMKR 写入的那份」只由
// CurrentIsApplied 表达。
type Status struct {
	Agent            string
	TargetPath       string
	BackupPath       string
	BackupAvailable  bool
	CurrentIsApplied bool
}

// Result 对应 agent_config.py:50 的 AgentConfigResult。
type Result struct {
	Agent      string
	TargetPath string
	BackupPath string
	// RouterURL 是写入 Agent 的 AMKR 地址；回退时为空串（agent_config.py:248）。
	RouterURL string
	// ExtraTargetPaths 是除主目标外一并改写的文件（Codex 的 auth.json 与模型目录）。
	ExtraTargetPaths []string
	Restored         bool
}

// TargetPath 按 Agent 推导目标配置文件路径（agent_config.py:69）。
//
// 环境变量优先级与参照实现一致：CLAUDE_CONFIG_DIR / PI_CODING_AGENT_DIR /
// CODEX_HOME 覆盖各自的默认目录。
func TargetPath(baseDir, agent string) (string, error) {
	if err := validateAgent(agent); err != nil {
		return "", err
	}
	// 空 baseDir 必须解析成**用户主目录**，对应参照实现的 Path.home()。
	//
	// 这一条不能省：filepath.Join("", ".claude") 得到的是相对路径 ".claude"，会落到进程的
	// **当前工作目录**——也就是集成接口会去写 ./.claude/settings.json，而不是 ~/.claude/。
	// 参照实现没有这个歧义（Path.home() 永远给出绝对路径），所以这里要么给出主目录，要么
	// 显式报错，绝不能让空串静默变成相对路径。
	resolvedBase := baseDir
	if resolvedBase == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errf("无法确定用户主目录: %v", err)
		}
		resolvedBase = home
	}
	switch agent {
	case ClaudeCode:
		dir := configDirFor("CLAUDE_CONFIG_DIR", resolvedBase, ".claude")
		return filepath.Join(dir, "settings.json"), nil
	case PiAgent:
		dir := configDirFor("PI_CODING_AGENT_DIR", resolvedBase, ".pi", "agent")
		return filepath.Join(dir, "models.json"), nil
	default:
		dir := configDirFor("CODEX_HOME", resolvedBase, ".codex")
		return filepath.Join(dir, "config.toml"), nil
	}
}

// configDirFor 解析「环境变量覆盖，否则 baseDir 下的默认子目录」。
func configDirFor(envName, baseDir string, parts ...string) string {
	if override := os.Getenv(envName); override != "" {
		return expandUser(baseDir, override)
	}
	return filepath.Join(append([]string{baseDir}, parts...)...)
}

// BackupPath 推导备份文件路径（agent_config.py:81）。
func BackupPath(opts Options, agent string) (string, error) {
	if err := validateAgent(agent); err != nil {
		return "", err
	}
	root := opts.BackupDir
	if root == "" {
		cacheDir, err := config.DefaultCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cacheDir, backupDirName)
	}
	return filepath.Join(root, agent+".json"), nil
}

// RouterOrigin 返回 Agent 应指向的 AMKR 根地址（agent_config.py:87）。
//
// 通配地址会被换成 127.0.0.1：Agent 不能连 0.0.0.0。IPv6 字面量要加方括号，
// 否则拼出来的 URL 无法解析。
func RouterOrigin(cfg *config.RouterConfig) string {
	host := cfg.Host
	switch host {
	case "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Port)
}

// GetStatus 返回 Agent 配置的当前状态（agent_config.py:94）。
func GetStatus(agent string, opts Options) (Status, error) {
	target, backup, err := resolveTargets(agent, opts)
	if err != nil {
		return Status{}, err
	}
	state := loadBackupState(backup)
	backupAvailable := state != nil &&
		state.str("agent") == agent &&
		state.str("target_path") == target
	currentIsApplied := false
	if backupAvailable {
		if content, readErr := os.ReadFile(target); readErr == nil {
			currentIsApplied = sha256Hex(content) == state.str("applied_sha256") &&
				extraTargetsAreApplied(state)
		}
	}
	return Status{
		Agent:            agent,
		TargetPath:       target,
		BackupPath:       backup,
		BackupAvailable:  backupAvailable,
		CurrentIsApplied: currentIsApplied,
	}, nil
}

// Configure 把 Agent 接到本机 AMKR，并备份改写前的内容（agent_config.py:114）。
//
// 没有「路由模式」参数：三个客户端一律写入 AMKR 当前可用的全部模型（模型 ID、别名，
// 以及配置了统一模型时的 unified-model），用户在客户端自己的模型选择器里切换。
func Configure(agent string, cfg *config.RouterConfig, opts Options) (Result, error) {
	if err := validateAgent(agent); err != nil {
		return Result{}, err
	}
	if cfg.LocalAPIKey == "" {
		return Result{}, errf("本地鉴权 key 为空，无法配置 Agent")
	}
	// 没有任何可用模型时直接拒绝：集成的内容就是模型清单，空清单在各客户端上要么
	// 是空选择器（Claude Code）、要么是**启动失败**（Codex 的目录文件不允许为空），
	// 写出去只会让用户更难看出问题所在。
	if len(availableModelNames(cfg)) == 0 {
		return Result{}, errf("没有可用的模型，无法配置 %s：请先在模型路由里配置至少一个带启用 Key 的模型", DisplayName(agent))
	}

	target, backup, err := resolveTargets(agent, opts)
	if err != nil {
		return Result{}, err
	}
	current, err := readFileOrEmpty(target)
	if err != nil {
		return Result{}, err
	}
	state := loadBackupState(backup)

	// 备份里记录的就是「当前这份被 AMKR 写入的内容」时，沿用更早的原始快照，
	// 否则用户已经手工改过配置，必须以当前内容为新快照
	// （agent_config.py:136-148）。
	preserveExistingBackup := state != nil &&
		state.str("agent") == agent &&
		state.str("target_path") == target &&
		state.str("applied_sha256") == sha256Hex(current) &&
		extraTargetsAreApplied(state)
	var originalExists bool
	var originalContent string
	if preserveExistingBackup {
		originalExists = state.truthy("original_exists")
		originalContent = state.str("original_content")
	} else {
		originalExists = fileExists(target)
		originalContent = base64.StdEncoding.EncodeToString(current)
	}

	// 「集成前」的原始内容：遗留单模型键判定的唯一依据（见 AgentLegacyPlan）。
	// 备份里的原始内容损坏时 legacyKnown 为 false，两个计划都空——保守不动用户数据。
	legacyKnown := true
	legacyContent := current
	if preserveExistingBackup {
		if decoded, decodeErr := decodeBackupContent(state); decodeErr == nil {
			legacyContent = decoded
		} else {
			legacyKnown = false
		}
	}
	legacyDoc, legacyKnown := legacyOriginalDoc(legacyKnown, legacyContent)
	claudeLegacy := planClaudeLegacy(legacyKnown, legacyDoc)
	codexLegacy := planCodexLegacy(legacyKnown, legacyDoc)
	var updated []byte
	var routeURL string
	var extraTargets []extraTarget
	switch agent {
	case ClaudeCode:
		if updated, err = configureClaudeCode(current, cfg, claudeLegacy); err != nil {
			return Result{}, err
		}
		routeURL = RouterOrigin(cfg)
	case PiAgent:
		if updated, err = configurePiAgent(current, cfg); err != nil {
			return Result{}, err
		}
		routeURL = RouterOrigin(cfg) + "/v1"
	default:
		// Codex 的模型清单落在单独一份目录文件里（config.toml 的
		// model_catalog_json 指向它）。它和 auth.json 一样是「附加目标」：一起
		// 备份、一起回退，绝不写进客户端会自行刷新的缓存文件。
		catalogTarget := filepath.Join(filepath.Dir(target), codexCatalogFileName)
		if updated, err = configureCodex(current, cfg, catalogTarget, codexLegacy); err != nil {
			return Result{}, err
		}
		authTarget := filepath.Join(filepath.Dir(target), "auth.json")
		currentAuth, err := readFileOrEmpty(authTarget)
		if err != nil {
			return Result{}, err
		}
		authContent, err := configureCodexAuth(currentAuth, cfg)
		if err != nil {
			return Result{}, err
		}
		extraTargets = []extraTarget{
			{Path: authTarget, Content: authContent},
			{Path: catalogTarget, Content: codexCatalogJSON(cfg)},
		}
		routeURL = RouterOrigin(cfg) + "/v1"
	}

	extraBackup := []extraBackupEntry(nil)
	if len(extraTargets) > 0 {
		if extraBackup, err = extraBackupEntries(extraTargets, preserveExistingBackup, state); err != nil {
			return Result{}, err
		}
	}
	previousBackup, previousBackupExists, err := readBackupBytes(backup)
	if err != nil {
		return Result{}, err
	}
	snapshots := fileSnapshots(append([]string{target}, extraTargetPaths(extraTargets)...))
	if err := writeAtomic(backup, newBackupBytes(agent, target, originalExists, originalContent, sha256Hex(updated), extraBackup)); err != nil {
		return Result{}, err
	}
	if err := writeOutputs(target, updated, extraTargets); err != nil {
		// 任一步写失败就整体回滚到改写前的快照，并还原备份文件
		// （agent_config.py:192-198）。
		restoreFileSnapshots(snapshots)
		if !previousBackupExists {
			_ = os.Remove(backup)
		} else if writeErr := writeAtomic(backup, previousBackup); writeErr != nil {
			return Result{}, writeErr
		}
		return Result{}, err
	}
	return Result{
		Agent:            agent,
		TargetPath:       target,
		BackupPath:       backup,
		RouterURL:        routeURL,
		ExtraTargetPaths: extraTargetPaths(extraTargets),
	}, nil
}

// Rollback 用备份还原 Agent 配置并删除备份（agent_config.py:209）。
func Rollback(agent string, opts Options) (Result, error) {
	if err := validateAgent(agent); err != nil {
		return Result{}, err
	}
	backup, err := resolveBackupPath(agent, opts)
	if err != nil {
		return Result{}, err
	}
	state := loadBackupState(backup)
	if state == nil || state.str("agent") != agent {
		return Result{}, errf("没有可用于回退的 %s 配置", DisplayName(agent))
	}

	storedTargetText := state.str("target_path")
	if storedTargetText == "" {
		return Result{}, errf("Agent 配置备份缺少目标路径")
	}
	storedTarget, err := resolvePath(opts.BaseDir, storedTargetText)
	if err != nil {
		return Result{}, err
	}
	target := storedTarget
	if opts.TargetPath != "" {
		if target, err = resolvePath(opts.BaseDir, opts.TargetPath); err != nil {
			return Result{}, err
		}
	}
	if target != storedTarget {
		return Result{}, errf("备份对应的配置路径与当前目标路径不一致")
	}

	type restore struct {
		path    string
		exists  bool
		content []byte
	}
	mainContent, err := decodeBackupContent(state)
	if err != nil {
		return Result{}, err
	}
	restores := []restore{{path: target, exists: state.truthy("original_exists"), content: mainContent}}
	for _, extra := range extraBackupStates(state) {
		extraTargetText := extra.str("target_path")
		if extraTargetText == "" {
			return Result{}, errf("Agent 配置备份缺少附加目标路径")
		}
		extraPath, err := resolvePath(opts.BaseDir, extraTargetText)
		if err != nil {
			return Result{}, err
		}
		content, err := decodeBackupContent(extra)
		if err != nil {
			return Result{}, err
		}
		restores = append(restores, restore{path: extraPath, exists: extra.truthy("original_exists"), content: content})
	}

	for _, item := range restores {
		if item.exists {
			if err := writeAtomic(item.path, item.content); err != nil {
				return Result{}, err
			}
			continue
		}
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			return Result{}, err
		}
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	extraPaths := make([]string, 0, len(restores)-1)
	for _, item := range restores[1:] {
		extraPaths = append(extraPaths, item.path)
	}
	return Result{
		Agent:            agent,
		TargetPath:       target,
		BackupPath:       backup,
		RouterURL:        "",
		ExtraTargetPaths: extraPaths,
		Restored:         true,
	}, nil
}

// resolveTargets 解析 (目标文件, 备份文件) 的绝对路径。
func resolveTargets(agent string, opts Options) (string, string, error) {
	targetSource := opts.TargetPath
	if targetSource == "" {
		var err error
		if targetSource, err = TargetPath(opts.BaseDir, agent); err != nil {
			return "", "", err
		}
	}
	target, err := resolvePath(opts.BaseDir, targetSource)
	if err != nil {
		return "", "", err
	}
	backup, err := resolveBackupPath(agent, opts)
	if err != nil {
		return "", "", err
	}
	return target, backup, nil
}

// resolveBackupPath 解析备份文件路径。
func resolveBackupPath(agent string, opts Options) (string, error) {
	source := opts.BackupPath
	if source == "" {
		var err error
		if source, err = BackupPath(opts, agent); err != nil {
			return "", err
		}
	}
	return resolvePath(opts.BaseDir, source)
}

// resolvePath 复刻 agent_config.py:526 的 `path.expanduser().resolve(strict=False)`。
//
// 用 filepath.Abs + Clean 代替 Path.resolve：对不存在的路径两者都只做词法规范化，
// 差别只在符号链接解析，而 AMKR 写入的三个目标都不应是符号链接。
func resolvePath(baseDir, path string) (string, error) {
	absolute, err := filepath.Abs(expandUser(baseDir, path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

// expandUser 复刻 Python 的 os.path.expanduser，但把 home 换成注入的 baseDir。
func expandUser(baseDir, path string) string {
	if path == "~" {
		return baseDir
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(baseDir, path[2:])
	}
	return path
}

// validateAgent 校验 Agent 标识（agent_config.py:534）。
func validateAgent(agent string) error {
	switch agent {
	case ClaudeCode, Codex, PiAgent:
		return nil
	}
	return errf("不支持的 Agent: %s", agent)
}

// validateMode 已随「路由模式」一并移除：集成不再有 native / unified-model 之分，
// 一律注入全部可用模型。旧备份里的 mode 字段（v1/v2）现在只是一个被忽略的键。

// extraTarget 是一个需要一并改写的附加文件。
type extraTarget struct {
	Path    string
	Content []byte
}

// extraTargetPaths 取出附加文件路径。
func extraTargetPaths(targets []extraTarget) []string {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	return paths
}

// writeOutputs 先写主目标，再写附加目标；任一步失败都返回错误，由调用方回滚。
func writeOutputs(target string, content []byte, extras []extraTarget) error {
	if err := writeAtomic(target, content); err != nil {
		return err
	}
	for _, extra := range extras {
		if err := writeAtomic(extra.Path, extra.Content); err != nil {
			return err
		}
	}
	return nil
}

// fileSnapshots 记录若干文件的当前内容，用于失败回滚（agent_config.py:477）。
func fileSnapshots(paths []string) []fileSnapshot {
	snapshots := make([]fileSnapshot, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			snapshots = append(snapshots, fileSnapshot{Path: path, Exists: false})
			continue
		}
		snapshots = append(snapshots, fileSnapshot{Path: path, Exists: true, Content: content})
	}
	return snapshots
}

// fileSnapshot 是单个文件的改写前快照。
type fileSnapshot struct {
	Path    string
	Exists  bool
	Content []byte
}

// restoreFileSnapshots 还原快照（agent_config.py:485）。
func restoreFileSnapshots(snapshots []fileSnapshot) {
	for _, snapshot := range snapshots {
		if snapshot.Exists {
			_ = writeAtomic(snapshot.Path, snapshot.Content)
			continue
		}
		_ = os.Remove(snapshot.Path)
	}
}

// readFileOrEmpty 读文件；不存在时返回空内容（对应 Python 的 `if target.exists()`）。
func readFileOrEmpty(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return content, nil
}

// readBackupBytes 读备份文件原始字节；不存在时 ok 为 false。
func readBackupBytes(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return content, true, nil
}

// fileExists 报告路径是否存在（Python 的 Path.exists()，目录也算存在）。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sha256Hex 返回小写十六进制 sha256（agent_config.py:530）。
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// writeAtomic 原子写文件：临时文件 + 带重试的 rename（agent_config.py:513）。
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	suffix, err := randomHex(6)
	if err != nil {
		return err
	}
	temporary := filepath.Join(dir, fmt.Sprintf(".%s.%s.tmp", filepath.Base(path), suffix))
	defer func() {
		// finally：无论成功失败都清理临时文件。
		_ = os.Remove(temporary)
	}()
	if err := os.WriteFile(temporary, content, 0o666); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporary, 0o600); err != nil {
			return err
		}
	}
	return replaceWithRetry(temporary, path)
}

// replaceWithRetry 重试 os.Rename，缓解 Windows 上杀软扫描或句柄未释放导致的
// 瞬时占用（对齐 config.py:300：4 次尝试、0.02s 起倍增）。
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

// randomHex 返回 n 字节的十六进制文本（对应 secrets.token_hex）。
func randomHex(n int) (string, error) {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// ────────────────────────────────── 备份状态 ────────────────────────────────

// backupState 是备份 JSON 的只读视图。
//
// 参照实现直接操作 dict 并混用 `state.get(k)`（缺省 None）与 `state[k]`
// （缺省 KeyError），这里统一成「缺失即零值」：两者只在备份被人为损坏时才有
// 差异，而那属于 doc.go 里记录的可接受差异。
type backupState struct {
	raw *canonical.Value
}

// str 对应 Python 的 `str(state.get(key) or "")`。
//
// 用 canonical 的 StringValue（即 `x or ""` 再 str()）而不是 PyStr：参照实现里
// 全部字符串字段要么走 `str(x or "")`，要么与一个非空字符串比较，两者在
// 「缺失即空串」的语义下结果一致（详见 StringValue 的说明）。
func (s *backupState) str(key string) string {
	if s == nil || s.raw == nil {
		return ""
	}
	return s.raw.Lookup(key).StringValue()
}

// truthy 对应 Python 的 `bool(state.get(key))`。
func (s *backupState) truthy(key string) bool {
	if s == nil || s.raw == nil {
		return false
	}
	return s.raw.Lookup(key).Truthy()
}

// isString 对应 Python 的 `isinstance(value, str)`。
func (s *backupState) isString(key string) bool {
	if s == nil || s.raw == nil {
		return false
	}
	return s.raw.Lookup(key).IsString()
}

// loadBackupState 读取并解析备份文件（agent_config.py:494）。
//
// 文件不存在、读失败、JSON 非法、根节点不是对象时一律返回 nil（参照实现
// 把这些情况全部折叠成 None，见 _load_backup_state 的 except (OSError,
// JSONDecodeError)）。
func loadBackupState(path string) *backupState {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, parseErr := canonical.Parse(content)
	if parseErr != nil {
		return nil
	}
	if !value.IsObject() {
		return nil
	}
	return &backupState{raw: value}
}

// backupMode 已随「路由模式」一并移除。旧备份（v1/v2）里的 mode 字段不再被读取：
// 集成写出的内容与模式无关，回退只需要 original_content。

// extraBackupStates 取出备份里的附加目标条目（agent_config.py:413）。
func extraBackupStates(state *backupState) []*backupState {
	if state == nil || state.raw == nil {
		return nil
	}
	raw := state.raw.Lookup("extra_targets")
	if !raw.IsArray() {
		return nil
	}
	var out []*backupState
	for _, item := range raw.Items() {
		if item.IsObject() {
			out = append(out, &backupState{raw: item})
		}
	}
	return out
}

// extraTargetsAreApplied 判断附加目标当前是否仍是 AMKR 写入的那份
// （agent_config.py:421）。state 为 nil 时返回 true。
func extraTargetsAreApplied(state *backupState) bool {
	if state == nil {
		return true
	}
	for _, extra := range extraBackupStates(state) {
		targetText := extra.str("target_path")
		if targetText == "" || !extra.isString("applied_sha256") {
			return false
		}
		target, err := resolvePath("", targetText)
		if err != nil {
			return false
		}
		content, readErr := os.ReadFile(target)
		if readErr != nil {
			return false
		}
		if sha256Hex(content) != extra.str("applied_sha256") {
			return false
		}
	}
	return true
}

// extraBackupEntry 是备份里额外目标的一条记录。
type extraBackupEntry struct {
	TargetPath      string
	OriginalExists  bool
	OriginalContent string
	AppliedSHA256   string
}

// extraBackupEntries 为附加目标生成备份记录（agent_config.py:436）。
//
// 沿用旧备份的原始快照有两种情况：备份本身仍是「当前文件即 AMKR 写入」，
// 且该附加目标记录的 applied_sha256 与磁盘现状一致；否则以磁盘现状为新快照。
func extraBackupEntries(targets []extraTarget, preserveExistingBackup bool, state *backupState) ([]extraBackupEntry, error) {
	existing := map[string]*backupState{}
	for _, item := range extraBackupStates(state) {
		targetText := item.str("target_path")
		if targetText == "" {
			continue
		}
		resolved, err := resolvePath("", targetText)
		if err != nil {
			continue
		}
		existing[resolved] = item
	}
	entries := make([]extraBackupEntry, 0, len(targets))
	for _, target := range targets {
		current, err := readFileOrEmpty(target.Path)
		if err != nil {
			return nil, err
		}
		var stored *backupState
		if preserveExistingBackup {
			stored = existing[target.Path]
		}
		entry := extraBackupEntry{
			TargetPath:    target.Path,
			AppliedSHA256: sha256Hex(target.Content),
		}
		if stored != nil && stored.str("applied_sha256") == sha256Hex(current) {
			entry.OriginalExists = stored.truthy("original_exists")
			entry.OriginalContent = stored.str("original_content")
		} else {
			entry.OriginalExists = fileExists(target.Path)
			entry.OriginalContent = base64.StdEncoding.EncodeToString(current)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// decodeBackupContent 解码备份里的原始内容（agent_config.py:469）。
func decodeBackupContent(state *backupState) ([]byte, error) {
	content, err := base64.StdEncoding.DecodeString(state.str("original_content"))
	if err != nil {
		return nil, errf("Agent 配置备份内容已损坏")
	}
	return content, nil
}
