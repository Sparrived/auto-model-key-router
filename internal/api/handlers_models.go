package api

import (
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// 本文件移植 management_api.py 里与「模型」和「模型的 key」相关的 11 条路由。

// —— GET /api/models ——

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		models := canonical.NewArray()
		for _, model := range cfg.Models {
			models.Arr = append(models.Arr, modelResponse(model))
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "models", Value: models},
		))
	})
}

// —— POST /api/models ——

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specModelCreate, false)
		if err != nil {
			return nil, err
		}
		modelData, err := modelCreateData(payload)
		if err != nil {
			return nil, err
		}
		revision := optString(modelData, "config_revision")
		modelID := modelData.Lookup("id").PyStr()
		routingMode := modelData.Lookup("routing_mode").StringValue()
		if routingMode == "" {
			routingMode = "round_robin"
		}
		reasoningEffort := optString(modelData, "reasoning_effort")
		keys := canonical.NewArray()
		if rawKeys := modelData.Lookup("keys"); rawKeys != nil && rawKeys.IsArray() {
			keys.Arr = append(keys.Arr, rawKeys.Arr...)
		}

		cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.CreateModelWithKeys(data, modelID, configops.CreateModelOptions{
				Aliases:         trailingStringSlice(modelData, "aliases"),
				RoutingMode:     routingMode,
				ReasoningEffort: reasoningEffort,
			}, keys.Arr)
		}, revision)
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, modelID)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, modelResponse(*model))
	})
}

// —— GET /api/models/{model_id} ——

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, r.PathValue("model_id"))
		if err != nil {
			return nil, err
		}
		return withRevision(data, modelResponse(*model))
	})
}

// —— PUT /api/models/{model_id} ——

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specModelUpdate, false)
		if err != nil {
			return nil, err
		}
		modelID := r.PathValue("model_id")
		revision := optString(payload, "config_revision")
		updates := updatesWithout(payload, "config_revision")
		if updates.Obj.Len() == 0 {
			return nil, httpErrorf(400, "至少需要提供一个要更新的字段")
		}
		if err := normalizeModelUpdates(updates); err != nil {
			return nil, err
		}
		updatedModelID := modelID
		if newID := optString(updates, "id"); newID != nil {
			updatedModelID = *newID
		}

		cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
			_, err := configops.UpdateModel(data, modelID, configops.UpdateModelOptions{
				NewID:                 optString(updates, "id"),
				Aliases:               optStringSlice(updates, "aliases"),
				RoutingMode:           optString(updates, "routing_mode"),
				ReasoningEffort:       optString(updates, "reasoning_effort"),
				UpdateReasoningEffort: hasKey(updates, "reasoning_effort"),
			})
			return err
		}, revision)
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, updatedModelID)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, modelResponse(*model))
	})
}

// —— DELETE /api/models/{model_id} ——

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	s.runStatus(w, func() (int, *canonical.Value, error) {
		// 这条路由的请求体是**可选**的（management_api.py:958 的
		// `payload: RevisionPayload | None = None`）：不带 body 时不校验版本，
		// 带 {} 反而是 422 missing。同一族的 providers/routes DELETE 则是必填。
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return 0, nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		modelID := r.PathValue("model_id")
		mutate := func(data *canonical.Value) error {
			return configops.DeleteModel(data, modelID)
		}
		if dryRunRequested(r) {
			body, err := s.dryRunImpact(r, mutate)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, body, nil
		}
		if _, err := s.updateConfig(r, mutate, revision); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

// —— GET /api/models/{model_id}/keys ——

func (s *Server) handleListModelKeys(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, r.PathValue("model_id"))
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		keys := canonical.NewArray()
		for _, key := range model.Keys {
			keys.Arr = append(keys.Arr, keyResponse(key))
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "keys", Value: keys},
		))
	})
}

// —— POST /api/models/{model_id}/keys ——

func (s *Server) handleCreateModelKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specKeyCreate, false)
		if err != nil {
			return nil, err
		}
		keyData, err := keyCreateData(payload)
		if err != nil {
			return nil, err
		}
		revision := optString(keyData, "config_revision")
		upstreamRoutes := canonical.NewNull()
		if rawRoutes, present := keyData.LookupOK("upstream_routes"); present {
			upstreamRoutes = rawRoutes
		}
		modelID := r.PathValue("model_id")
		keyName := keyData.Lookup("name").PyStr()
		apiKey := keyData.Lookup("api_key").PyStr()
		enabled := boolOr(keyData, "enabled", true)
		hasRoutes := upstreamRoutes.Kind != canonical.KindNull

		cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.CreateModelKey(data, modelID, keyName, apiKey, configops.CreateModelKeyOptions{
				BaseURL:              optString(keyData, "base_url"),
				Enabled:              &enabled,
				UpstreamRoutes:       upstreamRoutes,
				UpdateUpstreamRoutes: hasRoutes,
			})
		}, revision)
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, modelID)
		if err != nil {
			return nil, err
		}
		key, err := findConfigKey(model, keyName)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, keyResponse(*key))
	})
}

