package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// CPA 配置与账号写操作只开放明确字段。配置树和凭据字段里含有上游密钥、OAuth token
// 与任意请求头，通用透传会把一个便捷管理入口变成凭据覆盖/外发通道。
type cpaRuntimeConfigResponse struct {
	Instance      string   `json:"instance"`
	ETag          string   `json:"etag"`
	Debug         *bool    `json:"debug,omitempty"`
	RequestRetry  *int64   `json:"request_retry,omitempty"`
	Strategy      *string  `json:"strategy,omitempty"`
	ClientAPIKeys []string `json:"client_api_keys"`
}

type cpaAccountActionResponse struct {
	Instance  string `json:"instance"`
	AuthIndex string `json:"auth_index"`
	Action    string `json:"action"`
}

func (s *Server) cpaClientForInstance(r *http.Request, instanceID string) (cpaManagementClient, error) {
	if _, err := s.authorizedConfig(r); err != nil {
		return cpaManagementClient{}, err
	}
	data, err := s.managementConfigData()
	if err != nil {
		return cpaManagementClient{}, err
	}
	instances, err := configops.ParseCPAInstances(data)
	if err != nil {
		return cpaManagementClient{}, err
	}
	for _, instance := range instances {
		if instance.ID != instanceID {
			continue
		}
		if instance.BaseURL == "" || instance.ManagementKey == "" {
			return cpaManagementClient{}, httpErrorf(http.StatusConflict, "实例缺少 base_url 或 management_key，请先补全")
		}
		return cpaManagementClient{baseURL: instance.BaseURL, key: instance.ManagementKey, http: cpaHTTPClient}, nil
	}
	return cpaManagementClient{}, httpErrorf(http.StatusNotFound, "CPA 实例不存在: %s", instanceID)
}

func readCPARuntimeConfig(ctx context.Context, client cpaManagementClient, instanceID string) (cpaRuntimeConfigResponse, error) {
	result := cpaRuntimeConfigResponse{Instance: instanceID, ClientAPIKeys: []string{}}
	var raw json.RawMessage
	headers, err := client.doWithHeaders(ctx, http.MethodGet, "/v8/management/config", nil, &raw, nil)
	if err != nil {
		return result, err
	}
	config, err := canonical.Parse(raw)
	if err != nil || !config.IsObject() {
		return result, httpErrorf(http.StatusBadGateway, "CPA 配置响应不是 JSON 对象")
	}
	result.ETag = headers.Get("ETag")
	if value := config.Lookup("observability").Lookup("logs").Lookup("debug"); value.IsBool() {
		debug, _ := value.AsBool()
		result.Debug = &debug
	}
	if value := config.Lookup("routing").Lookup("retry").Lookup("request-retry"); value.IsNumber() {
		if retries, ok := value.AsInt(); ok {
			result.RequestRetry = &retries
		}
	}
	if value := config.Lookup("routing").Lookup("strategy"); value.IsString() {
		strategy := value.StringValue()
		result.Strategy = &strategy
	}
	if value := config.Lookup("access").Lookup("api-keys"); value.IsArray() {
		result.ClientAPIKeys = []string{}
		for _, item := range value.Arr {
			if key, ok := item.AsString(); ok {
				result.ClientAPIKeys = append(result.ClientAPIKeys, key)
			}
		}
	}
	return result, nil
}

func setCPAConfigValue(patch *canonical.Value, path []string, value *canonical.Value) {
	current := patch
	for _, key := range path[:len(path)-1] {
		next := current.Lookup(key)
		if !next.IsObject() {
			next = canonical.NewObject()
			current.SetKey(key, next)
		}
		current = next
	}
	current.SetKey(path[len(path)-1], value)
}

func cpaConfigWriteError(err error) error {
	var managementErr *cpaManagementError
	if errors.As(err, &managementErr) && managementErr.status == http.StatusPreconditionFailed {
		return httpErrorf(http.StatusConflict, "CPA 配置已被其它操作修改，请重新读取后再保存")
	}
	return httpErrorf(http.StatusBadGateway, "写入 CPA 配置失败: %s", err.Error())
}

func (s *Server) handleGetCPARuntimeConfig(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		client, err := s.cpaClientForInstance(r, r.PathValue("instance_id"))
		if err != nil {
			return nil, err
		}
		config, err := readCPARuntimeConfig(r.Context(), client, r.PathValue("instance_id"))
		if err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "读取 CPA 配置失败: %s", err.Error())
		}
		return canonicalFromAny(config)
	})
}

