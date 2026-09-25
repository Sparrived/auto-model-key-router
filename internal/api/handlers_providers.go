package api

import (
	"net/http"
	"sort"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// 本文件移植 management_api.py 里与「供应商」「供应商 key」「路由（models 段的
// v3 视图）」以及探测相关的路由。
//
// 这一族的 DELETE 请求体是**必填**的（`payload: RevisionPayload`），与
// models/tasks/unified-model 的可选请求体形成不对称——这是参照实现的历史行为，
// 刻意保留，改动会改变既有调用方看到的 422 边界。

// —— GET /api/providers ——

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
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
		raw, _ := rawProviders(data)
		providers := canonical.NewArray()
		for _, provider := range cfg.Providers {
			routeOrder := objectKeyOrder(raw.Lookup(provider.ID).Lookup("routes"))
			providers.Arr = append(providers.Arr, providerResponse(provider, routeOrder))
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "providers", Value: providers},
		))
	})
}

// —— POST /api/providers ——

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderCreate, false)
		if err != nil {
			return nil, err
		}
		providerID := payload.Lookup("id").PyStr()
		baseURL := payload.Lookup("base_url").PyStr()
		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.CreateProvider(current, providerID, baseURL)
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "provider", Value: rawProviderResponse(providerID, provider)},
		))
	})
}

// —— GET /api/providers/{provider_id} ——

func (s *Server) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "provider", Value: rawProviderResponse(providerID, provider)},
		))
	})
}

// —— PUT /api/providers/{provider_id} ——

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderUpdate, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		updates := updatesWithout(payload, "config_revision")
		routesValue := canonical.NewNull()
		if raw, present := updates.LookupOK("routes"); present {
			routesValue = raw
		}
		hasRoutes := !routesValue.IsNull()

		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.UpdateProvider(current, providerID, configops.UpdateProviderOptions{
				NewID:        optString(updates, "id"),
				BaseURL:      optString(updates, "base_url"),
				Routes:       routesValue,
				UpdateRoutes: hasRoutes,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		// `updates.get("id", provider_id)`：键存在但值为 null 时得到 None，随后
		// `_require_provider(providers, None)` 查不到 -> 500。Go 侧同样失败关闭。
		newID, ok := stringOrDefault(updates, "id", providerID)
		if !ok {
			return nil, &pyValueError{message: "供应商 id 不能为 null"}
		}
		provider, err := rawRequireProvider(data, newID)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "provider", Value: rawProviderResponse(newID, provider)},
		))
	})
}

// stringOrDefault 复刻 Python 的 `d.get(key, fallback)`。
//
// 第二个返回值为 false 表示「键存在但值是 None」——Python 会把这个 None 继续往下
// 用（查字典 -> KeyError/TypeError -> 500），而不是回落到默认值。调用方必须显式
// 处理这种情形，否则会把一个真实的 500 悄悄变成 200。
func stringOrDefault(data *canonical.Value, key, fallback string) (string, bool) {
	value, present := data.LookupOK(key)
	if !present {
		return fallback, true
	}
	if value == nil || value.IsNull() {
		return "", false
	}
	text, _ := value.AsString()
	return text, true
}

// —— DELETE /api/providers/{provider_id} ——

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRevisionPayload, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		_, err = s.v3Update(r, func(data *canonical.Value) error {
			_, err := configops.DeleteProvider(data, providerID)
			return err
		}, optString(payload, "config_revision"))
		return nil, err
	})
}

// —— GET /api/providers/{provider_id}/keys ——

func (s *Server) handleListProviderKeys(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		provider, err := rawRequireProvider(data, r.PathValue("provider_id"))
		if err != nil {
			return nil, err
		}
		keys := canonical.NewArray()
		rawKeys := provider.Lookup("keys")
		if rawKeys != nil && rawKeys.IsObject() {
			for _, keyName := range rawKeys.Obj.Keys() {
				child, _ := rawKeys.Obj.Get(keyName)
				if child == nil || !child.IsObject() {
					continue
				}
				keys.Arr = append(keys.Arr, rawKeyResponse(keyName, child))
			}
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "keys", Value: keys},
		))
	})
}

// —— POST /api/providers/{provider_id}/keys ——

func (s *Server) handleCreateProviderKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderKeyCreate, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := payload.Lookup("name").PyStr()
		apiKey := payload.Lookup("api_key").PyStr()
		enabled := boolOr(payload, "enabled", true)

		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.CreateProviderKey(current, providerID, keyName, apiKey, configops.CreateProviderKeyOptions{
				Enabled: &enabled,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		// 用**原始** name 去查（`["keys"][payload.name]`）：config 层会 strip 名字，
		// 因此带空白的名字在这里查不到，Python 抛 KeyError -> 500。这是参照实现的
		// 历史行为，刻意保留。
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		key := provider.Lookup("keys").Lookup(keyName)
		if key == nil || !key.IsObject() {
			return nil, &pyValueError{message: "Key 不存在: " + keyName}
		}
		result := rawKeyResponse(keyName, key)
		return withRevision(data, result)
	})
}

