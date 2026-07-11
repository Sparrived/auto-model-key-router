# Unified Model 熔断路由与一致性修复设计

## 目标

为 `unified-model` 增加无状态、请求级的主备模型故障转移：主要模型在当前请求内耗尽可用 Key 和既有重试后，尝试配置的熔断模型。该设计同时修复 unified-model 当前的图像目标校验、显式 Key 路由、alias 规范化、管理入口语义和模型列表不一致问题。

第一版不实现跨请求的模型级断路器，不维护 open、half-open 或模型冷却状态。现有 Key 级健康、冷却与重试继续负责单个模型内部的故障隔离。

## 配置结构

统一模型改为按请求类型组织的嵌套路由计划：

```json
{
  "unified_model": {
    "default": {
      "primary": {
        "model": "primary-chat",
        "key": null
      },
      "fallback": {
        "model": "backup-chat",
        "key": null
      }
    },
    "image": {
      "primary": {
        "model": "primary-image",
        "key": null
      },
      "fallback": {
        "model": "backup-image",
        "key": null
      }
    }
  }
}
```

内部配置类型为：

```python
@dataclass(frozen=True)
class RouteTarget:
    model: str
    key: str | None = None


@dataclass(frozen=True)
class RoutePlan:
    primary: RouteTarget
    fallback: RouteTarget | None = None


@dataclass(frozen=True)
class UnifiedModelConfig:
    default: RoutePlan
    image: RoutePlan | None = None
```

配置规则：

- `default.primary` 必填；其他目标可选。
- 未配置 `image.primary` 时，图像请求继承 `default.primary`，保持现有行为。
- 未配置 `image.fallback` 时，图像请求没有熔断目标，不继承 `default.fallback`。
- 每个目标引用的模型必须存在；固定 Key 必须属于该模型且已启用。
- 配置加载时将模型 alias 规范化为真实模型 ID，运行时配置和管理接口只暴露 canonical ID。
- 同一计划的 primary 和 fallback 不允许指向同一真实模型，避免无意义的重复调用。
- 不限制不同模型使用同名 Key，也不根据 base URL 判断两个模型是否相同。

## 旧配置迁移

旧平铺结构：

```json
{
  "unified_model": {
    "model": "chat-model",
    "key": "chat-key",
    "image_model": "image-model",
    "image_key": "image-key"
  }
}
```

迁移规则：

- `model/key` 迁移到 `default.primary`。
- `image_model/image_key` 迁移到 `image.primary`。
- 旧配置不生成 fallback，因此迁移不改变请求行为。
- 迁移函数必须幂等；新结构再次加载时不得产生文件变化。
- `RouterConfig.load()` 沿用现有迁移后写回机制，一次性持久化新结构。
- 管理 API 在一个兼容周期内接受旧平铺 payload，但 `GET` 和所有新写入统一返回嵌套结构。

## 请求分类与路由解析

代理入口先把路径归类为稳定的请求类型，再查询 unified 路由计划。第一版只有：

- `image`：`images/generations`、`images/edits`。
- `default`：其余支持的代理路径。

路径分类集中在 `proxy_support.py`，KeyPool 不再直接认识协议路径字符串。未来增加第三类独立路由时，只增加请求类型及配置项，不继续增加成对字段。

只有调用方请求模型为 `unified-model` 时启用主备计划。直接请求真实模型、alias 或 visitor 公共模型时保持现有单模型行为。

`unified-model[key-name]` 中的显式 Key 只覆盖 primary 的 Key。进入 fallback 后使用 fallback 自己配置的固定 Key 或自动路由，不复用调用方的 Key 名称。

## 故障转移流程

请求执行顺序为：

```text
解析 RoutePlan
  -> 执行 primary 的既有 Key 路由与重试
  -> primary 成功：返回
  -> primary 可重试失败且尝试耗尽：执行 fallback
  -> fallback 成功：返回并标记发生过故障转移
  -> fallback 失败：返回 fallback 的最终错误
```

每个目标独立使用当前 `round_robin`、`priority`、`only_first` 和 `max_retries` 语义。primary 必须先耗尽自己的执行预算，才进入 fallback；fallback 再获得完整的单目标预算。最坏调用次数和延迟是两个目标尝试次数之和。

