# v4 配置：去掉模型池抽象，Key 直接绑定模型

## 背景与目标

v3 中「模型池(pool)」是每个供应商 Key 的归属容器，模型路由 target 必须引用
`provider/pool/upstream_model`，池还承载启用模型白名单、可用模型探测缓存等。

用户反馈：模型池抽象本意是聚合 Key，实际成为认知负担。添加 Key 时被迫选择/新建
模型池、理解「一个 Key 只能属于一个池」「池的 models 白名单」等概念。实际上
「按模型管理 Key」更符合直觉。

本设计去除用户可见的模型池概念：

- 磁盘配置升级到 v4：`models[].targets[]` 直接以 `key` 粒度绑定供应商 Key，
  一个模型可以挂多个 Key，每个 Key 相互独立；Key 通过 `providers.*.keys`
  声明并经 target 引用，不再存在 pools 层级。
- 每个供应商的 base URL、三个协议路由路径（openai/anthropic/responses/images）
  对所有 Key 相同；能力探测提升为「每个 Key 独立」：探测结果按 Key 缓存
  （`providers.*.keys.<key>.capabilities`），只在添加该 Key 时自动探测一次，
  之后可手动刷新（提供接口/菜单动作）。
- 添加 Key 流程：输入 API key -> 自动探测这个新 Key（无论是否该供应商第一个
  Key，都只探测它）-> 展示该 Key 可服务的模型清单 -> 自动把这些模型建为本地
  模型（模型 ID 取上游模型名）并将该 Key 绑定到这些模型；用户可反选或手动
  输入补充。同一供应商的不同 Key 可见模型可能不同，因此每次添加只探测新 Key，
  结果不复用、不折叠。
- 同一供应商下多个 Key 可各自服务不同模型集；允许多个模型共用同一 Key
  （Key 会出现在这些模型的 keys 中，各自独立），不再有「Key 必须且只能属于
  一个池」的唯一性约束与重复归属修复流程。

## 磁盘格式（config_version: 4）

```json
{
  "config_version": 4,
  "host": "127.0.0.1",
  "port": 8000,
  "local_api_key": "amkr_...",
  "providers": {
    "openai": {
      "base_url": "https://api.openai.com",
      "routes": {"openai": "v1/chat/completions"},
      "keys": {
        "main": {
          "api_key": "sk-...",
          "enabled": true,
          "allow_visitor": false,
          "capabilities": {
            "models": ["gpt-5.5", "gpt-5.5-1"],
            "route_status": {"openai": "ok", "anthropic": "skip", "responses": "ok"},
            "checked_at": "2026-09-01T00:00:00+00:00",
            "errors": {}
          }
        }
      }
    }
  },
  "models": {
    "gpt-5.5": {
      "aliases": [],
      "routing_mode": "round_robin",
      "reasoning_effort": null,
      "native_first": true,
      "targets": [
        {"provider": "openai", "key": "main", "upstream_model": "gpt-5.5"}
      ]
    }
  },
  "unified_model": {...}
}
```

要点：

- `providers.*.pools` 删除；探测缓存按 Key 存放：新增 `providers.*.keys.<key>.capabilities`
  （每个 Key 独立的探测缓存，同一供应商不同 Key 可见模型可能不同）。
- `models.*.targets[]` 每个元素：
  - `provider` 必填（引用已有供应商）
  - `key` 必填（供应商内 Key 名，取代 v3 的 `pool` 引用）
  - `upstream_model` 必填：该 Key 向上游发送的模型名（默认同本地模型 ID）。
- 无任何 target 引用模型的 Key 仍然在 providers.keys 中存在（独立、可用），
  但不会被任何模型路由使用；不算配置错误（与 v3「key 必须属于池」不同）。
- v3 -> v4 迁移（自动、幂等）：
  - 每个 `pool` 的每个 `models` 成员（或 target 的 `upstream_model`）视为一个
    上游模型，与该池所有 keys 一起，为每个「本地模型 ID 等于上游模型 ID」的
    models 生成 `target: {provider, key, upstream_model}`；
  - 保留所有 providers、keys、aliases、routing_mode、reasoning_effort、
    native_first、unified_model、upstream_routes；
  - 旧的池级探测元信息（available_models 等）折进该供应商每个 Key 的
    capabilities（仅作为参考快照，不参与路由判定）；迁移期保守处理：同一池的
    Key 共享同一份 {models, checked_at}（不含 route_status/errors），随后逐 Key
    手动刷新会各自更新；早期 v4 写在 provider 级的 capabilities 也按同样方式
    折入各 Key（加载时自动，写回后 provider 不再保留该字段）；
  - 迁移必须幂等：新结构再次加载不产生文件变化。
- v2 -> v3 -> v4 的旧迁移链继续工作（v2 models[]/keys[] 语义已在 v3 迁移中
  展开为 pool，v3 再迁移到 v4）。

## 路由与 KeyPool 语义

KeyPool 输入是 ModelConfig.keys（模型 -> KeyConfig 数组），v3 解析已把池展开为
模型 keys；v4 解析后模型 keys 仍是该模型绑定的 Key 展开结果。因此
`key_pool.py` 的轮询、优先级、only_first、冷却、健康、亲和、visitor 过滤等
运行语义**无需改动**。改动集中在：

- config.py：解析模型 target 时从 provider.keys 按 `key` 取单 Key 展开
  （保留 provider/base_url/upstream_model 字段），不再处理 pool。
- 配置校验去掉「一个 Key 只能属于一个池」「Key 必须加入池」「pool 引用缺失」
  等 pool 约束；新增「target.key 引用了 provider 不存在的 key」校验
  （保持 v3 的严格错误信息风格：明确供应商、Key 与模型）。