// —— GET /api/providers/{provider_id}/keys/{key_name} ——

func (s *Server) handleGetProviderKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		provider, err := rawRequireProvider(data, r.PathValue("provider_id"))
		if err != nil {
			return nil, err
		}
		keyName := r.PathValue("key_name")
		key, err := rawRequireKey(provider, keyName)
		if err != nil {
			return nil, err
		}
		result := rawKeyResponse(keyName, key)
		return withRevision(data, result)
	})
}

// —— PUT /api/providers/{provider_id}/keys/{key_name} ——

func (s *Server) handleUpdateProviderKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderKeyUpdate, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := r.PathValue("key_name")
		updates := updatesWithout(payload, "config_revision")

		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.UpdateProviderKey(current, providerID, keyName, configops.UpdateProviderKeyOptions{
				NewName: optString(updates, "name"),
				APIKey:  optString(updates, "api_key"),
				Enabled: optBool(updates, "enabled"),
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		// `updates.get("name", key_name)`：显式 null 会得到 None，随后
		// _require_key(provider, None) 找不到 -> ManagementAPIError -> 500。
		//
		// 注意这条路由**没有**「至少要提供一个字段」的检查（management_api.py:586
		// 只 pop 了 config_revision），与模型 key 的 PUT 不对称。
		name, ok := stringOrDefault(updates, "name", keyName)
		if !ok {
			// Python 把 None 拼进 f-string，错误文本里的 key 名就是字面量 "None"。
			return nil, &managementAPIError{status: 404, message: "Key 不存在: None"}
		}
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		key, err := rawRequireKey(provider, name)
		if err != nil {
			return nil, err
		}
		return withRevision(data, rawKeyResponse(name, key))
	})
}

// —— DELETE /api/providers/{provider_id}/keys/{key_name} ——

func (s *Server) handleDeleteProviderKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRevisionPayload, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := r.PathValue("key_name")
		_, err = s.v3Update(r, func(data *canonical.Value) error {
			_, err := configops.DeleteProviderKey(data, providerID, keyName)
			return err
		}, optString(payload, "config_revision"))
		return nil, err
	})
}

// —— GET /api/routes ——

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		routesValue, err := rawRoutes(data)
		if err != nil {
			return nil, err
		}
		routes := canonical.NewArray()
		for _, name := range routesValue.Obj.Keys() {
			route, _ := routesValue.Obj.Get(name)
			routes.Arr = append(routes.Arr, routeWithID(name, route))
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "routes", Value: routes},
		))
	})
}

// routeWithID 构造 `{"id": name, **route}`。
//
// 若 route 里本来就有 "id" 键，字典展开会让它覆盖前面的 name——这是 Python 的
// 语义，Go 侧用「先 Set id 再逐键覆盖」复刻。
func routeWithID(name string, route *canonical.Value) *canonical.Value {
	out := canonical.NewObject()
	out.SetKey("id", canonical.NewString(name))
	if route != nil && route.IsObject() {
		for _, key := range route.Obj.Keys() {
			child, _ := route.Obj.Get(key)
			out.SetKey(key, child)
		}
	}
	return out
}

// —— POST /api/routes ——

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRouteCreate, false)
		if err != nil {
			return nil, err
		}
		route := updatesWithout(payload, "config_revision")
		routeID := payload.Lookup("id").PyStr()
		targets := route.Lookup("targets")
		targetItems := []*canonical.Value{}
		if targets != nil && targets.IsArray() {
			targetItems = targets.Arr
		}
		routingMode := "round_robin"
		if raw := route.Lookup("routing_mode"); raw != nil && raw.StringValue() != "" {
			routingMode = raw.StringValue()
		}

		data, err := s.v3Update(r, func(current *canonical.Value) error {
			if err := configops.ValidateTargets(current, targetItems); err != nil {
				return err
			}
			_, err := configops.CreateModel(current, routeID, configops.CreateModelOptions{
				Aliases:     trailingStringSlice(route, "aliases"),
				RoutingMode: routingMode,
				Targets:     targetItems,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		routesValue, err := rawRoutes(data)
		if err != nil {
			return nil, err
		}
		created := routesValue.Lookup(routeID)
		if created == nil || !created.IsObject() {
			// Python 在这里是 `_routes(data)[payload.id]` -> KeyError -> 500。
			return nil, &pyValueError{message: "路由不存在: " + routeID}
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "route", Value: routeWithID(routeID, created)},
		))
	})
}