func (s *Server) handlePatchCPARuntimeConfig(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specCPAConfigUpdate, false)
		if err != nil {
			return nil, err
		}
		client, err := s.cpaClientForInstance(r, r.PathValue("instance_id"))
		if err != nil {
			return nil, err
		}
		patch := canonical.NewObject()
		if debug := optBool(payload, "debug"); debug != nil {
			setCPAConfigValue(patch, []string{"observability", "logs", "debug"}, canonical.NewBool(*debug))
		}
		if hasKey(payload, "request_retry") {
			retries, _ := payload.Lookup("request_retry").AsInt()
			setCPAConfigValue(patch, []string{"routing", "retry", "request-retry"}, canonical.NewInt(strconv.FormatInt(retries, 10)))
		}
		if strategy := optString(payload, "strategy"); strategy != nil {
			switch *strategy {
			case "round-robin", "fill-first", "weighted-round-robin":
			default:
				return nil, httpErrorf(http.StatusUnprocessableEntity, "不支持的 CPA 路由策略: %s", *strategy)
			}
			setCPAConfigValue(patch, []string{"routing", "strategy"}, canonical.NewString(*strategy))
		}
		if hasKey(payload, "client_api_keys") {
			keys := optStringSlice(payload, "client_api_keys")
			seen := map[string]bool{}
			values := []*canonical.Value{}
			for _, key := range keys {
				key = strings.TrimSpace(key)
				if key == "" {
					return nil, httpErrorf(http.StatusUnprocessableEntity, "CPA 客户端密钥不能为空")
				}
				if seen[key] {
					return nil, httpErrorf(http.StatusUnprocessableEntity, "CPA 客户端密钥重复: %s", key)
				}
				seen[key] = true
				values = append(values, canonical.NewString(key))
			}
			setCPAConfigValue(patch, []string{"access", "api-keys"}, canonical.NewArray(values...))
		}
		if patch.Obj.Len() == 0 {
			return nil, httpErrorf(http.StatusUnprocessableEntity, "至少需要提供一个要更新的 CPA 配置字段")
		}
		headers := http.Header{"If-Match": []string{*optString(payload, "etag")}}
		body := json.RawMessage(canonical.DumpsOrdered(patch))
		if _, err := client.doWithHeaders(r.Context(), http.MethodPatch, "/v8/management/config", body, nil, headers); err != nil {
			return nil, cpaConfigWriteError(err)
		}
		// 成功后重新读取白名单视图和最新 ETag，不能把请求草稿当成 CPA 已落盘的配置。
		config, err := readCPARuntimeConfig(r.Context(), client, r.PathValue("instance_id"))
		if err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "CPA 配置已写入，但重新读取失败: %s", err.Error())
		}
		return canonicalFromAny(config)
	})
}

func (s *Server) handleCPAAccountAction(w http.ResponseWriter, r *http.Request, action string) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		var spec *modelSpec
		switch action {
		case "status":
			spec = specCPAAccountStatus
		case "fields":
			spec = specCPAAccountFields
		default:
			spec = specCPAAccountRoutingReset
		}
		payload, _, err := decodePayload(r, spec, false)
		if err != nil {
			return nil, err
		}
		client, err := s.cpaClientForInstance(r, r.PathValue("instance_id"))
		if err != nil {
			return nil, err
		}
		authIndex := payload.Lookup("auth_index").StringValue()
		body := canonical.NewObject()
		body.SetKey("auth_index", canonical.NewString(authIndex))
		path := "/v8/management/routing/cooldown/reset"
		method := http.MethodPost
		switch action {
		case "status":
			path = "/v8/management/credentials/status"
			method = http.MethodPatch
			body.SetKey("name", canonical.NewString(payload.Lookup("name").StringValue()))
			disabled, _ := payload.Lookup("disabled").AsBool()
			body.SetKey("disabled", canonical.NewBool(disabled))
		case "fields":
			path = "/v8/management/credentials/fields"
			method = http.MethodPatch
			body.SetKey("name", canonical.NewString(payload.Lookup("name").StringValue()))
			updated := false
			for _, field := range []string{"label", "note"} {
				if value := optString(payload, field); value != nil {
					body.SetKey(field, canonical.NewString(*value))
					updated = true
				}
			}
			if !updated {
				return nil, httpErrorf(http.StatusUnprocessableEntity, "至少需要提供 label 或 note")
			}
		}
		if err := client.do(r.Context(), method, path, json.RawMessage(canonical.DumpsOrdered(body)), nil); err != nil {
			return nil, httpErrorf(http.StatusBadGateway, "CPA 账号操作失败: %s", err.Error())
		}
		return canonicalFromAny(cpaAccountActionResponse{
			Instance: r.PathValue("instance_id"), AuthIndex: authIndex, Action: action,
		})
	})
}

func (s *Server) handlePatchCPAAccountStatus(w http.ResponseWriter, r *http.Request) {
	s.handleCPAAccountAction(w, r, "status")
}

func (s *Server) handlePatchCPAAccountFields(w http.ResponseWriter, r *http.Request) {
	s.handleCPAAccountAction(w, r, "fields")
}

func (s *Server) handleResetCPAAccountRouting(w http.ResponseWriter, r *http.Request) {
	s.handleCPAAccountAction(w, r, "routing-reset")
}