允许进入 fallback 的失败包括：

- primary 没有配置 Key 或没有可用 Key；
- 网络连接、读取或首字节超时等 `httpx.RequestError`；
- `401`、`403`、`429`、`500`、`502`、`503`、`504`；
- 上述可重试结果在 primary 内部的全部尝试已经耗尽。

以下结果不得进入 fallback：

- `400`、`404`、`409`、`422` 等请求、资源或能力错误；
- 已经向客户端输出内容后的流式中断；
- 真实模型或 alias 的直接请求；
- fallback 自身失败，第一版不支持递归备用链。

原生 Anthropic/Responses 端点回退到兼容端点仍是单个目标内部的协议降级。协议降级完成后仍失败，才判断是否切换模型。

## 流式请求边界

上游成功状态和首字节尚未转交客户端前，可以安全切换模型。上游返回可重试错误状态或首字节超时时，按普通请求进入 fallback。

一旦第一个响应字节或 SSE 事件已经发送，响应身份和内容已经确定，中途断流只记录当前模型失败并结束流，不得拼接备用模型输出。这样避免一个响应混合两个模型的内容。

上游可能已经处理请求但响应在网络中丢失，因此现有重试和新增 fallback 都无法提供 exactly-once。图像生成等计费操作可能重复执行，使用文档必须明确这一风险。

## 组件职责

### `config.py`

- 定义 `RouteTarget`、`RoutePlan` 和新 `UnifiedModelConfig`。
- 解析嵌套配置并完成旧结构迁移。
- 集中解析 alias、校验模型、启用 Key、继承规则和主备不同模型约束。
- 保证运行时 `UnifiedModelConfig` 只包含 canonical 模型 ID。

### `proxy_support.py`

- 将请求路径分类为 `default` 或 `image`。
- 保留 `model[key]` 解析，但不决定主备目标。

### `key_pool.py`

- 普通 alias 表不再加入 `unified-model`，避免普通 `resolve_model_id()` 绕过路由计划。
- 提供 `resolve_unified_plan(route_kind, explicit_key)`，返回本次请求的 canonical primary 和可选 fallback。
- Key 选择、冷却、健康状态和 routing mode 保持单模型职责。
- `available_model_ids()` 对本地调用返回所有可用真实模型、alias 和已配置的 `unified-model`；visitor 逻辑不变。

### `proxy_handler.py`

- 将现有单目标尝试循环提取为 `_execute_route_target()`。
- 单目标执行返回成功响应、不可故障转移响应，或携带最终错误的“尝试耗尽”内部结果。
- 外层只负责依次执行 primary 和 fallback，不实现任意候选链或规则引擎。
- 最后一次可重试响应必须在返回客户端前转成“尝试耗尽”，使 fallback 有机会运行。

### `unified_model.py`

- 成为 CLI、TUI、管理 API 共用的唯一配置 mutation 服务。
- 统一处理 alias、换模型时清除旧 Key、显式 null、可选目标清除和完整配置校验。
- 保留现有 `switch_unified_model()` 作为 `default.primary` 的兼容包装。

### `management_api.py`

- `GET /api/unified-model` 返回 canonical 嵌套配置。
- `PUT /api/unified-model` 采用完整对象替换语义，不再模拟隐式 PATCH。
- 一个兼容周期内接受旧平铺请求体，并立即按新结构写回。
- `DELETE /api/unified-model` 行为不变。

### CLI 与 TUI

- 旧 `--switch-model/--switch-key` 继续操作 `default.primary`。
- 新增 `--unified-target`，支持 `default.primary`、`default.fallback`、`image.primary`、`image.fallback`。
- 可选目标支持清除；`default.primary` 不可清除。
- TUI 展示四个目标槽位，每个槽位选择模型及自动或固定 Key；不提供通用规则编辑器。

## 可观测性与公开接口

- fallback 成功的响应增加 `X-AMKR-Fallback: true`，不暴露 Key。
- 记录结构化故障转移事件，包括请求模型、请求类型、primary、fallback 和 primary 最终失败原因。
- 现有 metrics 已记录真实 `model_id` 和 `requested_model_id`，第一版不迁移指标数据库；fallback 比例可由两者和事件日志判断。
- `/health` 返回 canonical 嵌套路由计划。
- 本地 `/v1/models` 返回所有实际可调用的真实模型、alias 和 `unified-model`。
- visitor `/v1/models` 和访问限制保持不变，不暴露或允许 unified-model。