// —— GET /api/routes/{route_id} ——

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		routesValue, err := rawRoutes(data)
		if err != nil {
			return nil, err
		}
		routeID := r.PathValue("route_id")
		route, err := rawRequireRoute(routesValue, routeID)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "route", Value: routeWithID(routeID, route)},
		))
	})
}

// —— PUT /api/routes/{route_id} ——

// handleUpdateRoute 整体替换某个模型路由的 id / targets / aliases / routing_mode。
//
// 一个特例：`targets` 写成**空数组**等于删除这个路由，响应 204。理由是「路由下没有
// 目标就不该存在」是写路径的全局不变式（取消 Key 勾选、解绑 Key、删 Key 都遵守），
// 要求调用方先清空再 DELETE 只是把这条不变式漏给客户端去维持。
func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	s.runStatus(w, func() (int, *canonical.Value, error) {
		payload, _, err := decodePayload(r, specRouteUpdate, false)
		if err != nil {
			return 0, nil, err
		}
		routeID := r.PathValue("route_id")
		updates := updatesWithout(payload, "config_revision")
		targetsValue := canonical.NewNull()
		if raw, present := updates.LookupOK("targets"); present {
			targetsValue = raw
		}
		var targetItems []*canonical.Value
		if targetsValue.IsArray() {
			targetItems = targetsValue.Arr
		}

		if targetsValue.IsArray() && len(targetItems) == 0 {
			_, err := s.v3Update(r, func(current *canonical.Value) error {
				return configops.DeleteModel(current, routeID)
			}, optString(payload, "config_revision"))
			if err != nil {
				return 0, nil, err
			}
			return http.StatusNoContent, nil, nil
		}

		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.UpdateModel(current, routeID, configops.UpdateModelOptions{
				NewID:       optString(updates, "id"),
				Aliases:     optStringSlice(updates, "aliases"),
				RoutingMode: optString(updates, "routing_mode"),
				Targets:     targetItems,
			})
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return 0, nil, err
		}
		name, ok := stringOrDefault(updates, "id", routeID)
		if !ok {
			return 0, nil, &pyValueError{message: "路由不存在: null"}
		}
		routesValue, err := rawRoutes(data)
		if err != nil {
			return 0, nil, err
		}
		route := routesValue.Lookup(name)
		if route == nil || !route.IsObject() {
			return 0, nil, &pyValueError{message: "路由不存在: " + name}
		}
		body, err := withRevision(data, objectOf(
			canonical.ObjectPair{Key: "route", Value: routeWithID(name, route)},
		))
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, body, nil
	})
}

// —— DELETE /api/routes/{route_id} ——

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRevisionPayload, false)
		if err != nil {
			return nil, err
		}
		routeID := r.PathValue("route_id")
		_, err = s.v3Update(r, func(data *canonical.Value) error {
			return configops.DeleteModel(data, routeID)
		}, optString(payload, "config_revision"))
		return nil, err
	})
}

// —— POST /api/probes/keys ——

func (s *Server) handleProbeKeys(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusAccepted, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProbeKeysRequest, false)
		if err != nil {
			return nil, err
		}
		timeout := 15.0
		if value := payload.Lookup("timeout_seconds"); value != nil && value.IsNumber() {
			timeout, _ = value.AsFloat()
		}
		return s.startProbe(r, payload.Lookup("provider_id").StringValue(), optStringSlice(payload, "keys"), timeout)
	})
}

// —— POST /api/providers/{provider_id}/probe ——

func (s *Server) handleProbeProvider(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specRevisionPayload, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		data, err := s.v3Update(r, func(current *canonical.Value) error {
			provider, err := configops.RequireProvider(current, providerID)
			if err != nil {
				return err
			}
			keys, err := configops.ProviderKeys(provider)
			if err != nil {
				return err
			}
			if keys.Obj.Len() == 0 {
				return &configops.ConfigOperationError{Message: "供应商暂无 Key: " + providerID, StatusCode: 422}
			}
			names := keys.Obj.Keys()
			sort.Strings(names)
			for _, keyName := range names {
				key, _ := keys.Obj.Get(keyName)
				if key == nil || !key.IsObject() || !boolOr(key, "enabled", true) {
					continue
				}
				capabilities, err := s.probeKeyCapability(provider, keyName, nil)
				if err != nil {
					return err
				}
				key.SetKey("capabilities", capabilities)
			}
			return nil
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "provider", Value: rawProviderResponse(providerID, provider)},
		))
	})
}

