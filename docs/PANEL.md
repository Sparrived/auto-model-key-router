# 嵌入工作空间面板

面向**接入方开发者**的集成指南：把一个只看得到自己那个工作空间的控制面板嵌进你自己的
后台。它讲清楚怎么取 key、怎么嵌、边界在哪、凭据为什么这样传，以及出问题时怎么排查。

工作原理与设计取舍见 [`WORKSPACE.md` 第 9 节](WORKSPACE.md#9-面板-key-与可嵌入面板)；
逐字段的接口契约见 [`API.md`](API.md#get-uiworkspace-paneljson)；操作步骤（含 WebUI
路径）见 [`USAGE.md` 9.3](USAGE.md#93-把工作空间面板嵌进你自己的后台)。

## 1. 它解决什么

你的应用已经在用 AMKR 转发模型请求，并且把任务路由按自己的业务分了组（一个工作空间一
组）。现在你想在后台里放一块页面，让运营能**自己看用量、自己调任务**，而不必把 AMKR
的完整管理凭据交出去——那等于把整个实例交出去。

面板就是为此存在的：它是一张独立的精简页，用一把**只对该工作空间有效**的 key 鉴权。

| 面板能做 | 面板不能做 |
| --- | --- |
| 读写**本空间**的任务（增/删/改/查） | 看或改别的空间的任务 |
| 读本空间的用量与请求流向 | 看供应商、Key、模型、设置等任何全局配置 |
| | 用 `/v1/*` 代理面（面板 key 不是推理凭据） |
| | 导出/导入配置、迁移工作空间、给自己扩权 |

> **面板 key 与推理 key 是两把不同的凭据。** 面板 key（`amkr_ws_…`）只用于本页描述的
> 面板场景；要让**项目代码**调 `/v1` 推理，用同一空间另一把**推理 key**（`amkr_ik_…`，
> 同一次创建响应里一起给出）。两者互不通用，用错会拿到 `401`。想让某个项目只能直呼
> 少数模型，在 WebUI 的任务路由页点该空间的「模型授权」配置。

## 2. 三步接入

### 第一步：拿到一把 key

**方式 A —— 应用侧创建（推荐，可脚本化）**

```bash
curl -X POST http://127.0.0.1:8000/api/workspaces \
  -H "Authorization: Bearer amkr_your-local-api-key" \
  -H "Content-Type: application/json" \
  -d '{"config_revision": "<当前版本号>", "name": "myapp-prod", "api_key": "你自己定的key"}'
```

`api_key` 可以省略，服务端会生成一个（`amkr_ws_` + 43 位随机字符）；`inference_key`
**总是**由服务端生成（`amkr_ik_` + 43 位随机字符）。响应 `201`：

```json
{"name": "myapp-prod", "task_count": 0, "api_key": "amkr_ws_…",
 "inference_key": "amkr_ik_…", "config_revision": "…"}
```

`config_revision` 从任意读接口拿（如 `GET /api/workspaces` 的响应里就有）。它是乐观锁：
版本对不上会拒绝，防止你的脚本基于过期配置写坏别的东西。

**方式 B —— 在 AMKR 的 WebUI 里手工创建**

任务路由页点「＋ 新建工作空间…」，建好后会弹出**两把** key 并给出可复制的嵌入片段。
适合人工搭一次环境。

> **两把 key 的明文都只出现这一次。** 之后任何接口都不再返回它们——工作空间目录
> （`GET /api/workspaces`）刻意不含它们（只给一个 `has_inference_key` 布尔），配置导出
> 也会剥掉它们。请在这一步存进你的密钥管理。
>
> 补救手段不同：推理 key 可以**轮换**（`POST /api/workspaces/{空间}/inference-key`，
> 只换这一把），面板 key 只能直接读 AMKR 的配置文件（`workspaces.<空间>.api_key`）或
> 重建空间。

### 第二步：塞进 iframe

```html
<iframe
  src="https://amkr.internal:8000/ui/panel.html#k=amkr_ws_你的key"
  width="100%"
  height="720"
  style="border:0"
  title="AMKR 工作空间面板"
></iframe>
```

| 项 | 说明 |
| --- | --- |
| `/ui/panel.html` | 面板页。挂在 AMKR 的 WebUI 前缀下，因此要求服务端 `webui_enabled: true` |
| `#k=<key>` | 凭据。**必须在 fragment 里**，理由见第 4 节 |
| `#api=<地址>` | 可选，仅当面板页与 AMKR 不同源时；见第 5 节 |
| `height` | 面板会自适应容器宽度，请给足高度（建议 ≥ 640px） |

面板地址固定是 `<AMKR 基址>/ui/panel.html`。`/ui` 这个路径段本身不随部署变化——它是
WebUI 的挂载路径（`webui.Path`）。只有把 AMKR 当 Go 库嵌进别的进程、并显式设了
`server.Options.MountPrefix` 时才会多一层前缀（如 `/amkr/ui/panel.html`）；独立运行的
二进制从不设它。反过来说，如果你给 AMKR 配了反向代理的子路径，**是代理在做重写**，
面板的实际路径仍以 AMKR 自己看到的路由为准。

### 第三步：确认它通了

面板加载后会打两个请求：`GET /ui/workspace-panel.json`（读数）与 `GET /api/tasks`（任务
列表）。两个都 200 就说明 key 有效。

```
面板打开 → 顶部显示工作空间名
         有任务则列出；没有任务显示空态（可以就地新建）
         用量卡片与流向链条显示数字
```

## 3. 权限边界：为什么 key 被"钉死"

面板 key 生效时，**`X-AMKR-Workspace` 请求头会被忽略**——空间完全由 key 决定。

这不是实现细节，是这个模式的安全基石：面板 key 会出现在被嵌入页面的 URL 里，而 URL 会
被复制、贴进聊天记录、写进前端源码、出现在浏览器历史里。它是最可能泄漏的位置。如果请求
头能换空间，一把泄漏的 key 就等于**所有空间**的任务面权限。

同理，面板 key 不能钉在默认空间上：配置里没有 `workspaces.default` 这个槽位，也不该为它
开一个。

由此推出两条实践建议：

- **一把 key 一个环境。** 别在生产与测试之间复用；泄漏一把只影响一个空间。
- **前端不要发空间头。** 服务端会忽略它，但发了会让读代码的人以为换个头能换空间。

## 4. 凭据为什么走 `#` 而不是 `?`

写成 `?k=…` 会让明文 key 出现在两个地方：

- **`Referer` 头**——嵌你的页面的第三方资源（CDN、字体、统计脚本）都会收到它；
- **服务端访问日志**——AMKR 的日志、以及中间任何反向代理的日志。

URL fragment（`#` 之后的部分）**不会**被浏览器发给服务端，因此两种泄漏都不会发生。
这是硬要求，不是风格偏好。

面板页自身也遵守同一套约束，你可以据此审查集成：

| 规则 | 原因 |
| --- | --- |
| **绝不读写 `localStorage` / `sessionStorage`** | 面板与 AMKR 的 WebUI **同源**，而 WebUI 把本地管理 key 存在 `localStorage.amkr.apiKey`。面板一旦碰存储，两个凭据会互相覆盖；更糟的是，一个嵌进第三方后台的页面就能读到你的完整管理凭据 |
| **绝不发送 `X-AMKR-Workspace`** | 见第 3 节 |
| **只从 fragment 取凭据** | 见上 |

三条都由 `webui/probes/webui_panel_probe.mjs` 断言——它把存储访问做成了毒药记录器，任何
一次触碰都会让探针失败。你自己改面板代码时也能靠它兜底。

一个相关的实现细节：面板在没有 key 时会先要求输入，用户手填之后键会被**写回 fragment**
（`history.replaceState`）而不是存进任何存储。这样刷新仍然可用，同时用 `replaceState`
而不是 `pushState` 避免在浏览历史里堆出一串带凭据的地址。如果你自己做面板，可以照这个
思路处理"用户手填 key"的场景。

## 5. 关于 `api=` 与跨源（重要）

默认情况下面板**按自己的页面路径反推**接口基址（`/ui/panel.html` → 同源的 `/`）。这是
正常部署下唯一需要的行为。

`#api=https://amkr.example.com` 用来在**不同源**时指定 AMKR 的地址。但请注意：

> **AMKR 不发送任何 CORS 响应头。**

因此浏览器**不允许**面板页从另一个源直接读取 AMKR 的接口响应——预检或实际请求会被同源
策略挡下，面板会显示网络错误。也就是说 `api=` 只在这些情况下真的能用：

- 你通过**同源的反向代理**把 AMKR 暴露在自己域名下（例如 `https://app.example.com/amkr/`
  转发到 AMKR），此时面板与接口仍然同源，其实不需要 `api=`；
- 你的宿主环境**关掉了同源策略**（例如 Electron 应用、或你完全控制的 WebView）。

**如果你的 AMKR 是独立域名**，正确做法是把它挂到自己域名下的一个路径（nginx/Caddy 反代），
而不是靠 `api=` 跨源直连。这是刻意的取舍：为面板开一个 `Access-Control-Allow-Origin: *`
会把整套管理接口的跨源面一起打开，代价远大于收益。

## 6. iframe 嵌入没有被禁止

AMKR **没有**设置 `X-Frame-Options`，也没有 CSP `frame-ancestors`，因此面板默认可被任意
来源 iframe 嵌入。这是**有意的**：嵌入是它的全部用途，加白名单就得让你先把宿主域名配进
AMKR，而那与"凭据是拉取式的"模型重复。

安全性由 **key 的范围**承担，不由来源承担——一把 key 只能读写一个空间的任务和读数，且用
不了代理面。因此即使有人把面板嵌到别处，他能做的也只有那一个空间的事。

顺带一提，面板页带 `noindex, nofollow`：地址里带着凭据，不该被搜索引擎收录。

## 7. 自定义 UI：直接调接口

不想用现成的面板页（比如你要和自己的设计系统统一风格），可以只借这把 key，自己渲染。
面板调的就是标准的管理接口，没有任何私有协议。

### 读读数

```
GET /ui/workspace-panel.json?hours=24
Authorization: Bearer <面板 key>
```

也支持 `all_history=true` 替代 `hours`。响应里 `workspace` 是这把 key 钉死的空间名，
`models` 是可选择的模型名数组（模型 ID + 别名，与 `/v1/models` 同口径；上游模型名不在其中
——它只是"发给那个上游的名字"，不可调用），`links` 是流向图的连边。完整形状见
[`API.md`](API.md#get-uiworkspace-paneljson)。

`unattributed` 字段恒为零：没有归属的请求不属于任何空间，面板看不到也不该看到。

### 读写任务

```
GET    /api/tasks                      列出本空间任务
POST   /api/tasks                      新建
PUT    /api/tasks/{task_name}          更新
DELETE /api/tasks/{task_name}          删除
```

全部只要 `Authorization: Bearer <面板 key>`。写入类要带 `config_revision`（从上一个成功
响应里取，任何管理接口的响应都有）。

新建/更新的请求体字段：

| 字段 | 说明 |
| --- | --- |
| `name` | 任务名（仅新建时）。这就是客户端要传的 `model` |
| `model` | 首选模型。可省略 → 占位任务，被调用时明确报「尚未指定模型」，**不会**回落到别的模型 |
| `display_name` | 给人看的中文名，不影响调用 |
| `fallback_model` | 可选；首选失败后切换。没有首选时不能填 |
| `params` | 固定采样参数：`temperature`、`top_p`、`top_k`、`frequency_penalty`、`presence_penalty`、`seed`、`stop`、`max_tokens`、`reasoning_effort` |

**更新时要小心 `params` 的省略语义**：`PUT` 里不传 `params` 表示"不动它"，传了就是整体
替换。现成的面板页因此在更新时**刻意不传** `params`，免得把运营在 AMKR 里配好的采样参数
悄悄清掉。你自己实现时请照做，除非你确实要改参数。

### 错误约定

管理接口的错误是 `{"detail": "…"}`（`422` 时 `detail` 是字段错误数组）。两个状态码值得单独
处理：

- **`401`**：key 无效或空间已被删除。应提示"凭据失效"并让人换 key，**不要**当成服务故障
  重试。
- **`409`**：任务名已存在（同一空间内重名）。让用户改名。

## 8. key 的生命周期

| 操作 | 对 key 的影响 |
| --- | --- |
| 重命名工作空间（`PUT /api/workspaces/{空间}`） | **key 跟着走**，面板不受影响（空间名变了，但 key 仍有效） |
| 删除工作空间（`DELETE /api/workspaces/{空间}`） | **key 立即失效**，面板开始 401 |
| 配置导出/导入（`/api/config/*`） | 导出**剥掉** key，导入后原 key 不再存在 |
| 工作空间整包迁移的**覆盖**导入 | 用包里的 key 替换，**旧 key 立即失效** |
| 工作空间整包迁移的**克隆**导入（带 `prefix`） | 原空间保留原 key，克隆体拿到**新** key |

后两条是你要留意的：迁移的导入响应会明确告诉你哪些空间被覆盖（`replaced`）、哪些拿到了
新 key（`rekeyed`）。收到这些名字时，记得同步更新你侧的嵌入片段。

## 9. 排查

| 现象 | 多半是什么 |
| --- | --- |
| 页面本身 404 | AMKR 的 `webui_enabled` 是 `false`（面板是 `/ui/` 下的静态资源）。用 `POST /api/tool/webui` 或 `--webui` 打开，**需要重启服务**才挂载 |
| 面板要求输入 key | fragment 里没有 `k`。注意 `#` 后面才是 fragment；URL 被转义或被截断都会丢 |
| 一直 401 | key 写错、空间被删、或被覆盖导入换掉了。用 `GET /ui/workspace-panel.json` 直接试这把 key，能区分"key 失效"和"页面问题" |
| 面板显示连接失败 | 面板页与接口不同源，而 AMKR 不发 CORS 头。见第 5 节：改用同源反代 |
| 数字全是 0 | 可能真的没有流量；也可能是这些请求在 AMKR 侧没有工作空间归属（升级前的历史行永远是 `unattributed`，而面板看不到那一栏） |
| 改了任务但参数没了 | 你更新时传了 `params`。不传才是"保持原样"，见第 7 节 |
| 宿主页面高度不对 | iframe 给的是固定高度，面板不会反向撑开宿主。必要时用 `postMessage` 自己接（面板当前不发这类消息） |

自查命令（把地址与 key 换成你的）：

```bash
# 读数端点：200 说明 key 有效
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H 'Authorization: Bearer amkr_ws_你的key' \
  'http://127.0.0.1:8000/ui/workspace-panel.json?hours=24'

# 任务列表：应只含该空间的任务
curl -sS -H 'Authorization: Bearer amkr_ws_你的key' \
  http://127.0.0.1:8000/api/tasks
```

## 10. 最小可用示例

一个自包含的宿主页（把两个常量换成你的值即可）：

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <title>我的后台 · 模型用量</title>
    <style>
      body { margin: 0; font: 14px/1.6 system-ui, sans-serif; }
      header { padding: 16px 24px; border-bottom: 1px solid #e5e7eb; }
      iframe { display: block; width: 100%; height: 720px; border: 0; }
    </style>
  </head>
  <body>
    <header>模型用量与任务</header>
    <!-- key 写在 fragment 里：不会进 Referer，也不会进服务端日志 -->
    <iframe
      src="https://amkr.internal:8000/ui/panel.html#k=amkr_ws_你的key"
      title="AMKR 工作空间面板"
    ></iframe>
  </body>
</html>
```

生产环境请**不要**把 key 硬编码在前端源码里——从你的后端下发这个地址（或只下发 fragment），
否则任何能看到页面源码的人都能拿到它。

## 11. 改动面板时的检查清单

- [ ] 没有读写 `localStorage` / `sessionStorage`？（与管理面同源，共享存储会互相覆盖凭据）
- [ ] 没有发送 `X-AMKR-Workspace`？（空间由 key 钉死）
- [ ] 凭据仍只从 URL fragment 取？没有落到查询串或请求体里？
- [ ] 更新任务时没有顺手传 `params`？除非确实要改参数
- [ ] 面板是否仍只依赖 `/ui/workspace-panel.json` 与 `/api/tasks*`？没有偷偷加管理面接口？
- [ ] `node webui/probes/webui_panel_probe.mjs` 是否全绿？（存储毒药记录器会拦住前两条）
