# 工作空间

任务集合的隔离单位。本文说明**为什么这样设计**、边界在哪，以及改动时要守住哪些
不变量。使用方式见 [`USAGE.md` 9.1](USAGE.md#91-工作空间把任务集合分开)（面板的嵌入
方式见 [`USAGE.md` 9.3](USAGE.md#93-把工作空间面板嵌进你自己的后台)），接口细节
见 [`API.md` 的「工作空间」](API.md#工作空间)。

## 1. 解决什么问题

任务路由把「模型 + 一组固定采样参数」打包成一个可直接当 `model` 传的名字。原来的
问题出在**唯一性范围**：任务名全局唯一，于是多个人或多个团队共用一个配置时，每个人
都得为避让别人的名字而加前缀（`ALICE_SUMMARIZE`、`TEAM_A_SUMMARIZE`……）。前缀解决
的是命名冲突，却把一个团队的概念（「摘要任务」）污染成了带人员信息的字符串。

工作空间把唯一性的范围从「整份配置」缩到「一个空间」：

```
默认工作空间（顶层 tasks）        团队 A（workspaces.teamA）
  summarize  -> gpt-4o-mini        summarize  -> claude-sonnet-4
```

同名任务在两个空间里各指各的模型与参数，互不可见。

## 2. 三个已确认的设计决策

| 决策 | 选择 | 理由 |
| --- | --- | --- |
| 工作空间是什么 | 任务的命名空间容器：装一组任务，不同空间任务名可重复 | 它需要表达的是「谁的这一组」，而不是「另一套配置」 |
| 调用方怎么选 | HTTP 头 `X-AMKR-Workspace` | 见下节 |
| 兼容策略 | 保持 `config_version` 4，`workspaces` 为可选新增字段，顶层 `tasks` 视为默认空间 | 既有配置与既有调用方零改动 |

### 为什么用请求头

任务名是请求体 `model` 字段的**值**，因此只有三个候选位置：

- **模型名前缀**（`teamA/summarize`）：会让工作空间与真实模型名挤进同一个命名空间，
  于是还得处理「前缀」与「模型名」的冲突，等于把问题挪了个地方。
- **URL 路径**（`/v1/teamA/chat/completions`）：与参照实现固定的 `/v1/...` 形状冲突。
  代理路径的分类（`RequestRouteKind`）是按固定形状匹配的，动它会波及所有路由判断。
- **请求头**：唯一既不动请求体、也不动路径的位置。因此选它。

首尾空白会被裁掉；空串等价于默认工作空间。这个头**不会**转发给上游（见
`proxysupport.UpstreamHeaders`）：它是 AMKR 自己的路由状态，上游既看不懂也不该看到。

## 3. 边界：它只隔离任务

这是最容易误推的一点。工作空间**只**让任务名免于全局唯一，此外一律不变：

- 模型 ID、别名与 `unified-model` 仍然**全局唯一**，任务名也不能与它们
  撞名。否则 `resolve_route` 的语义会取决于查表顺序——那是全局唯一的判断，工作空间
  不能用来遮蔽模型名。
- 供应商、Key、unified_model、设置项都**没有**工作空间维度。
- 因此工作空间不是「多租户」：它不隔离凭据、不隔离配额，任何人带上这个头就能用任
  何空间的任务。它只是任务集合的命名空间。

用量**可以**按工作空间分开看（见第 8 节），但那只是**观测**，不是**隔离**：归属被
记录下来用于统计与图表，却不用来拒绝任何请求。两者的区别很关键——把统计误当成配额
会得出「工作空间 A 用超了会影响 B」这种并不存在的结论。

## 4. 空工作空间：无 key 的消失，带 key 的留下

工作空间通常由「在它里面建任务」**隐式产生**：

- 删掉某个空间的最后一个任务，这个空间就消失了（`configops.writeWorkspaceTasks`
  删空即删分组）。
- 配置里手写的空分组也会被清掉（`config.RepairTasks` 同样会清理）。
- `RouterConfig.WorkspaceNames()` 因此由**任务反推**，而不是回放配置里的
  `workspaces` 键。

为什么不允许空分组：工作空间只是「一组任务」的容器，一个没有成员的分组既不可观测
（按名字取不到任何东西）也没有意义。允许它存在会立刻带来三处口径分叉——配置里能写、
删空后留不留、界面上列不列——而每一处都要单独决定。由任务反推后只剩一个口径。

### 例外：带凭据或模型清单的工作空间

一个工作空间可以带**面板 key**（`workspaces.<空间>.api_key`，见第 9 节）、**推理 key**
（`workspaces.<空间>.inference_key`，见第 10 节）或**可直呼模型清单**
（`workspaces.<空间>.models`）。这三种东西任一个存在，这个空间即使**一个任务都没有**
也会保留，并有独立的创建入口（`POST /api/workspaces`）。

这不是放松上面那条规则，而是它多了一份**任务之外的可观测内容**。带面板 key 的空间
正是「应用先建空间拿到 key、之后才陆续填任务」这个时序的产物——如果它因为没有任务
就被清掉，那个应用手里的 key 会在下一次配置写回时无声失效。推理 key 同理，而且更隐蔽：
它被配进了各个项目的**环境变量**里，配置里消失之后调用方只会看到 401，很难联想到
「空间被清理了」。

`models` 也计入：它是运维显式写下的授权配置。最常见的形态正是「一个**已经有任务**的
空间被加上了模型限制」——此时它两把 key 都没有。若因为「没有凭据」把它丢掉，限制会在
热重载后静默消失：界面看起来配过，实际完全没生效。

口径仍然只有一条（两个层各有一份实现，必须同时改）：

```go
// configops.hasWorkspaceCredential：凭据或模型清单任一存在就留住这个分组
func hasWorkspaceCredential(entry *canonical.Value) bool {
    return 有 api_key || 有 inference_key || 有 models
}
```

`WorkspaceNames()` 相应地把这些空间一并列出（排在任务反推出的空间之后）。

**反过来说**，界面上的「新建工作空间」走的是显式入口（`POST /api/workspaces`），因为
它的产物（两把 key）必须能立刻交付给用户。什么都不带的空壳依然不存在——手写
`"teamB": {}` 照旧会被清掉。

> 手写的配置是唯一能短暂出现无名无凭据空分组的地方（例如 `"teamB": {}`），它会被解析
> 接受、但不会出现在工作空间清单里，并在下一次写回时消失。

### 改与删

两个既有动作的形状（`internal/configops/tasks.go`）：

- **改名**（`RenameWorkspace`）就是**把整组内容搬到新键下**：旧键清空后由
  `writeWorkspaceTasks` 自动抹掉，因此不需要（也不能）单独「新建一个空间再搬」。
  先写新键再清旧键，顺序不能反——两条路径共用同一批任务对象，先清旧键会把要搬的
  内容一起丢掉。`api_key` 随整组一起搬走（它是这个空间的一部分，不是任务的属性）。
- **删除**（`DeleteWorkspace`）就是**清空该组的内容**，分组随后消失。它因此没有独立
  的删除逻辑，也不会留下空壳。

两条动作的拒绝路径都由「默认空间不可动」与「空间由任务反推（或带 key 而存在）」推出：

| 情形 | 结果 | 理由 |
| --- | --- | --- |
| 改名/删除默认空间 | `400` | 默认空间是不带 `X-AMKR-Workspace` 头的调用方命中的那个，动它会让所有缺省调用方落空；它也没有可以放 key 的槽位 |
| 改名/删除不存在的空间 | `404` | 空间由任务反推（带 key 的空间也由此判定），两者都没有就是不存在，没有可改可删的东西 |
| 改成另一个已存在的空间名 | `409` | **不合并**：两个空间各有一批任务时，合并会瞬间造出重名任务，而任务名在同一空间内唯一是配置层的硬校验 |
| 改成默认空间名 | `409` | 同「与既有空间重名」，默认空间只是那个必然存在的特例 |

## 5. 未知工作空间名不是错误

调用方写了一个没建过的空间名时，**不报错**：那里没有任务，于是任务查表落空，接着按
普通模型名继续解析，最终和「模型未配置」是同一个 `404`。

`NormalizeWorkspace` 因此只做去空白，**不校验存在性**。单加一条「空间不存在」的错误
既没有信息量（调用方真正需要知道的是任务或模型名不对），也会多一种需要维护的状态。

这条只适用于**调用方按请求头选空间**的读路径。管理面的改/删（`PUT`/`DELETE
/api/workspaces/{workspace}`）是另一回事：那两个动作有副作用，悄悄成功比报错危险得多，
因此对不存在的空间报 `404`（见上节）。

## 6. 兼容性契约

工作空间是 Go 侧新增的能力，参照实现没有对应实现，因此它**没有**历史版本要沿用。它的
兼容性契约是另一条：**不影响既有配置与既有调用方**。

由此推出必须守住的性质：

1. **`config_version` 仍是 4**，`workspaces` 是可选新增字段，顶层 `tasks` 就是
   `DefaultWorkspace`。
2. **不带 `X-AMKR-Workspace` 头的请求行为逐字节不变。** 正因如此，`tasks/list` 这类既有
   响应体（形状为 `{"tasks":[{name,model,fallback_model,params}],"config_revision"}`）
   **没有、也不允许**新增 `workspace` 字段——当前空间由请求头决定，不体现在响应里。
3. **新增的 `/api` 路由与已发布清单分开维护。** 管理面 47 条与运维面 7 条路由
   （`routePatterns()` / `opsRoutePatterns()`）是**已发布接口**的清单，它们的响应形状已经
   对外承诺。工作空间自身的操作（`GET|POST /api/workspaces`、
   `PUT|DELETE /api/workspaces/{workspace}`、`POST /api/workspaces/export|import`）
   是管理面的正式资源，因此注册在 `/api` 之下，但列在**另一份**清单
   （`workspacePatterns()`）里：它们没有历史版本可对照，塞进那 47 条会让「这 47 条就是
   已发布行为」这句话失去意义。两批都注册在同一棵 mux 上，因此错方法的
   `405` / `Allow` 判定必须同时看两份清单（`internal/api/server.go` 的 `patterns`）。

   反过来说，**其余新增能力**（价格目录 `/ui/pricing.json`、自更新入口、工作空间用量
   `/ui/workspace-usage.json`、访问密钥用量 `/ui/access-key-usage.json`）仍然挂 `/ui/`：
   那些是本项目自有的读数，与 `/api` 面在语义上不连续，挂 `/ui/` 既落在那份已发布清单
   之外，也让「不开 WebUI 就没有这些读数」这件事顺理成章。判断标准是**它是不是管理面的
   正式资源**，而不是「能不能挂到 /ui 躲开清单」。
4. **访问密钥不能使用任务**，带上该头也一样（它不绑定工作空间，该头只用来选任务所在的
   空间，而访问密钥一律进不了任务路由）。
5. **错误文本**：工作空间引入的新错误（`workspaces 必须是对象`、`工作空间名不能为空`、
   `工作空间名重复: %s`、`工作空间 %s 必须是对象`、`workspaces.<空间>.tasks...`）没有
   Python 先例，是自由措辞；但既有的 `任务…` / `tasks.<名字>…` 文本是**对外契约**，
   既有调用方依赖它，不得改写。
6. **`Validate` 的错误顺序也是契约**。新增检查请追加在末尾或紧邻同类检查，不要插到
   既有检查之前——那会改变既有非法配置报出的第一条错误。

## 7. 数据形状与关键实现

配置（`config` 包）：

```json
{
  "config_version": 4,
  "tasks": {"summarize": {"model": "gpt-4o-mini"}},
  "workspaces": {
    "teamA": {"tasks": {"summarize": {"model": "claude-sonnet-4"}}},
    "panel": {"api_key": "amkr_ws_…"},
    "worker": {"inference_key": "amkr_ik_…", "models": ["gpt-4o-mini"]}
  }
}
```

三个字段都可选，且都让这个空间即使没有任务也保留（第 4 节）：

- `api_key`：嵌入方面板凭据（第 9 节）。
- `inference_key`：推理凭据，给项目做 `/v1` 调用（第 10 节）。
- `models`：允许**直呼**的模型名清单。省略 = 不限制；`[]` = 一个都不许直呼（只走
  任务名）；有内容 = 只许这些。名字可以是模型 id 或别名，逐个校验能解析到已配置的
  模型。**任务名不受它限制**——任务自己固定的模型就是该空间被授权用的。

解析后是**扁平**的一份 `[]TaskConfig`，每项带自己的 `Workspace`（顶层 `tasks` 的
任务归属 `default`）。扁平化让运行时查表退化成一次线性扫描或一次二元组查表，不必在
每个调用点先选分组。

凭据本身另收在 `RouterConfig.Workspaces`（`[]WorkspaceConfig`，只装带凭据或 `models`
的分组）——这份列表恰好等于「需要参与鉴权匹配与模型收窄的空间」，不多不少。

各层的落点：

| 关注点 | 位置 |
| --- | --- |
| 常量与归一化（`DefaultWorkspace` / `WorkspaceHeader` / `NormalizeWorkspace`） | `internal/config/model.go` |
| 解析 `workspaces`、按 `(空间, 任务名)` 校验唯一性、两把 key 的底线与跨类型撞车、`models` 引用校验 | `internal/config/model.go` 的 `parseTasks` / `Validate` / `parseWorkspaceModels` |
| key 的生成（`GenerateWorkspaceKey` / `GenerateInferenceKey`）与反查（`WorkspaceForAPIKey` / `WorkspaceForInferenceKey` / `WorkspaceAllowedModels`） | `internal/config/persist.go`、`internal/config/model.go` |
| 任务 CRUD（`*TaskIn` 系列）、`RepairTasks`、`CreateWorkspace`、导入导出 | `internal/configops/tasks.go`、`transfer.go` |
| 工作空间**整包**迁移（带 key，与配置迁移隔离） | `internal/configops/workspace_bundle.go` |
| 运行时查表（`taskPlans` / `taskParams` 的键是 `[2]string`） | `internal/keypool/pool.go` |
| 读头、选空间（`RequestContext.Workspace`） | `internal/proxy/handler.go` |
| 阻止该头外泄 | `internal/proxysupport/support.go` |
| 管理端点感知空间（`X-AMKR-Workspace` 头） | `internal/api/handlers_meta.go` |
| 空间自身的读/改/删（`/api/workspaces*`、`workspacePatterns()`） | `internal/api/handlers_workspaces.go`、`internal/api/router.go` |
| 空间迁移端点（`/api/workspaces/export|import`） | `internal/api/handlers_workspace_migration.go` |
| 面板 key 的解析（钉死空间、忽略请求头） | `internal/api/server.go` 的 `authorizedTaskConfig` |
| 推理 key 的解析（钉死空间、忽略请求头、模型白名单） | `internal/proxy/handler.go` 的 `authorize` 与模型解析段 |
| `/v1/models` 按作用域收窄 | `internal/server/handlers.go` 的 `handleModels` / `scopedModelNames` |
| 推理 key 轮换与模型清单端点 | `internal/api/handlers_workspaces.go`、`internal/api/validate.go` |
| 归属落库（`RecordParams.Workspace`、`request_workspace` 旁挂表） | `internal/metrics/store.go`、`internal/metrics/schema.go` |
| 归属贯穿（`MetricRecord.Workspace` ← `RequestContext.Workspace`） | `internal/proxy/retry.go`、`internal/server/metricsadapter.go` |
| 空间用量与流向查询（`WorkspaceUsage`） | `internal/metrics/workspace.go` |
| 读数端点（`/ui/workspace-usage.json`） | `internal/server/workspace_usage.go` |
| 嵌入方面板读数（`/ui/workspace-panel.json`） | `internal/server/workspace_panel.go` |
| 流向图基元与页面 | `webui/charts.js`、`webui/chart-math.js`、`webui/pages/workspaces.js` |
| 界面切换与改名/删除 | `webui/pages/tasks.js`、`webui/api.js` |
| 可嵌入的独立面板页 | `webui/panel.html`、`webui/panel.js`、`webui/panel-api.js` |

`WorkspaceNames()` 由任务反推，默认空间固定排首位——界面上的下拉顺序因此与配置文件
里的书写顺序无关，且默认空间永远可直接选中。

## 8. 用量统计与流向图

每个请求在工作空间维度上的归属会被记下来，用于出「各空间用量」与「请求流向」两张
读数（WebUI 的**工作空间**页）。这一节说明存储形状与**为什么不能事后反推**。

### 为什么必须落库：工作空间反推不出来

一个自然的想法是「反正指标里已经存了请求模型名（`requested_model_id`），查的时候
拿它回配置里查一下不就是工作空间了」。这条路走不通：

同一个任务名**可以合法地同时存在于多个工作空间**（这正是工作空间要解决的问题，
见第 1 节）。`router-config.example.json` 里 `TASK_000001` 就同时出现在顶层 `tasks`
与 `workspaces.teamA.tasks` 下。历史指标只记了「调用方传的模型名是 `TASK_000001`」，
单凭它无从判断当时那个 `X-AMKR-Workspace` 头写的是什么。**归属只在请求处理时就近
可得**，因此必须在写入指标时一并记下。

### 存储：旁挂表而不是新列

归属存在 `request_workspace` 旁挂表里，**不是** `request_metrics` 的新列：

```sql
CREATE TABLE IF NOT EXISTS request_workspace (
    request_id INTEGER PRIMARY KEY,   -- 就是 request_metrics.id
    workspace  TEXT NOT NULL
)
```

两个理由，第二个是决定性的：

1. `request_id` 是 `INTEGER PRIMARY KEY`，即 rowid 别名。一对一约束与 JOIN 索引
   同时到手，且**不会**在 `sqlite_master` 里多出一条索引条目。
2. 旁挂表让 `request_metrics` 自身的建表原文、列序与索引定义保持逐字节不变，而
   `ALTER TABLE ... ADD COLUMN` **会重写 `sqlite_master.sql`**（实测：即便在全新库
   上也会把新列以追加形式写进原文），加列因此会让表定义本身发生变化。差异面从
   「表定义被改写」缩小到「多了一张表」。

   `request_metrics` 的列序与索引定义仍是**兼容性契约**（旧二进制要能继续读写同一个
   库），改动前请回到本节与 `internal/metrics` 的 schema 说明确认影响面。

> 旧二进制打开新库时只是看不到这张表，仍能正常读写指标。加列则会遇到它不认识的列序
> （`SELECT *` 与 `table_info` 的输出都会变）。

### 没有归属的行：`unattributed`，不兜底成 default

`workspace` 为空时不写旁挂表。查询端用 `NOT EXISTS` 把这类行单独统计成
`unattributed`（未归属），**不会**并进 `default`：

升级前写入的历史行都在这里。把它们算到默认工作空间头上会凭空造出一段并不存在的
用量，而且看起来像真的。界面因此把未归属提示放在流向图**之前**——否则用户先看到一张
少了一块的图，往下才知道原因。

同理，流向图里某一端为空的请求（`provider_id` / `upstream_model_id` 可空）**不补占位
节点**，而是在那个位置留出缺口。补一个占位符会让它与真实取值混在一起，看图的人分不出
哪条是数据、哪条是兜底。（`key_name` 恒非空，所以「供应商 → 上游 Key」这一段只在
`provider_id` 为空时才缺边。）

### 流向图的六层

```
工作空间 → 请求模型 → 实际模型 → 供应商 → 上游 Key → 上游模型
```

粒度选在这里是因为它恰好是请求在系统里的完整流转，且每一层都已存在于指标里
（`requested_model_id` / `model_id` / `provider_id` / `key_name` / `upstream_model_id`），
无需额外埋点。宽度可切请求数或 Token：前者看调用次数，后者看实际消耗。

**上游 Key 这一层**（后补的，原先是五层、供应商直接连到上游模型）：同一家供应商可以配多把
Key，而 v4 起「模型 → target」的选择就是**选 Key**，所以「打到哪家」与「用的哪把 Key」是
两个不同的问题。只画到供应商时，两把 Key 承担的流量在同一段流带里并成一条，看不出是哪把
Key 出去的。`key_name` 就是被选中的那把上游 Provider Key 名（见 `internal/proxy` 的
`recordMetric`）。

> 按层名取数，**不要写死下标**。Key 层是从「供应商 → 上游模型」中间插进去的：写死「第 4 段
> 是上游模型」的取数在插入之后会静默取到 Key 名（面板的「上游模型用量」卡原先就是这样，
> 已经改成 `layerIndexOf(usage, "upstream_model_id") - 1`，并由
> `webui/probes/webui_panel_probe.mjs` 的 `upstreamCardListsUpstreamModel` 锁住）。

布局上有一条容易写错、写错了却"看起来对"的地方：**纵向必须只用一把尺子**（全局
scale），不能让每层各自缩放到满高。后者会让同一节点的入边与出边拿到不同厚度，流带
溢出节点、读数失真。代价是层总量不齐时留白，而那段留白本身就是"在此处丢失的流量"。
节点自身的量取 `max(流入合计, 流出合计)`——取单条最大边会让多条出边依次排开时溢出。

### 边界

- **统计不是配额**：归属只用于观测，不用来拒绝任何请求（见第 3 节）。
- **归属从本版本才开始记录**：升级前的历史行永远是 `unattributed`，不会追溯回填
  （回填需要当时的工作空间名，而它没有被记下来）。
- 该读数挂在 `/ui/workspace-usage.json`：它是本项目自有的响应形状，没有可比对的
  oracle，因此不混进 `/metrics` 系列（理由与 `/ui/` 的选择一致，见第 6 节）。

## 9. 面板 key 与可嵌入面板

> 接入方的实操指南见 [`PANEL.md`](PANEL.md)：取 key、iframe 嵌入、跨源限制、自定义 UI
> 与排查表。这一节讲的是**为什么这样设计**。

一个工作空间可以带一把 **面板 key**：应用侧创建空间时提供（`POST /api/workspaces` 的
`api_key`），在 WebUI 里创建则由服务端生成（`amkr_ws_` + 43 位 base64url，共 50 字符）
并**只在那一次响应里**返回。

它的用途是让应用把一个**只看得到自己那个空间**的控制面板嵌进自己的后台：面板能读自己
空间的用量与流向，并对自己的任务做增删改，别的什么都不能做。

### 权限范围

| 能做 | 不能做 |
| --- | --- |
| 读写**本空间**的任务（`/api/tasks*`） | 别的空间的任务（请求头被忽略，见下） |
| 读本空间的用量与流向（`/ui/workspace-panel.json`） | 供应商、模型、设置等全局配置（一律 `401`） |
| 创建/改名/删除工作空间 | `/v1/*` 代理面（面板 key 不是推理凭据） |
| | 配置导出/导入、工作空间整包迁移（要完整权限） |

面板 key **不是**第三档权限，而是「被钉死在某个空间上的任务面权限」。实现上它刻意不
进 `internal/auth`：那个包是不依赖 `config` 的纯函数，加一条分支会把它作为通用鉴权
判定的价值弄糊。解析发生在调用点——`internal/api/server.go` 的 `authorizedTaskConfig`
先照常调 `auth.Authenticate`，**失败之后**才拿请求头里的 key 去查
`config.WorkspaceForAPIKey`。因此面板 key 永远走不到 `IsFull()` 那条路。

### 钉死：请求头被忽略

面板 key 生效时，调用方传来的 `X-AMKR-Workspace` **被忽略**，空间由 key 决定。

这是这个模式要防的核心事情：如果请求头能换空间，一把泄漏的面板 key 就等于所有空间的任务
面权限，而这把 key 会出现在被嵌入页面的 URL 里——正是最可能泄漏的位置。同理，面板 key
不能钉在 `default` 上：配置里没有 `workspaces.default` 这个槽位，也不该为它开一个。

### 凭据怎么进浏览器

面板是 `webui/panel.html`（独立精简页），凭据走 **URL fragment**：

```
https://<amkr>/ui/panel.html#k=amkr_ws_…&api=https://<amkr>
```

三条硬规则，都由 `webui/probes/webui_panel_probe.mjs` 断言：

1. **绝不读写 `localStorage`**。面板与后台 WebUI **同源**，而 WebUI 把本地管理 key 存在
   `amkr.apiKey` 里；面板一旦碰存储，一个嵌进第三方后台的页面就能读到完整管理凭据。
2. **绝不发送 `X-AMKR-Workspace`**。见上——那把 key 已经决定了空间。
3. **只从 fragment 取凭据**。fragment 不会进 `Referer`，也不进服务端访问日志；换成
   query string 就会两头都留下明文 key。`?api=` 允许指定 AMKR 基地址（面板与 AMKR
   不同源时用），它只影响请求发往哪里。

### 只显示一次的 key

面板 key 明文只出现在两处：`POST /api/workspaces` 的 201 响应，以及配置文件里的
`workspaces.<空间>.api_key`。工作空间目录（`GET /api/workspaces`）**刻意不返回它**——
那个接口会被列表页轮询，把凭据挂在上面等于每次刷新都重新分发一遍。

因此 WebUI 在建完空间后会立刻弹出「复制 key」与「复制嵌入片段」，并明说这个 key 只显示
这一次。同样的理由让配置导出剥掉 key（见第 11 节）。

### 面板与完整权限页的分工

面板页面调的是 `/ui/workspace-panel.json`（Go 侧新增，在 `workspacePatterns()` 之外
另挂 `/ui/`），只认面板 key；后台 WebUI 的「工作空间」页调的是
`/ui/workspace-usage.json`，只认完整权限。两者读数形状同源（同一个
`metrics.WorkspaceUsage`），但**面板永远看不到 `unattributed`**——那是全实例的缺口读数，
不属于任何一个空间，给面板看既没有意义也泄漏了别的空间的规模。

面板不返回 `models` 之外的全局信息：它给出模型 ID 与**可见**别名供任务表单选择，隐藏
别名（`upstream_model`）不在其中，因为面板不该知道上游叫什么。

> 关于 iframe：本项目**没有**设置 `X-Frame-Options` 或 CSP `frame-ancestors`，因此
> 面板默认可被任意来源 iframe 嵌入。这是**有意的**——嵌入是它的全部用途，加白名单就得
> 让用户先把第三方后台的域名配进来，而那与「面板凭据是拉取式」的模型重复。安全性由
> key 的范围（钉死单空间、不能用代理面）承担，不由来源承担。
>
> 同理**没有**任何 CORS 响应头。为面板开一个 `Access-Control-Allow-Origin: *` 会把整套
> `/api` 管理面的跨源面一起打开，代价远大于收益；代价是 fragment 里的 `api=` 覆盖只在
> 同源（或宿主自己做反代/关同源策略）时可用，跨源直连会被浏览器挡下。这条取舍写在
> [`PANEL.md` 第 5 节](PANEL.md#5-关于-api-与跨源重要)。

## 10. 推理 key：让一个空间共用同一台网关

面板 key 解决的是「嵌一个只看得见自己的面板」，但它**不能调 `/v1`**。当 AMKR 要作为
多个项目共用的网关时，还缺一样东西：给每个项目一把**只能推理、且只属于自己空间**的
凭据。这就是推理 key（`workspaces.<空间>.inference_key`）。

创建空间时与面板 key **一起**发放：`POST /api/workspaces` 的 201 响应同时给出
`api_key` 与 `inference_key`（服务端生成 `amkr_ik_` + 43 位 base64url，共 50 字符）。
一次发两把，是因为建空间是**唯一**能拿到明文 key 的时刻——AMKR 没有任何端点会再回一次
已有 key。若只给面板 key，应用侧就还得再找一条路要推理凭据。

### 权限范围

| 能做 | 不能做 |
| --- | --- |
| 调 `/v1/*` 推理，空间由 key 决定 | 管理面（`/api/*` 一律 `401`），包括本空间的任务 CRUD |
| 直呼 `models` 清单内的模型（未配清单则不限制） | 清单外的模型（`403`，且不触达上游） |
| 用本空间的**任务名**（不受清单限制） | 别的空间的任务（请求头被忽略，见下） |
| 读 `/v1/models`（按空间收窄后的清单） | `unified-model`（全局计划，不属于任何空间） |

与面板 key 一样，它**不是**第三档权限，而是「被钉死在某个空间上的推理面权限」。同样
刻意不进 `internal/auth`：解析在 `internal/proxy/handler.go` 的 `authorize`，先照常调
`auth.Authenticate`，**失败之后**才拿请求头的 key 去查
`config.WorkspaceForInferenceKey`。因此推理 key 永远走不到 `IsFull()` 那条路。

两把 key 的反查函数（`WorkspaceForAPIKey` / `WorkspaceForInferenceKey`）**刻意分开**，
不合成一个「任意凭据 → 空间」的查表：两把 key 的权限不同，调用点必须知道自己匹配上的
是哪一种，合成一个会让「该按哪套权限走」取决于返回值的用法。

### 钉死：请求头被忽略

与面板 key 同理，而且更必要：推理 key 会被写进各个项目的**环境变量**，那是比 URL
fragment 更宽的攻击面。如果 `X-AMKR-Workspace` 能换空间，一把泄漏的 key 就等于所有
空间的推理权限，而这个模式存在的全部意义就是防这件事。

### 模型清单：模型维度的隔离

任务名天然按空间隔离（同名任务在各自空间里指向不同模型），但**真实模型名在配置里是
全局的**。没有 `models` 清单，任何一把推理 key 都能直呼全部模型——这正是「多个项目
共用一台网关」最需要收窄的一维。

判定发生在**解析之后**：别名先解析成真实模型 id，再拿它比对清单。否则同一个模型写成
别名就绕过了白名单。

### 只显示一次的 key

与面板 key 相同的策略：明文只出现在 `POST /api/workspaces` 的 201 响应（以及配置文件
里的 `workspaces.<空间>.inference_key`）。目录接口（`GET /api/workspaces`）不返回它，
只给一个 `has_inference_key` 布尔，让界面能提示「这个空间发过推理凭据，可以轮换」。

轮换走 `POST /api/workspaces/{workspace}/inference-key`，**只换这一把**：面板 key 换掉
会让已嵌入的页面立刻失效，两者的轮换节奏不同（推理 key 泄漏面更宽、轮换更频繁），
合成一个「轮换全部凭据」的端点会逼调用方在只想换一把时承担另一把失效的代价。

### 两把 key 的底线（含跨类型撞车）

`config.Validate` 用**一张占用表**统管两把 key，因此下列情况全部非法：

- 与 `local_api_key` 相同（那等于把主凭据发出去）；
- 与**任何**空间的**任何**一把 key 重复——**包括跨类型**：一个空间的面板 key 不得
  等于另一个空间的推理 key；
- 与**任何一把访问密钥**（`access_keys.*.key`）重复：访问密钥也走 `/v1` 面，与推理
  key 的判定彼此独立，撞车会让同一把 key 的权限取决于先命中哪张清单。

最后一条是必须的：两把 key 的判定发生在不同调用点（面板面 vs `/v1` 面），同一个字符串
两处都命中会让权限边界取决于走到哪条路由。错误文本沿用既有的
`工作空间 a 与 b 的 api_key 重复` 形状（`api_key` 位置按后出现的那种 key 名填充），
既有三条文本因此逐字不变。

## 11. 整包迁移：一条独立通道

工作空间有自己的导出/导入（`POST /api/workspaces/export|import`，
`internal/configops/workspace_bundle.go`），与 `/api/config/export|import` **刻意分开**。

两条通道的语义恰好相反，这是分开的理由：

| | `/api/config/export|import` | `/api/workspaces/export|import` |
| --- | --- | --- |
| `api_key` | **剥掉**（导出文件会被贴进工单与聊天记录） | **带上**（这是有意的凭据搬迁） |
| `providers` / `models` | 核心内容 | **不带**（搬的是命名空间，不是模型库） |
| 内容 | 整台实例 | 指定或全部命名工作空间 |
| 合并规则 | 按 `base_url` / key secret 去重、按模型 ID 合并 | 同空间整包覆盖，或加前缀改名 |

因为不带模型库，包里的任务引用的模型在目标实例上可能不存在。这类任务由 `RepairTasks`
的既有规则清掉并**如实回报**（响应里的 `removed_tasks`），而不是带进来一批请求时必然
`404` 的僵尸任务——这也是「不搬模型」这个选择必须付的代价，付得明白比藏起来好。

冲突策略由 `prefix` 决定：

- **留空** = 同空间整包覆盖。恢复备份的语义：用户要的就是把那个空间变回包里的样子。
- **非空**（如 `teamA-`）= 同名空间改名为 `前缀+原名`，一个都不覆盖。搬别人的空间到
  自己实例上时用。

响应把 `added` 与 `replaced` **分开报**：覆盖会换掉目标实例上那个空间的面板 key，旧 key
立刻失效。调用方必须能把这件事告诉用户，否则嵌入方的面板会毫无征兆地开始 `401`。导入前
先备份配置（与配置导入一致）。

### key 冲突：报错，而不是悄悄换一个

包里的 `api_key` 撞上目标实例上**别处**的凭据（另一个空间、`local_api_key`，或任意一把
**访问密钥**）时，导入**报错**，错误信息指出与哪个空间撞了。

不悄悄换一个新 key 的理由是：换掉看似「让导入成功」，实际藏起了一件用户必须知道的事——
那把 key 通常已经嵌在别人的页面里，换掉之后**旧 key 会指向别人别的空间**，而响应里没有
任何字段能说明这件事。报错则用户一眼知道该改哪一个，改完重试即可。

**唯一的例外是加前缀克隆**：原空间仍然存在并占着原 key，克隆体不可能也用它，因此换 key
是必然的，不算意外。这种情况由响应里的 `rekeyed`（`空间名 -> 新 key`）明确回报——嵌入方
需要拿新 key 更新嵌入片段，否则克隆出来的空间没有可用的面板。

### 包格式

```json
{
  "version": 1,
  "workspaces": ["teamA"],
  "spaces": {"teamA": {"api_key": "amkr_ws_…", "tasks": {"summarize": {"model": "gpt-4o-mini"}}}}
}
```

`spaces` 而不是套一层 `config`：`providers` / `models` 是**合法的空间名**，套包装层会让
「空间叫 providers」与「包里混进了配置导出的段」变成同一种输入，只能二选一地误判。分开
之后两者一眼可辨（有专门用例钉住）。包格式自带 `version`，**不**经过
`config.MigrateConfigData`——工作空间是 v4 才有的概念，没有更旧的形状要迁移，而让迁移
函数遍历一张以空间名为键的表，等于把 `unified_model` 这种空间名当成配置段去改写。

导出与导入都只认**完整权限**：内容里有明文面板 key，比配置导出更敏感。

## 12. 改动时的检查清单

- [ ] 新代码是否让「不带 `X-AMKR-Workspace` 头」的行为发生了变化？既有调用方依赖它。
- [ ] 是否给 `tasks/list` 这类既有响应体新增了字段？（那等于改动已对外承诺的形状）
- [ ] 新增的 `/api` 路由是否放在了 `workspacePatterns()` 这类**独立清单**里，并同步
      了 `internal/api/server.go` 的 `patterns`（错方法的 405 判定）？
- [ ] 挂 `/ui/` 的新能力是否真的是「非管理面读数」？管理面的正式资源不该借 `/ui/`
      从那份已发布路由清单里溜出去。
- [ ] 冲突检查是否被意外地收窄到空间内？模型名冲突必须保持**全局**。
- [ ] 空分组的两处口径是否仍然一致？（`configops.writeWorkspaceTasks` 的
      `hasWorkspaceCredential` 与 `config.parseTasks`；带 `api_key`、`inference_key`
      或 `models` 的空间在**两处**都必须留下）
- [ ] 面板 key 是否仍然只从**请求头**解析，且生效时忽略 `X-AMKR-Workspace`？
- [ ] 推理 key 是否同样钉死空间、忽略请求头，且走的仍是「先完整权限、再访问密钥、最后
      作用域凭据」这个顺序？（顺序反了会让一把空间 key 变成管理员凭据）
- [ ] 模型清单是否在**别名解析之后**判定，且 `/v1/models` 的收窄与 proxy 的判定一致？
- [ ] 两把 key 的占用表是否仍然**跨类型**统一检查？（面板 key 撞推理 key、以及任一者撞
      访问密钥，都必须非法）
- [ ] 新的响应体是否泄漏了任一 key？（目录接口只给 `has_inference_key`
      布尔，不得返回 key）
- [ ] `webui/panel.js` 是否又碰了 `localStorage` 或发了 `X-AMKR-Workspace`？
      `webui_panel_probe.mjs` 会拦——它把存储访问做成了毒药记录器。
- [ ] 是否往指标库里加了**第二个**新对象？`extraMasterEntries` 白名单会拦住——兼容
      分歧必须逐条列出来，不能无声增长。
- [ ] 归属是否仍只在写入时确定？（查询期反推不成立，见第 8 节）
- [ ] `go test ./...`、`node webui/probes/webui_auth_probe.mjs`、
      `node webui/probes/webui_panel_probe.mjs` 与
      `node webui/probes/webui_chart_probe.mjs` 是否全绿？