// —— GET /api/models/{model_id}/keys/{key_name} ——

func (s *Server) handleGetModelKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, r.PathValue("model_id"))
		if err != nil {
			return nil, err
		}
		key, err := findConfigKey(model, r.PathValue("key_name"))
		if err != nil {
			return nil, err
		}
		return withRevision(data, keyResponse(*key))
	})
}

// —— GET /api/models/{model_id}/keys/{key_name}/stats ——

func (s *Server) handleGetModelKeyStats(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		modelID := r.PathValue("model_id")
		keyName := r.PathValue("key_name")
		model, err := findConfigModel(cfg, modelID)
		if err != nil {
			return nil, err
		}
		if _, err := findConfigKey(model, keyName); err != nil {
			return nil, err
		}
		// hours 解析失败被静默忽略（值为 None），而不是 422——这是参照实现的历史
		// 行为（`hours=abc` 时如此），刻意保留。
		var hours *float64
		if raw := r.URL.Query().Get("hours"); raw != "" {
			if parsed, parseErr := canonical.ToFloat(canonical.NewString(raw)); parseErr == nil {
				hours = &parsed
			}
		}
		if s.KeyStats == nil {
			return nil, &managementAPIError{status: 500, message: "指标未接入"}
		}
		result, err := s.KeyStats(modelID, keyName, hours)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, result)
	})
}

// —— PUT /api/models/{model_id}/keys/{key_name} ——

func (s *Server) handleUpdateModelKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specKeyUpdate, false)
		if err != nil {
			return nil, err
		}
		modelID := r.PathValue("model_id")
		keyName := r.PathValue("key_name")
		revision := optString(payload, "config_revision")
		updates := updatesWithout(payload, "config_revision")
		// 与 provider key 的 PUT 不同，模型 key 在这里**有**空更新检查（400）。
		if updates.Obj.Len() == 0 {
			return nil, httpErrorf(400, "至少需要提供一个要更新的字段")
		}
		if err := normalizeKeyUpdates(updates); err != nil {
			return nil, err
		}
		updatedKeyName := keyName
		if newName := optString(updates, "name"); newName != nil {
			updatedKeyName = *newName
		}
		upstreamRoutes := canonical.NewNull()
		if rawRoutes, present := updates.LookupOK("upstream_routes"); present {
			upstreamRoutes = rawRoutes
		}
		hasRoutes := upstreamRoutes.Kind != canonical.KindNull

		cfg, err := s.updateConfig(r, func(data *canonical.Value) error {
			_, err := configops.UpdateModelKey(data, modelID, keyName, configops.UpdateModelKeyOptions{
				NewName:              optString(updates, "name"),
				APIKey:               optString(updates, "api_key"),
				BaseURL:              optString(updates, "base_url"),
				UpdateBaseURL:        hasKey(updates, "base_url"),
				Enabled:              optBool(updates, "enabled"),
				UpstreamRoutes:       upstreamRoutes,
				UpdateUpstreamRoutes: hasRoutes,
			})
			return err
		}, revision)
		if err != nil {
			return nil, err
		}
		model, err := findConfigModel(cfg, modelID)
		if err != nil {
			return nil, err
		}
		key, err := findConfigKey(model, updatedKeyName)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, keyResponse(*key))
	})
}

// —— DELETE /api/models/{model_id}/keys/{key_name} ——

func (s *Server) handleDeleteModelKey(w http.ResponseWriter, r *http.Request) {
	s.runStatus(w, func() (int, *canonical.Value, error) {
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return 0, nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		modelID := r.PathValue("model_id")
		keyName := r.PathValue("key_name")
		mutate := func(data *canonical.Value) error {
			return configops.DeleteModelKey(data, modelID, keyName)
		}
		// 解绑最后一个 key 会把这个模型一起删掉（模型下没有目标就不该存在），
		// 因此这条路由同样支持预演。
		if dryRunRequested(r) {
			body, err := s.dryRunImpact(r, mutate)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, body, nil
		}
		if _, err := s.updateConfig(r, mutate, revision); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

// optBool 取可选布尔字段：缺失或 null 返回 nil。
func optBool(data *canonical.Value, key string) *bool {
	value, present := data.LookupOK(key)
	if !present || value == nil || value.Kind == canonical.KindNull {
		return nil
	}
	converted, _ := value.AsBool()
	return &converted
}