// probeKeyCapability 通过接缝调用 config_editor.probe_key_capability。
func (s *Server) probeKeyCapability(provider *canonical.Value, keyName string, modes []string) (*canonical.Value, error) {
	if s.ProbeKeyCapability == nil {
		return nil, &managementAPIError{status: 500, message: "能力探测未接入"}
	}
	return s.ProbeKeyCapability(provider, keyName, modes, 15.0)
}

// —— POST /api/providers/{provider_id}/keys/{key_name}/probe ——

func (s *Server) handleProbeProviderKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderKeyProbeRequest, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := r.PathValue("key_name")
		modes := optStringSlice(payload, "modes")
		data, err := s.v3Update(r, func(current *canonical.Value) error {
			provider, err := configops.RequireProvider(current, providerID)
			if err != nil {
				return err
			}
			key, err := configops.RequireKey(provider, keyName)
			if err != nil {
				return err
			}
			capabilities, err := s.probeKeyCapability(provider, keyName, modes)
			if err != nil {
				return err
			}
			key.SetKey("capabilities", capabilities)
			return nil
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		provider, err := rawRequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		key, err := rawRequireKey(provider, keyName)
		if err != nil {
			return nil, err
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "provider", Value: rawProviderResponse(providerID, provider)},
			canonical.ObjectPair{Key: "key", Value: rawKeyResponse(keyName, key)},
		))
	})
}

// —— GET /api/providers/{provider_id}/keys/{key_name}/models ——

func (s *Server) handleGetProviderKeyModels(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := r.PathValue("key_name")
		provider, err := configops.RequireProvider(data, providerID)
		if err != nil {
			return nil, err
		}
		if _, err := configops.RequireKey(provider, keyName); err != nil {
			return nil, err
		}
		models, err := configops.KeyServiceModels(data, providerID, keyName)
		if err != nil {
			return nil, err
		}
		return withRevision(data, keyServiceModelsResponse(providerID, keyName, models))
	})
}

// keyServiceModelsResponse 构造 models 视图响应（models 升序）。
func keyServiceModelsResponse(providerID, keyName string, models []string) *canonical.Value {
	sorted := append([]string{}, models...)
	sort.Strings(sorted)
	return objectOf(
		canonical.ObjectPair{Key: "provider_id", Value: canonical.NewString(providerID)},
		canonical.ObjectPair{Key: "key", Value: canonical.NewString(keyName)},
		canonical.ObjectPair{Key: "models", Value: stringArray(sorted)},
	)
}

// —— PUT /api/providers/{provider_id}/keys/{key_name}/models ——

func (s *Server) handleSetProviderKeyModels(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specProviderKeyModelsRequest, false)
		if err != nil {
			return nil, err
		}
		providerID := r.PathValue("provider_id")
		keyName := r.PathValue("key_name")
		requested := trailingStringSlice(payload, "models")
		data, err := s.v3Update(r, func(current *canonical.Value) error {
			_, err := configops.SetKeyServiceModels(current, providerID, keyName, requested)
			return err
		}, optString(payload, "config_revision"))
		if err != nil {
			return nil, err
		}
		models, err := configops.KeyServiceModels(data, providerID, keyName)
		if err != nil {
			return nil, err
		}
		return withRevision(data, keyServiceModelsResponse(providerID, keyName, models))
	})
}

// —— GET /api/probes/{probe_id} ——

func (s *Server) handleGetProbe(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		record := s.probeRecord(r.PathValue("probe_id"))
		if record == nil {
			return nil, httpErrorf(404, "探测不存在")
		}
		return probeResponse(record), nil
	})
}

// —— POST /api/probes/{probe_id}/cancel ——

func (s *Server) handleCancelProbe(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		if _, err := s.authorizedConfig(r); err != nil {
			return nil, err
		}
		probeID := r.PathValue("probe_id")
		record := s.probeRecord(probeID)
		if record == nil {
			return nil, httpErrorf(404, "探测不存在")
		}
		s.probesMu.Lock()
		record.cancelRequested = true
		cancel := record.cancel
		s.probesMu.Unlock()
		select {
		case <-record.done:
			// 已完成：Python 里 task.done() 为真则不再 cancel，状态保持原样。
		default:
			if cancel != nil {
				cancel()
			}
		}
		return probeResponse(record), nil
	})
}

// probeRecord 读取探测记录（不存在返回 nil）。
func (s *Server) probeRecord(probeID string) *probeRecord {
	s.probesMu.Lock()
	defer s.probesMu.Unlock()
	return s.probes[probeID]
}
