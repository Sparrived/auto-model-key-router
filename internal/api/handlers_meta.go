package api

import (
	"net/http"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// run 执行 handler 主体并把结果写成 JSON（status 为 204 时不写实体）。
//
// 所有 handler 都走这一条出口，保证错误映射只有一处（writeError），不会出现
// 「某个分支忘了映射状态码」的漏网之鱼。
func (s *Server) run(w http.ResponseWriter, status int, fn func() (*canonical.Value, error)) {
	s.runStatus(w, func() (int, *canonical.Value, error) {
		body, err := fn()
		return status, body, err
	})
}

// runStatus 是 run 的「状态码由主体决定」版本。
//
// 只有 PUT /api/routes/{id} 需要它：同样的请求可能得到 200（路由被改写）或
// 204（targets 被清空，路由随之被删掉），状态码依赖主体执行到什么结果。
func (s *Server) runStatus(w http.ResponseWriter, fn func() (int, *canonical.Value, error)) {
	status, body, err := fn()
	if err != nil {
		writeError(w, err)
		return
	}
	if status == http.StatusNoContent {
		writeNoContent(w)
		return
	}
	writeJSON(w, status, body)
}

// optString 取可选字符串字段：缺失或显式 null 都返回 nil。
//
// 对应 Python 的 `fields.get(name)`——它对「键不存在」与「值为 None」一视同仁。
func optString(obj *canonical.Value, key string) *string {
	if obj == nil {
		return nil
	}
	value, present := obj.LookupOK(key)
	if !present || value == nil || value.Kind == canonical.KindNull {
		return nil
	}
	text, ok := value.AsString()
	if !ok {
		return nil
	}
	return &text
}

// hasKey 对应 Python 的 `"name" in fields`。
func hasKey(obj *canonical.Value, key string) bool {
	if obj == nil {
		return false
	}
	_, present := obj.LookupOK(key)
	return present
}

// nonNull 对应 `fields.get(name) is not None`。
func nonNull(obj *canonical.Value, key string) bool {
	if obj == nil {
		return false
	}
	value, present := obj.LookupOK(key)
	return present && value != nil && value.Kind != canonical.KindNull
}

// optStringSlice 取可选字符串数组：缺失/null 返回 nil，空数组返回空切片。
//
// nil 与空切片的区别是**有意义的**：configops 用 nil 表示「不更新该字段」，用空
// 切片表示「更新成空」。Pydantic 的 exclude_unset 正好给出同样的区分。
func optStringSlice(obj *canonical.Value, key string) []string {
	if obj == nil {
		return nil
	}
	value, present := obj.LookupOK(key)
	if !present || value == nil || value.Kind == canonical.KindNull || !value.IsArray() {
		return nil
	}
	out := make([]string, 0, len(value.Arr))
	for _, item := range value.Arr {
		text, _ := item.AsString()
		out = append(out, text)
	}
	return out
}

// trailingStringSlice 取数组字段，缺失时返回空切片（对应 `list(x or [])`）。
func trailingStringSlice(obj *canonical.Value, key string) []string {
	values := optStringSlice(obj, key)
	if values == nil {
		return []string{}
	}
	return values
}

// clearableString 取一个「可清空」的可选字符串字段，供 TaskUpdate 使用。
//
// 三种情况要区分开，optString 单独做不到：
//   - 键不存在        -> nil，表示不改这个字段；
//   - 键存在且为 null -> 指向空串，表示清空该字段；
//   - 键存在且有值    -> 指向该值。
func clearableString(obj *canonical.Value, key string) *string {
	if !hasKey(obj, key) {
		return nil
	}
	if value, present := obj.LookupOK(key); present && value != nil && value.Kind == canonical.KindNull {
		empty := ""
		return &empty
	}
	return optString(obj, key)
}

// findConfigModel 按**精确 id** 查找模型。
//
// 刻意匹配别名：参照实现的 _find_model 只比 model.id，因此 GET /api/models/alias-a
// 是 404 而不是命中别名（这是参照实现的历史行为，刻意保留）。它旁边那个会匹配别名的
// find_config_model 在 management_api.py 里根本没有被任何路由调用。
func findConfigModel(cfg *config.RouterConfig, modelID string) (*config.ModelConfig, error) {
	for i := range cfg.Models {
		if cfg.Models[i].ID == modelID {
			return &cfg.Models[i], nil
		}
	}
	return nil, httpErrorf(404, "模型不存在: %s", modelID)
}

// findConfigKey 对应 management_api.py:1579 的 _find_key。
func findConfigKey(model *config.ModelConfig, keyName string) (*config.KeyConfig, error) {
	for i := range model.Keys {
		if model.Keys[i].Name == keyName {
			return &model.Keys[i], nil
		}
	}
	return nil, httpErrorf(404, "模型 %s 的 key 不存在: %s", model.ID, keyName)
}

// findConfigTask 对应 management_api.py:1433 的 _find_task。
func findConfigTask(cfg *config.RouterConfig, workspace, taskName string) (*config.TaskConfig, error) {
	for i := range cfg.Tasks {
		if cfg.Tasks[i].Name == taskName && cfg.Tasks[i].Workspace == workspace {
			return &cfg.Tasks[i], nil
		}
	}
	return nil, httpErrorf(404, "任务不存在: %s", taskName)
}

// taskWorkspace 取本请求要操作的工作空间。
//
// 与代理面共用 X-AMKR-Workspace 这一个头：调用方与管理面用同一个概念选空间，
// 不必记两套机制。缺省即默认工作空间，因此既有的管理调用（都不带这个头）语义
// 一字未变——这是对外契约。
//
// 刻意复用 config.NormalizeWorkspace：空白与命名规则必须与运行时一致，否则
// 「在 A 里建、去 A 里读」会因多一个空格而落空。
func taskWorkspace(r *http.Request) string {
	return config.NormalizeWorkspace(r.Header.Get(config.WorkspaceHeader))
}

// —— GET /api/unified-model ——

func (s *Server) handleGetUnifiedModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "unified_model", Value: serializeUnified(cfg)},
		))
	})
}

