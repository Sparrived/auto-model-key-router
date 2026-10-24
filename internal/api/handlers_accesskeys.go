package api

import (
	"net/http"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// 访问密钥的管理端点（Go 侧新增，取代已删除的访客模式）。
//
// 「访问密钥」是分发给外部使用者的受限推理凭据。原先是固定的一把 `amkr-visitor`，
// 权限由每个上游 key 上的 allow_visitor 开关拼出来——全局共享、无法按人收窄。改成
// 独立资源后，每把 key 带自己的两份清单（providers 与 models），于是分发、收窄、
// 停用、轮换都成为针对单把 key 的操作。
//
// 五个端点：GET 列目录、POST 新建（回明文一次）、PUT 改清单/名字/启停、
// POST …/rotate 换 key、DELETE 删除。
//
// **明文只在新建与轮换的响应里出现**：目录与任何 GET 都只回指纹。凭据随列表散出去，
// 等于每次打开管理页都重新泄漏一遍；要看已有 key 只能翻配置文件或轮换。

// accessKeyCatalog 把配置渲染成访问密钥目录。
//
// 形状 [{id, name, enabled, providers, models, key_fingerprint}, ...]：数组而非对象，
// 因为调用方要按顺序渲染成列表，而对象的键序在 JSON 里本就不是可靠契约。顺序按配置
// 里的插入顺序——运维刚建的那把会出现在末尾，比字典序更符合"我刚加了什么"的直觉。
//
// providers / models 只在配置里**显式写了**字段时才出现：省略表示「不限制」，空数组
// 表示「一个都不许」。两者对界面是有区别的状态，不能让空数组同时代表它们——那会把
// 运维下的禁令显示成「未限制」。这与 workspaceEntryOf 对 models 的处理是同一约定。
func accessKeyCatalog(cfg *config.RouterConfig) *canonical.Value {
	items := canonical.NewArray()
	for i := range cfg.AccessKeys {
		key := cfg.AccessKeys[i]
		entry := objectOf(
			canonical.ObjectPair{Key: "id", Value: canonical.NewString(key.ID)},
			canonical.ObjectPair{Key: "name", Value: canonical.NewString(key.Name)},
			canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(key.Enabled)},
			// 指纹而不是 key 本身：界面要能区分"这是哪把"，但没有任何理由把凭据回显
			// 出来。与 local_api_key 的处理一致。
			canonical.ObjectPair{Key: "key_fingerprint", Value: fingerprintOf(key.Key)},
		)
		if key.Providers != nil {
			entry.SetKey("providers", stringArray(key.Providers))
		}
		if key.Models != nil {
			entry.SetKey("models", stringArray(key.Models))
		}
		items.Arr = append(items.Arr, entry)
	}
	return items
}

// accessKeyID 取本请求要操作的访问密钥 ID（路径参数）。
//
// 与 workspaceName 同一条理由：显式编码的 "%20" 能出现在 URL 段里，空 ID 会让
// configops 的 404 文案退化成「访问密钥不存在: 」，调用方看不出是哪一步错了。
func accessKeyID(r *http.Request) (string, error) {
	id := trimSpace(r.PathValue("key_id"))
	if id == "" {
		return "", httpErrorf(422, "访问密钥 ID 不能为空")
	}
	return id, nil
}

// scopeFromList 读取新建请求里的清单字段，转成 configops.SetList。
//
// 新建时 null 与「字段缺失」同义：本次不设这份清单（不限制）。三态里的「清除限制」
// 在新建时没有意义——没有既有限制可清。更新接口用 scopeFromNullableList。
func scopeFromList(payload *canonical.Value, name string) configops.SetList {
	value, present := payload.LookupOK(name)
	if !present || value.IsNull() {
		return configops.SetList{}
	}
	return configops.SetList{Present: true, Values: listValues(payload, name)}
}