## 热加载

热加载必须先完整构建候选 `RouterConfig` 和 `KeyPool`，全部成功后才替换当前运行时。候选配置的迁移、模型解析、Key 校验或 KeyPool 构造失败时：

- 记录明确错误；
- 不更新已应用配置的 mtime；
- 不替换当前运行时；
- 当前及后续请求继续使用最后一个合法运行时。

该行为同时修复无效 `image_model` 绕过校验后在 KeyPool 构造阶段抛出裸 `KeyError` 的问题。

## 已有问题的修复范围

本设计必须同时解决：

1. 图像模型和图像 Key 未在 `RouterConfig.validate()` 中校验。
2. `unified-model[key]` 因显式 Key 跳过图像模型分流。
3. CLI/TUI/API 对换模型、保留 Key 和显式清除的语义不一致。
4. 图像目标只能由 API 或手工配置设置，CLI/TUI 只能展示。
5. unified 指向 alias 时 Agent reasoning effort 查找错误。
6. `/v1/models` 隐藏仍可直接调用的真实模型和 alias。
7. KeyPool 候选构造异常不在热加载保护范围内。

不进行与上述目标无关的配置编辑器、KeyPool 或代理重构。

## 测试矩阵

### 配置与迁移

- 旧平铺结构迁移为 default/image primary。
- 迁移后再次加载不改变文件。
- alias 规范化为真实模型 ID。
- 不存在模型、不存在或禁用 Key、无效图像目标、无效 fallback 在加载阶段拒绝。
- 同一计划的 primary/fallback 指向同一模型时拒绝。
- image primary 继承 default primary，image fallback 不继承。

### 路由与错误

- primary 成功时不访问 fallback。
- primary 所有 Key 耗尽后访问 fallback。
- 网络错误及约定的可重试状态触发 fallback。
- `400`、`404`、`409`、`422` 不触发 fallback。
- 真实模型、alias 和 visitor 请求不触发 fallback。
- `unified-model[key]` 只固定 primary Key。
- image 使用独立主备计划。
- primary/fallback 分别遵守 routing mode 和重试预算。
- fallback 失败时返回其最终错误并保留 primary 失败日志。

### 流式与协议

- 成功流建立前的 `429/5xx` 和首字节超时可以 fallback。
- 已输出首个事件后的断流不 fallback。
- 原生端点到兼容端点的协议降级发生在模型 fallback 之前。

### 接口与热加载

- API 新结构完整替换、旧 payload 兼容和 DELETE。
- CLI 旧参数兼容及四种 target 操作。
- TUI 设置、清除和展示四个目标槽位。
- `/health` 返回 canonical 新结构。
- 本地 `/v1/models` 返回真实模型、alias、unified；visitor 不变。
- fallback 成功响应包含 `X-AMKR-Fallback: true`。
- 非法热加载继续使用旧运行时，合法配置能够原子替换。

## 实施顺序

1. 为现有 unified-model 缺陷增加失败回归测试。
2. 引入嵌套配置类型、canonical 解析和旧结构迁移，保持现有运行行为。
3. 集中 unified 配置 mutation，并让 CLI、TUI、API 共用。
4. 增加请求类型分类和主备计划解析。
5. 提取单目标执行循环，验证行为不变。
6. 接入 fallback 外层流程及流式边界。
7. 更新 API、CLI、TUI、health、models、示例配置和用户文档。
8. 运行相关测试、完整测试，并通过文件配置热加载做一次集成验证。

## 明确不做

- 模型级 open/half-open 断路器和跨请求冷却。
- 后台健康探测。
- 任意长度候选链或递归 fallback。
- 用户自定义错误码和规则表达式。
- fallback 指标数据库 schema 迁移。
- 已输出流的模型切换。

当实际加入 embeddings、audio 等第三类独立目标时，在当前 route kind 结构上增加新计划；只有出现两个以上备用候选的真实需求时，才重新评估候选链。