// —— PUT /api/unified-model ——

func (s *Server) handleUpdateUnifiedModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specUnifiedModelUpdate, false)
		if err != nil {
			return nil, err
		}
		revision := optString(payload, "config_revision")

		// 分支一：整段替换 unified_model（default 非 null 时）。
		if nonNull(payload, "default") {
			unifiedData := canonical.NewObject()
			unifiedData.SetKey("default", payload.Lookup("default"))
			for _, planName := range []string{"image", "embeddings"} {
				if nonNull(payload, planName) {
					unifiedData.SetKey(planName, payload.Lookup(planName))
				}
			}
			cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
				return configops.SetUnifiedModel(data, unifiedData)
			}, revision)
			if err != nil {
				return nil, err
			}
			data, err := s.managementConfigData()
			if err != nil {
				return nil, err
			}
			return withRevision(data, objectOf(
				canonical.ObjectPair{Key: "unified_model", Value: serializeUnified(cfg)},
			))
		}

		// 分支二：切换目标。缺 model 时抛 ManagementAPIError——它在 _update_config
		// 之外，因此对外是 500 而不是 422（这是参照实现的历史行为，刻意保留）。
		if !nonNull(payload, "model") {
			return nil, &managementAPIError{status: 422, message: "必须提供 model 或 default"}
		}
		targetModel := trimSpace(*optString(payload, "model"))
		keyProvided := hasKey(payload, "key")
		targetKey := trimmedOrNil(optString(payload, "key"))
		imageModelProvided := hasKey(payload, "image_model")
		targetImageModel := trimmedOrNil(optString(payload, "image_model"))
		imageKeyProvided := hasKey(payload, "image_key")
		targetImageKey := trimmedOrNil(optString(payload, "image_key"))

		cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
			if err := configops.SwitchUnifiedTarget(data, "default.primary", &targetModel, targetKey, keyProvided); err != nil {
				return err
			}
			switch {
			case imageModelProvided && targetImageModel != nil:
				return configops.SwitchUnifiedTarget(data, "image.primary", targetImageModel, targetImageKey, imageKeyProvided)
			case imageModelProvided:
				// 显式传了 image_model 但值为空：删掉整个 image 计划。
				migrated, err := config.MigrateConfigData(data)
				if err != nil {
					return err
				}
				if unified := migrated.Lookup("unified_model"); unified != nil && unified.IsObject() {
					unified.DeleteKey("image")
					return configops.SetUnifiedModel(data, unified)
				}
				return nil
			case imageKeyProvided:
				return configops.SwitchUnifiedTarget(data, "image.primary", nil, targetImageKey, true)
			}
			return nil
		}, revision)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "unified_model", Value: serializeUnified(cfg)},
		))
	})
}