// scopeFromNullableList 读取更新请求里的清单字段，null 表示**清除限制**。
//
// 与 scopeFromList 的唯一差别就是 null 的含义，而这一差别是整组接口里最容易写错的
// 地方：把 null 当成「不传」会让「改回不限制」这个动作静默失效，调用方以为放宽了，
// 实际清单还在。校验层把这两个字段标成必填（见 specAccessKeyUpdate），因此这里可以
// 放心把 null 解读成显式的清除指令。
func scopeFromNullableList(payload *canonical.Value, name string) configops.SetList {
	value, present := payload.LookupOK(name)
	if !present {
		return configops.SetList{}
	}
	if value.IsNull() {
		// Values 为 nil 且 Present 为真 = 删掉字段、回到不限制。
		return configops.SetList{Present: true}
	}
	return configops.SetList{Present: true, Values: listValues(payload, name)}
}

// listValues 取出清单的字符串值，空数组归一成**非 nil** 空切片。
//
// nil 与空切片在 configops 里语义不同（nil = 清除限制，空切片 = 一个都不许），而
// optStringSlice 对空数组返回 nil，因此这里必须补成空切片。
func listValues(payload *canonical.Value, name string) []string {
	if values := optStringSlice(payload, name); values != nil {
		return values
	}
	// 校验层已挡住非数组；走到这里说明是空数组。
	return []string{}
}

// —— GET /api/access-keys ——

func (s *Server) handleListAccessKeys(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		cfg, err := s.authorizedConfig(r)
		if err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		items := accessKeyCatalog(cfg)
		if s.AccessKeyLastUsed != nil {
			lastUsed, err := s.AccessKeyLastUsed()
			if err != nil {
				return nil, err
			}
			for _, entry := range items.Arr {
				lastUsedAt := canonical.NewNull()
				if at, ok := lastUsed[entry.Lookup("id").StringValue()]; ok {
					lastUsedAt = canonical.NewString(at)
				}
				entry.SetKey("last_used_at", lastUsedAt)
			}
		}
		return withRevision(data, objectOf(
			canonical.ObjectPair{Key: "access_keys", Value: items},
		))
	})
}

// —— POST /api/access-keys ——

// handleCreateAccessKey 新建一把访问密钥并返回它的明文。
//
// key 在响应里**明文出现一次**，之后任何 GET 都不再回它（见文件头的说明）。key 可选，
// 不传由服务端生成。
func (s *Server) handleCreateAccessKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusCreated, func() (*canonical.Value, error) {
		payload, _, err := decodePayload(r, specAccessKeyCreate, false)
		if err != nil {
			return nil, err
		}
		// name 在校验层是必填且非空，因此这里取到的一定是它；回落到默认名只为不在
		// 一条 422 路径上 panic（与 proxy 里同名取值同一考虑）。
		name := stringOr(payload, "name", config.DefaultAccessKeyName)
		providerScope := scopeFromList(payload, "providers")
		modelScope := scopeFromList(payload, "models")

		var created configops.CreatedAccessKey
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			var err error
			// key 可选：缺省时由 configops 生成（GenerateAccessKey）。这里不能直接对
			// optString 的结果解引用——字段缺席时它是 nil。
			created, err = configops.CreateAccessKey(data, name, configops.CreateAccessKeyOptions{
				Key:       stringOr(payload, "key", ""),
				Providers: providerScope,
				Models:    modelScope,
				Enabled:   optBool(payload, "enabled"),
			})
			return err
		}, optString(payload, "config_revision")); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, accessKeyValue(data, created.ID, created.Key))
	})
}

// —— PUT /api/access-keys/{key_id} ——

// handleUpdateAccessKey 改名字、启停与两份清单；**不换 key**。
//
// 轮换是独立端点（见 handleRotateAccessKey）：换 key 会让调用方手里那把立刻失效，
// 是必须单独告知的动作。塞进「更新」里会让一次改名顺手换掉别人正在用的凭据。
func (s *Server) handleUpdateAccessKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		id, err := accessKeyID(r)
		if err != nil {
			return nil, err
		}
		payload, _, err := decodePayload(r, specAccessKeyUpdate, false)
		if err != nil {
			return nil, err
		}
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			return configops.UpdateAccessKey(data, id, configops.UpdateAccessKeyOptions{
				Name:      optString(payload, "name"),
				Enabled:   optBool(payload, "enabled"),
				Providers: scopeFromNullableList(payload, "providers"),
				Models:    scopeFromNullableList(payload, "models"),
			})
		}, optString(payload, "config_revision")); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, accessKeyValue(data, id, ""))
	})
}