- `models.*.keys` 允许模型 -> 同 provider 同 Key 去重；不同模型共用 Key 合法。

## 管理 API

v4 对齐 TUI 语义，模型池端点下线（保留兼容还是删除？见下）：
- 保留 provider/key/routes 系列端点；routes 端点 = v3 的
  `/api/routes`（模型路由）保持，但 route 里 target 使用 `{provider,key,upstream_model}`。
- pools 系列端点删除；`/api/providers/{id}/pools/*` 404。
- probes：`/api/probes/keys`（逐 Key 探测模型列表）语义保留；
  新增同步刷新端点，探测结果写入各 Key 的 `providers.*.keys.<key>.capabilities`：
  - `POST /api/providers/{provider_id}/probe`：刷新该供应商全部启用 Key
    （携带 `config_revision`，每个 Key 分别探测 models + route_status 后写回，
    响应返回新 provider）；
  - `POST /api/providers/{provider_id}/keys/{key_name}/probe`：刷新单个 Key，
    请求体 `{config_revision, modes?}`，`modes` 限定路由检查范围
    （如 `["openai","responses"]`，省略 = 全部模式），模型清单探测总是执行；
    响应除 provider 外还含 `key` 对象。
  - provider 响应对象不再含顶层 `capabilities`；其 `keys[]` 各项携带
    `capabilities`（可能为 `null`）。
- 迁移期：旧 payload 兼容策略 = 由于 pools 与 routes 的删除是破坏性的，本版本
  直接删除 pools 相关端点并在文档说明（不承诺向后兼容 pools payload）；
  只保留 v2/v3 磁盘结构读取迁移，不做 pools HTTP payload 兼容。

## TUI / CLI / 交互

主菜单「供应商」「模型设置」保留；删除「模型池」入口：
- 供应商菜单：添加 Key / 管理 Key / Base URL 与路由 / 刷新能力探测 / 删除供应商。
  刷新能力探测进入子菜单：「1 刷新全部 Key / 2 指定 Key / 0 返回」；选「2 指定 Key」
  后再选 Key，并可再选端点范围：「1 全部路由模式 / 2 仅 Chat (openai) /
  3 仅 Messages (anthropic) / 4 仅 Responses」。
- 添加 Key：无论是否该供应商第一个 Key，都自动探测这个新 Key（GET /v1/models
  得到该 Key 的模型清单 + 该 Key 对 openai/anthropic/responses 各做一次最小
  请求），随后多选「该 Key 服务的模型」（清单来自该 Key 自己的探测），自动建模型。
- 模型设置：模型列表 -> 模型 Keys（添加/移除该模型的 Key）、别名、路由模式、
  reasoning_effort、删除模型。
- unified-model / 一键配置 / 调用日志等不变。
- 移除 TUI 中「修复重复模型池归属」的自动交互与 startup 提示。

## 统计

metrics 持久化仍带 provider_id/pool_name/upstream_model_id。v4 路由后
pool_name 不再有意义：改为写入 provider_id + upstream_model_id，pool_name 置空，
避免统计归因残留。API 快照保留 provider/upstream_models 维度，去掉
provider_pools 嵌套或保留空表以兼容已有读取端（倾向保留空表并文档说明废弃）。

## 探测

- 每个 Key 独立的能力探测函数（config_editor / management_api 共享，按
  Key 执行，结果互不复用）：
  - 用该 Key 调 GET /v1/models 得到该 Key 的模型清单；
  - 用该 Key 对 openai/anthropic/responses 三个路由模式各做一次最小请求
    （1 token 或 1 像素），记录 route_status；手动刷新单个 Key 时可限定只检查
    其中某个模式；
  - 添加 Key（无论是否该供应商第一个 Key）时自动探测该 Key；探测结果写入
    `providers.*.keys.<key>.capabilities`（models / route_status / errors /
    checked_at）；
  - 提供手动刷新入口（TUI 子菜单与 HTTP 接口，见上）。
- 由于同一供应商的不同 Key 能访问的模型集可能不同（如免费/付费额度、不同
  订阅、各自授权范围），探测以 Key 为单位，不再做供应商级合并探测；模型
  多选「该 Key 服务哪些模型」的清单来自该 Key 自己的探测。

## 兼容周期

- 本版本同时读 v2/v3/v4；加载 v2/v3 自动迁移并写回 v4。
- 显式报错保留：models 列表结构（v1/v2）需要先迁移；config_version 超过 4
  报错提示升级软件。
- 迁移前如检测到重复池归属（v3 不合法数据），仍在启动时报清晰错误
  （不自动交互修复；v4 已无该概念）。

## 明确不做

- 不做 v3 pools HTTP payload 兼容端点。
- 不保留模型池 UI/CLI/API 的任何残留交互。
- 不新增自动把 Key 绑定到「供应商所有可用模型」之外的能力。
- 不做模型级 open/half-open 断路器（沿用现有 key 级健康/冷却）。

## 实施顺序

1. config.py：CONFIG_VERSION=4；新解析 + v3->v4 迁移（含 v2 链路）；类型、
   校验；全量旧测试先修到能解析 v4。
2. config_operations.py：以 target.key 为语义的 mutation。
3. config_editor.py / dashboard.py：交互与面板改版，移除模型池。
4. management_api.py：端点改版。
5. key_pool/metrics/探测/visitor 适配。
6. 文档（README/API/USAGE/CLI/example/CHANGELOG）。
7. 测试改写到新语义并跑全量。