// —— DELETE /api/unified-model ——

func (s *Server) handleDeleteUnifiedModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		_, err = s.updateConfig(r, func(data *canonical.Value) error {
			return configops.SetUnifiedModel(data, nil)
		}, revision)
		return nil, err
	})
}

// —— GET /api/tasks ——

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, workspace, err := s.authorizedTaskConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		tasks := canonical.NewArray()
		// 只列本空间的任务。响应体**没有 workspace 字段**：既有调用方依赖
		// {tasks:[{name,model,fallback_model,params}], config_revision} 这个形状，加字段
		// 就会改掉已发布的接口。display_name 是唯一的例外，且只在任务真的取了名时才
		// 出现（见 taskResponse），因此对没取名的既有任务仍然逐字节不变。
		for _, task := range cfg.Tasks {
			if task.Workspace == workspace {
				tasks.Arr = append(tasks.Arr, taskResponse(task))
			}
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "tasks", Value: tasks},
		))
	})
}

// —— POST /api/tasks ——

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specTaskCreate, false)
		if err != nil {
			return nil, err
		}
		name := *optString(payload, "name")
		// model 可省略：任务可以先建出来占位（尚未指定模型），请求它时由 proxy 报 404。
		model := ""
		if chosen := optString(payload, "model"); chosen != nil {
			model = *chosen
		}
		displayName := ""
		if name := optString(payload, "display_name"); name != nil {
			displayName = *name
		}
		fallbackModel := optString(payload, "fallback_model")
		params := taskParamsPayload(payload)

		// 面板 key 的空间由 key 决定，完整权限的由请求头决定——两者都由
		// updateWorkspaceTaskConfig 解析好交给 mutation，这里不再自己读请求头。
		cfg, workspace, err := s.updateWorkspaceTaskConfig(r, func(data *canonical.Value, workspace string) error {
			_, err := configops.CreateTaskIn(data, workspace, name, configops.CreateTaskOptions{
				Model:         model,
				DisplayName:   displayName,
				FallbackModel: fallbackModel,
				Params:        params,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		task, err := findConfigTask(cfg, workspace, name)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, taskResponse(*task))
	})
}

// taskParamsPayload 对应 management_api.py:1425 的 _task_params_payload。
//
// 「未提供」与「提供成空对象」都得到 None：Python 是 `values or None`，空 dict 为
// 假值。这个区分会影响 update_task 的 update_params 语义。
func taskParamsPayload(payload *canonical.Value) *canonical.Value {
	params, present := payload.LookupOK("params")
	if !present || params == nil || !params.IsObject() {
		return nil
	}
	if params.Obj.Len() == 0 {
		return nil
	}
	return params
}

// —— GET /api/tasks/{task_name} ——

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, workspace, err := s.authorizedTaskConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		task, err := findConfigTask(cfg, workspace, r.PathValue("task_name"))
		if err != nil {
			return nil, err
		}
		return withRevision(data, taskResponse(*task))
	})
}