// —— POST /api/access-keys/{key_id}/rotate ——

// handleRotateAccessKey 换掉一把访问密钥的明文并返回新值。
//
// 明文只在这一次响应里出现。旧 key 立即失效（配置里只存新值）。
func (s *Server) handleRotateAccessKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusOK, func() (*canonical.Value, error) {
		id, err := accessKeyID(r)
		if err != nil {
			return nil, err
		}
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		var key string
		if _, err := s.updateConfig(r, func(data *canonical.Value) error {
			var err error
			key, err = configops.RotateAccessKey(data, id)
			return err
		}, revision); err != nil {
			return nil, err
		}
		data, err := s.managementConfigData()
		if err != nil {
			return nil, err
		}
		return withRevision(data, accessKeyValue(data, id, key))
	})
}

// —— DELETE /api/access-keys/{key_id} ——

func (s *Server) handleDeleteAccessKey(w http.ResponseWriter, r *http.Request) {
	s.run(w, http.StatusNoContent, func() (*canonical.Value, error) {
		id, err := accessKeyID(r)
		if err != nil {
			return nil, err
		}
		payload, present, err := decodePayload(r, specRevisionPayload, true)
		if err != nil {
			return nil, err
		}
		var revision *string
		if present {
			revision = optString(payload, "config_revision")
		}
		_, err = s.updateConfig(r, func(data *canonical.Value) error {
			return configops.DeleteAccessKey(data, id)
		}, revision)
		return nil, err
	})
}

// accessKeyValue 渲染单把访问密钥，可选附上明文 key。
//
// 直接读原始配置而不是回落到 *config.RouterConfig：调用点刚写完盘，手上就有这份
// data，重建一次全量配置只为读一个字段既慢又引入一条会失败的路径。
//
// plaintext 非空时才出现 key 字段：更新响应走同一条路径（plaintext 为空），因此形状
// 与新建/轮换一致，而**只有**这两个动作会把凭据带出去。
func accessKeyValue(data *canonical.Value, id, plaintext string) *canonical.Value {
	entry := rawAccessKeyEntry(data, id)
	value := objectOf(
		canonical.ObjectPair{Key: "id", Value: canonical.NewString(id)},
		canonical.ObjectPair{Key: "name", Value: canonical.NewString(stringOr(entry, "name", id))},
		canonical.ObjectPair{Key: "enabled", Value: canonical.NewBool(boolOr(entry, "enabled", true))},
		canonical.ObjectPair{Key: "key_fingerprint", Value: fingerprintOf(entry.Lookup("key").StringValue())},
	)
	// providers / models 只在配置里显式写了字段时才出现（nil 与 [] 的区别），沿用
	// accessKeyCatalog 的约定。
	for _, field := range []string{"providers", "models"} {
		if raw, present := entry.LookupOK(field); present && raw.IsArray() {
			value.SetKey(field, raw.Clone())
		}
	}
	if plaintext != "" {
		value.SetKey("key", canonical.NewString(plaintext))
	}
	return value
}

// rawAccessKeyEntry 返回原始配置里某把访问密钥的对象；不存在时返回空对象。
func rawAccessKeyEntry(data *canonical.Value, id string) *canonical.Value {
	entry, _ := data.Lookup("access_keys").LookupOK(id)
	if !entry.IsObject() {
		return canonical.NewObject()
	}
	return entry
}

// stringOr 取对象的字符串成员，缺失或为空时回落到 fallback。
func stringOr(value *canonical.Value, key, fallback string) string {
	if text := trimSpace(value.Lookup(key).StringValue()); text != "" {
		return text
	}
	return fallback
}
