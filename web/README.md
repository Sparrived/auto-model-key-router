# AMKR WebUI

AMKR 的 React 19 + TypeScript + Vite 前端。源码在 `web/`，构建产物写到仓库根的 `webui/`，由 Go 侧 `//go:embed` 原样嵌入二进制；运行 AMKR 不需要 Node.js。

设计规格见 [`docs/design/webui-v7.md`](../docs/design/webui-v7.md)。实现前先读第 0–6 节，再有针对性地看自己负责的页面小节。

## 开发

需要 Node.js 22+。

```bash
cd web
npm ci
npm run dev
```

Vite 默认监听 `http://127.0.0.1:5173`，把 `/api`、`/metrics`、`/health`、`/v1`、`/ws`、`/ui` 代理到本机 AMKR。默认目标是 `http://127.0.0.1:18900`，可用 `AMKR_DEV_TARGET` 覆盖：

```bash
AMKR_DEV_TARGET=http://127.0.0.1:19000 npm run dev
```

## 测试

```bash
npm run typecheck   # tsc --noEmit
npm test            # vitest run
```

测试用 Vitest + jsdom + Testing Library，不访问真实网络或本机服务。

## 构建

```bash
npm run build
```

先跑 TypeScript 检查，再由 Vite 构建到仓库根的 `webui/`。产物是发布资产，**必须随源码一并提交**；CI 会在构建后检查 `git status --porcelain -- webui` 为空。不要在 Go 测试正在使用旧 `webui/` 时覆盖构建，除非你就是来更新这份产物的。

## 目录结构

```text
web/
  index.html  login.html  panel.html  guest.html   # 四个入口
  public/                                          # 静态资源（favicon 等）
  src/
    entries/       # 四个入口的 React 挂载点
    app/           # 外壳、路由、鉴权、查询客户端
    components/    # ui 原语、charts、filters
    lib/           # API、类型、格式、图表数学、主题、品牌图标
    pages/         # 每个页面一个目录
    styles/        # Tailwind 入口与设计令牌
    test/          # 测试 setup
```

## 四个入口的安全边界

- `index.html`：主控制台。本地 API Key 存 `localStorage["amkr.apiKey"]`，只在同源管理请求中使用；不得把完整 Key 渲染到页面。
- `login.html`：只负责本地鉴权。`next` 只接受同源路径；失效凭据不得在主界面保留表单。登录页地址带 `next` 时加 `noindex`。
- `panel.html`：工作空间可嵌入面板。凭据**只**从 URL fragment（`#k=`）读取，绝不读写 `localStorage`，绝不发送 `X-AMKR-Workspace`（空间由 Key 钉死）。
- `guest.html`：访问密钥使用看板。只使用自己的存储键，绝不读写 `amkr.apiKey`，绝不发写请求、绝不展示明文 Key；401 与 403 给出不同指引。

`panel.html` 与 `guest.html` 都必须保持可达，不能当成旧版 visitor UI 删除。