// —— PUT /api/tasks/{task_name} ——

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specTaskUpdate, false)
		if err != nil {
			return nil, err
		}
		taskName := r.PathValue("task_name")
		// 三个可选字段的可更新性由 hasKey 判定：显式传 null 也算「提到了它」，表示要
		// 清空该字段。optString 对「缺失」和「null」都返回 nil，单靠它无法区分，因此
		// 这里把 null 换算成指向空串的指针，交给 configops 按「空 = 清空」处理。
		model := clearableString(payload, "model")
		displayName := clearableString(payload, "display_name")
		if model == nil && displayName == nil && !hasKey(payload, "fallback_model") && !hasKey(payload, "params") {
			return nil, httpErrorf(422, "至少需要提供一个要更新的字段")
		}
		fallback := optString(payload, "fallback_model")
		updateFallback := hasKey(payload, "fallback_model")
		params := taskParamsPayload(payload)
		updateParams := hasKey(payload, "params")

		cfg, workspace, err := s.updateWorkspaceTaskConfig(r, func(data *canonical.Value, workspace string) error {
			_, err := configops.UpdateTaskIn(data, workspace, taskName, configops.UpdateTaskOptions{
				Model:          model,
				DisplayName:    displayName,
				FallbackModel:  fallback,
				UpdateFallback: updateFallback,
				Params:         params,
				UpdateParams:   updateParams,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		task, err := findConfigTask(cfg, workspace, taskName)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, taskResponse(*task))
	})
}

// —— DELETE /api/tasks/{task_name} ——

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		taskName := r.PathValue("task_name")
		_, _, err = s.updateWorkspaceTaskConfig(r, func(data *canonical.Value, workspace string) error {
			return configops.DeleteTaskIn(data, workspace, taskName)
		}, revision)
		return nil, err
	})
}

// —— GET /api/settings ——

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		cfg, err := config.FromDict(data)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "settings", Value: settingsResponse(cfg)},
		))
	})
}

// —— PUT /api/settings ——

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specSettingsUpdate, false)
		if err != nil {
			return nil, err
		}
		revision := optString(payload, "config_revision")
		updates := updatesWithout(payload, "config_revision")
		if updates.Obj.Len() == 0 {
			return nil, httpErrorf(422, "至少提供一个设置字段")
		}
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.UpdateSettings(data, updates)
		}, revision); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		cfg, err := config.FromDict(data)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "settings", Value: settingsResponse(cfg)},
		))
	})
}

// updatesWithout 返回去掉某些键后的浅拷贝，对应 `updates.pop("config_revision")`。
func updatesWithout(payload *canonical.Value, drop ...string) *canonical.Value {
	skipped := map[string]bool{}
	for _, key := range drop {
		skipped[key] = true
	}
	out := canonical.NewObject()
	for _, key := range payload.Obj.Keys() {
		if skipped[key] {
			continue
		}
		child, _ := payload.Obj.Get(key)
		out.SetKey(key, child)
	}
	return out
}

// —— POST /api/settings/local-api-key ——

func (s *Server) handleRegenerateLocalAPIKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRevisionPayload, false)
		if err != nil {
			return nil, err
		}
		generate := s.GenerateLocalAPIKey
		if generate == nil {
			generate = config.GenerateLocalAPIKey
		}
		localAPIKey, err := generate()
		if err != nil {
			return nil, err
		}
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.RegenerateLocalAPIKey(data, localAPIKey)
		}, optString(payload, "config_revision")); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "local_api_key", Value: canonical.NewString(localAPIKey)},
			canonical.ObjectPair{Key: "local_api_key_fingerprint", Value: fingerprintOf(localAPIKey)},
		))
	})
}

// —— POST /api/update/check ——

