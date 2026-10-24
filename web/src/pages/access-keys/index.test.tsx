// 访问密钥页：三态提交体、行内启停回滚、失配修复与 last_used_at 的三种显示。
//
// 三态是这一页唯一的安全边界：不传/null = 不限制，[] = 一个都不许，[...]= 限定。
// 这些断言逐字节检查请求体，避免把「全部禁止」折叠成「不限制」这种静默放权。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccessKeyEntry } from "@/lib/types";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import AccessKeysPage from "./index";

interface Call {
  method: string;
  path: string;
  body: Record<string, unknown> | undefined;
}

const PROVIDERS = [{ id: "openai" }, { id: "deepseek" }, { id: "anthropic" }];
const MODELS = [
  { id: "gpt-4o-mini", aliases: ["fast-mini"] },
  { id: "deepseek-chat", aliases: [] },
  { id: "claude-sonnet-4", aliases: [] },
];
const ROUTES = [
  { id: "gpt-4o-mini", aliases: ["fast-mini"], targets: [{ provider: "openai" }] },
  { id: "deepseek-chat", aliases: [], targets: [{ provider: "deepseek" }] },
  { id: "claude-sonnet-4", aliases: [], targets: [{ provider: "anthropic" }] },
];

let calls: Call[] = [];
let keys: AccessKeyEntry[] = [];
let revision = "rev-ak-1";
let failUpdates = new Set<string>();
let createdCounter = 0;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetch(): void {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = (init?.method || "GET").toUpperCase();
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ method, path: url.pathname, body });

    if (method === "GET" && url.pathname === "/api/access-keys") {
      return json({ access_keys: keys, config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/api/providers") {
      return json({ providers: PROVIDERS, config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/api/models") {
      return json({ models: MODELS, config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/api/routes") {
      return json({ routes: ROUTES, config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/ui/metric-dimensions.json") {
      return json({
        window: { from: null, to: "2026-10-24T16:00:00+08:00", hours: 24 },
        providers: [], models: [], requested_models: [], upstream_keys: [], workspaces: [], caller_types: [],
        access_keys: [
          { id: "ak-alice", name: "Alice 试用", requests: 12 },
          { id: "ak-bob", name: "Bob 团队", requests: 0 },
        ],
      });
    }
    if (method === "POST" && url.pathname === "/api/access-keys") {
      createdCounter += 1;
      const key: AccessKeyEntry = {
        id: `ak-created-${createdCounter}`,
        name: String(body?.name || ""),
        enabled: body?.enabled !== false,
        key_fingerprint: `fp-created-${createdCounter}`,
        ...(body?.providers !== undefined ? { providers: body.providers as string[] } : {}),
        ...(body?.models !== undefined ? { models: body.models as string[] } : {}),
      };
      keys = [...keys, key];
      return json({ ...key, key: "amkr_ak_created_secret", config_revision: revision }, 201);
    }
    if (method === "PUT" && url.pathname.startsWith("/api/access-keys/")) {
      const id = decodeURIComponent(url.pathname.split("/").at(-1) || "");
      if (failUpdates.has(id)) {
        // 留出一点时间让测试观察到乐观更新后的开关状态，再模拟服务端失败。
        await new Promise((resolve) => setTimeout(resolve, 120));
        return json({ detail: "注入的写入失败" }, 500);
      }
      const index = keys.findIndex((key) => key.id === id);
      if (index < 0) return json({ detail: "not found" }, 404);
      const next: AccessKeyEntry = { ...keys[index] };
      if (typeof body?.name === "string") next.name = body.name;
      if (typeof body?.enabled === "boolean") next.enabled = body.enabled;
      for (const field of ["providers", "models"] as const) {
        // 服务端的「不限制」回包是字段缺席，不是 null；mock 也照这个形状回。
        if (body?.[field] === null) delete next[field];
        else if (body?.[field] !== undefined) next[field] = body[field] as string[];
      }
      keys[index] = next;
      return json(next);
    }
    if (method === "POST" && url.pathname.endsWith("/rotate")) {
      const id = decodeURIComponent(url.pathname.split("/").at(-2) || "");
      return json({
        id,
        name: keys.find((key) => key.id === id)?.name || "",
        enabled: true,
        key_fingerprint: "fp-rotated",
        key: "amkr_ak_rotated_secret",
        config_revision: revision,
      });
    }
    if (method === "DELETE" && url.pathname.startsWith("/api/access-keys/")) {
      const id = decodeURIComponent(url.pathname.split("/").at(-1) || "");
      keys = keys.filter((key) => key.id !== id);
      return json({ config_revision: revision });
    }
    return json({ detail: `unexpected ${method} ${url.pathname}` }, 500);
  }));
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <ConfirmProvider>
        <SecretProvider>
          <AccessKeysPage />
        </SecretProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return { user };
}

async function row(name: RegExp): Promise<HTMLElement> {
  return screen.findByRole("row", { name });
}

async function closeSecret(user: ReturnType<typeof userEvent.setup>, title: string): Promise<void> {
  const dialog = await screen.findByRole("dialog", { name: title });
  await user.click(within(dialog).getByRole("checkbox", { name: "我已保存这些凭据" }));
  await user.click(within(dialog).getByRole("button", { name: "完成" }));
  await waitFor(() => {
    expect(screen.queryByRole("dialog", { name: title })).not.toBeInTheDocument();
  });
}

beforeEach(() => {
  calls = [];
  failUpdates = new Set();
  createdCounter = 0;
  keys = [
    {
      id: "ak-alice", name: "Alice 试用", enabled: true, key_fingerprint: "fp-alice",
      models: ["gpt-4o-mini", "fast-mini", "deepseek-chat"],
      last_used_at: "2026-10-24T12:41:38+08:00",
    },
    {
      id: "ak-bob", name: "Bob 团队", enabled: true, key_fingerprint: "fp-bob",
      providers: ["deepseek", "anthropic"], models: ["gpt-4o-mini"],
      last_used_at: null,
    },
    {
      id: "ak-ci", name: "CI 流水线", enabled: true, key_fingerprint: "fp-ci",
    },
    {
      id: "ak-old", name: "旧合作方", enabled: false, key_fingerprint: "fp-old",
      providers: [], models: [], last_used_at: null,
    },
  ];
  revision = "rev-ak-1";
  window.location.hash = "#/access-keys";
  installFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("访问密钥页", () => {
  it("last_used_at 三种显示：时间 / 暂无调用记录 / 后端未提供", async () => {
    renderPage();
    const alice = await row(/Alice 试用/);
    expect(within(alice).getByTitle(/北京时间 ·/)).toBeInTheDocument();

    const bob = await row(/Bob 团队/);
    expect(within(bob).getByText("暂无调用记录")).toBeInTheDocument();

    const ci = await row(/CI 流水线/);
    expect(within(ci).getByText("后端未提供")).toBeInTheDocument();
  });

  it("24 小时请求数取不到时显示 —，不显示 0", async () => {
    renderPage();
    const alice = await row(/Alice 试用/);
    expect(within(alice).getByText("12")).toBeInTheDocument();
    const bob = await row(/Bob 团队/);
    expect(within(bob).getByText("0")).toBeInTheDocument();
    const ci = await row(/CI 流水线/);
    expect(within(ci).getByText("—")).toBeInTheDocument();
  });

  it("新建访问密钥：指定清单 POST 提交排序后的数组", async () => {
    const { user } = renderPage();
    await screen.findByText("Alice 试用");
    await user.click(screen.getByRole("button", { name: "新建访问密钥" }));
    const sheet = await screen.findByRole("dialog", { name: "新建访问密钥" });
    await user.type(within(sheet).getByLabelText(/^名称/), "新密钥");

    const providerGroup = within(sheet).getByRole("radiogroup", { name: "允许的供应商范围" });
    await user.click(within(providerGroup).getByRole("radio", { name: "指定清单" }));
    await user.click(within(sheet).getByRole("checkbox", { name: /openai/ }));

    const modelGroup = within(sheet).getByRole("radiogroup", { name: "允许的模型范围" });
    await user.click(within(modelGroup).getByRole("radio", { name: "指定清单" }));
    await user.click(within(sheet).getByRole("checkbox", { name: "gpt-4o-mini" }));

    await user.click(within(sheet).getByRole("button", { name: "创建" }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === "POST" && call.path === "/api/access-keys")).toBe(true);
    });
    const call = calls.find((item) => item.method === "POST" && item.path === "/api/access-keys")!;
    expect(call.body).toMatchObject({ name: "新密钥", providers: ["openai"], models: ["gpt-4o-mini"] });

    const secret = await screen.findByRole("dialog", { name: "访问密钥已创建" });
    expect(within(secret).getByText("amkr_ak_created_secret")).toBeInTheDocument();
    await closeSecret(user, "访问密钥已创建");
    // 明文关闭后不再出现在列表里，列表只有指纹。
    expect(screen.queryByText("amkr_ak_created_secret")).not.toBeInTheDocument();
  });

  it("新建访问密钥：不限制时 POST 不传 providers/models 字段", async () => {
    const { user } = renderPage();
    await screen.findByText("Alice 试用");
    await user.click(screen.getByRole("button", { name: "新建访问密钥" }));
    const sheet = await screen.findByRole("dialog", { name: "新建访问密钥" });
    await user.type(within(sheet).getByLabelText(/^名称/), "全开密钥");
    await user.click(within(sheet).getByRole("button", { name: "创建" }));

    await waitFor(() => {
      expect(calls.some((call) => call.method === "POST" && call.path === "/api/access-keys")).toBe(true);
    });
    const call = calls.find((item) => item.method === "POST" && item.path === "/api/access-keys")!;
    expect(call.body).not.toHaveProperty("providers");
    expect(call.body).not.toHaveProperty("models");
  });

  it("新建访问密钥：两份清单都选全部禁止时 POST 提交空数组", async () => {
    const { user } = renderPage();
    await screen.findByText("Alice 试用");
    await user.click(screen.getByRole("button", { name: "新建访问密钥" }));
    const sheet = await screen.findByRole("dialog", { name: "新建访问密钥" });
    await user.type(within(sheet).getByLabelText(/^名称/), "全禁密钥");
    for (const label of ["允许的供应商范围", "允许的模型范围"]) {
      const group = within(sheet).getByRole("radiogroup", { name: label });
      await user.click(within(group).getByRole("radio", { name: "全部禁止" }));
    }
    await user.click(within(sheet).getByRole("button", { name: "创建" }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === "POST" && call.path === "/api/access-keys")).toBe(true);
    });
    const call = calls.find((item) => item.method === "POST" && item.path === "/api/access-keys")!;
    // 全部禁止必须显式为 []；漏字段会被服务端理解成不限制。
    expect(call.body?.providers).toEqual([]);
    expect(call.body?.models).toEqual([]);
  });

  it("编辑：不限制在 PUT 里是 null，切到全部禁止提交 []", async () => {
    const { user } = renderPage();
    const alice = await row(/Alice 试用/);
    await user.click(within(alice).getByRole("button", { name: "编辑" }));
    let sheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    await user.click(within(sheet).getByRole("button", { name: "保存" }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === "PUT" && call.path === "/api/access-keys/ak-alice")).toBe(true);
    });
    let call = calls.find((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-alice")!;
    expect(call.body?.providers).toBeNull();
    expect(call.body?.models).toEqual(["deepseek-chat", "fast-mini", "gpt-4o-mini"]);

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /编辑访问密钥/ })).not.toBeInTheDocument();
    });
    await user.click(within(await row(/Alice 试用/)).getByRole("button", { name: "编辑" }));
    sheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    const providerGroup = within(sheet).getByRole("radiogroup", { name: "允许的供应商范围" });
    await user.click(within(providerGroup).getByRole("radio", { name: "全部禁止" }));
    await user.click(within(sheet).getByRole("button", { name: "保存" }));

    await waitFor(() => {
      expect(calls.filter((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-alice").length).toBe(2);
    });
    call = calls.filter((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-alice").at(-1)!;
    expect(call.body?.providers).toEqual([]);
  });

  it("编辑：模型从清单切到不限制，PUT 提交 null", async () => {
    const { user } = renderPage();
    await user.click(within(await row(/Alice 试用/)).getByRole("button", { name: "编辑" }));
    const sheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    const modelGroup = within(sheet).getByRole("radiogroup", { name: "允许的模型范围" });
    await user.click(within(modelGroup).getByRole("radio", { name: "不限制" }));
    await user.click(within(sheet).getByRole("button", { name: "保存" }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === "PUT" && call.path === "/api/access-keys/ak-alice")).toBe(true);
    });
    const call = calls.find((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-alice")!;
    expect(call.body?.models).toBeNull();
    expect(call.body?.providers).toBeNull();
  });

  it("行内启用开关失败后回滚", async () => {
    failUpdates = new Set(["ak-alice"]);
    const { user } = renderPage();
    const alice = await row(/Alice 试用/);
    const toggle = within(alice).getByRole("switch");
    expect(toggle).toHaveAttribute("aria-checked", "true");
    await user.click(toggle);
    // 乐观更新先动。
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
    // 失败后回到服务端值。
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
    const call = calls.find((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-alice")!;
    expect(call.body).toMatchObject({
      name: "Alice 试用",
      enabled: false,
      providers: null,
      models: ["deepseek-chat", "fast-mini", "gpt-4o-mini"],
    });
  });

  it("失配提示与一键补授权", async () => {
    const { user } = renderPage();
    await user.click(within(await row(/Bob 团队/)).getByRole("button", { name: "编辑" }));
    const sheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    expect(within(sheet).getByText(/存在权限失配：1 个模型未获得所属供应商授权/)).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "一并授权缺失的供应商" }));
    await user.click(within(sheet).getByRole("button", { name: "保存" }));

    await waitFor(() => {
      expect(calls.some((call) => call.method === "PUT" && call.path === "/api/access-keys/ak-bob")).toBe(true);
    });
    const call = calls.find((item) => item.method === "PUT" && item.path === "/api/access-keys/ak-bob")!;
    expect(call.body?.providers).toEqual(["anthropic", "deepseek", "openai"]);
    expect(call.body?.models).toEqual(["gpt-4o-mini"]);
  });

  it("删除确认写明调用方会立刻收到 401", async () => {
    const { user } = renderPage();
    await user.click(within(await row(/Bob 团队/)).getByRole("button", { name: "删除" }));
    const dialog = await screen.findByRole("dialog", { name: "删除访问密钥" });
    expect(within(dialog).getByText(/使用它的调用方会立刻收到 401/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
  });

  it("查看用量跳转到 activity 并带 ak 深链", async () => {
    const { user } = renderPage();
    await user.click(within(await row(/Bob 团队/)).getByRole("button", { name: "查看用量" }));
    await waitFor(() => expect(window.location.hash).toBe("#/activity?ak=ak-bob"));
  });

  it("整行点击与回车打开编辑抽屉；开关和指纹复制不触发行", async () => {
    const { user } = renderPage();
    let keyRow = await row(/Bob 团队/);

    await user.click(keyRow);
    const firstSheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    await user.click(within(firstSheet).getByRole("button", { name: "取消" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /编辑访问密钥/ })).not.toBeInTheDocument();
    });

    keyRow = await row(/Bob 团队/);
    await user.click(within(keyRow).getByRole("switch"));
    expect(screen.queryByRole("dialog", { name: /编辑访问密钥/ })).not.toBeInTheDocument();

    await user.click(within(keyRow).getByRole("button", { name: "复制 fp-bob" }));
    expect(screen.queryByRole("dialog", { name: /编辑访问密钥/ })).not.toBeInTheDocument();

    keyRow.focus();
    await user.keyboard("{Enter}");
    const secondSheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });
    await user.click(within(secondSheet).getByRole("button", { name: "取消" }));
  });

  it("权限段只保留一句说明，全部禁止用 critical 文字点明后果", async () => {
    const { user } = renderPage();
    await user.click(within(await row(/Alice 试用/)).getByRole("button", { name: "编辑" }));
    const sheet = await screen.findByRole("dialog", { name: /编辑访问密钥/ });

    expect(within(sheet).getByText("不限制：允许全部供应商。")).toBeInTheDocument();
    expect(within(sheet).queryByText("不限制（允许全部）")).not.toBeInTheDocument();

    const providerGroup = within(sheet).getByRole("radiogroup", { name: "允许的供应商范围" });
    await user.click(within(providerGroup).getByRole("radio", { name: "全部禁止" }));
    const warning = within(sheet).getByText("全部禁止：这把密钥无法使用任何供应商，调用会直接失败。");
    expect(warning).toHaveClass("text-critical-ink");
  });
  
  it("范围最多显示两个芯片，完整清单保留在可访问说明里", async () => {
    renderPage();
    const alice = await row(/Alice 试用/);
    expect(within(alice).getByText("+1")).toBeInTheDocument();
    expect(within(alice).getByLabelText("模型范围：gpt-4o-mini、fast-mini、deepseek-chat")).toBeInTheDocument();
  });
  });