func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		if s.CheckUpdate == nil {
			return nil, &managementAPIError{status: 500, message: "更新检查未接入"}
		}
		result := s.CheckUpdate(10.0)
		updateAvailable := result.UpdateAvailable
		if !updateAvailable && result.LatestVersion != "" && result.CurrentVersion != "" {
			// Python 侧 update_available 由 VersionCheckResult 自己算；接缝给的是
			// 最终结果，这里不再重复推导，只在调用方没填时按 is_newer_version
			// 的语义保持原样（false）。
			updateAvailable = false
		}
		errorValue := canonical.NewNull()
		if result.Error != "" {
			errorValue = canonical.NewString(result.Error)
		}
		latest := canonical.NewNull()
		if result.LatestVersion != "" {
			latest = canonical.NewString(result.LatestVersion)
		}
		return objectOf(
			canonical.ObjectPair{Key: "current_version", Value: canonical.NewString(result.CurrentVersion)},
			canonical.ObjectPair{Key: "latest_version", Value: latest},
			canonical.ObjectPair{Key: "release_url", Value: nullableString(result.ReleaseURL)},
			canonical.ObjectPair{Key: "source", Value: nullableString(result.Source)},
			canonical.ObjectPair{Key: "artifact_url", Value: nullableString(result.ArtifactURL)},
			canonical.ObjectPair{Key: "artifact_sha256", Value: nullableString(result.ArtifactSHA256)},
			canonical.ObjectPair{Key: "update_available", Value: canonical.NewBool(updateAvailable)},
			canonical.ObjectPair{Key: "error", Value: errorValue},
		), nil
	})
}

// —— POST /api/config/export ——

func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		exported, err := configops.TransferableConfig(data)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "config", Value: exported},
		))
	})
}

// —— POST /api/config/import ——

func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specConfigImportRequest, false)
		if err != nil {
			return nil, err
		}
		rawConfig := payload.Lookup("config")
		migrated, err := config.MigrateConfigData(rawConfig)
		if err != nil {
			return nil, err
		}
		imported, err := configops.TransferableConfig(migrated)
		if err != nil {
			return nil, err
		}

		var addedModels, addedKeys, skippedKeys int
		data, err := s.updateConfig(r, func(current *canonical.Value) error {
			merged, err := configops.MergeTransferableConfig(current, imported)
			if err != nil {
				return err
			}
			addedModels, addedKeys, skippedKeys = merged.AddedModels, merged.AddedKeys, merged.SkippedKeys
			// 先备份当前配置（Python 用 `<name>.<stamp>.<rand8>.bak`），再整体替换。
			backupPath := importBackupPath(s.ConfigPath, s.uuidHex())
			if err := config.SaveConfigData(backupPath, current); err != nil {
				return err
			}
			replaceObject(current, merged.Config)
			return nil
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		_ = data
		// v3_update 返回的是**重新读盘**的数据，不是 _update_config 的返回值。
		fresh, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(fresh, objectOf(
			canonical.ObjectPair{Key: "imported", Value: canonical.NewBool(true)},
			canonical.ObjectPair{Key: "added_models", Value: canonical.NewInt(pyIntLiteral(addedModels))},
			canonical.ObjectPair{Key: "added_keys", Value: canonical.NewInt(pyIntLiteral(addedKeys))},
			canonical.ObjectPair{Key: "skipped_keys", Value: canonical.NewInt(pyIntLiteral(skippedKeys))},
		))
	})
}

// importBackupPath 生成导入前的备份文件名。
//
// 对齐 `config_path.with_name(f"{config_path.name}.{stamp}.{uuid4().hex[:8]}.bak")`；
// stamp 是 UTC 的 "%Y%m%d%H%M%S%f"。
func importBackupPath(configPath, uuidHex string) string {
	now := time.Now().UTC()
	stamp := now.Format("20060102150405") + padMicroseconds(now.Nanosecond()/1000)
	suffix := uuidHex
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	dir, name := splitPath(configPath)
	return joinPath(dir, name+"."+stamp+"."+suffix+".bak")
}
