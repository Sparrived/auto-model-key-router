import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, type RenderResult } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { ImpactProvider } from "@/components/ui/ImpactDialog";
import type {
  ModelSummaryResponse,
  Provider,
  QuotaFreeze,
  RouteEntry,
  UnifiedModel,
} from "@/lib/types";

export interface ApiCall {
  method: string;
  path: string;
  search: string;
  body: Record<string, unknown> | null;
}

export interface FakeState {
  providers: Provider[];
  routes: RouteEntry[];
  efforts: Record<string, string>;
  unified: UnifiedModel | null;
  revision: string;
  counter: number;
  /** 额度冻结表（GET /api/quota-freezes 的内容；默认空表）。 */
  freezes: QuotaFreeze[];
}

const clone = <T,>(value: T): T => structuredClone(value);

export function seedState(): FakeState {
  return {
    revision: "rev-1",
    counter: 1,
    efforts: { "gpt-4o-mini": "medium" },
    freezes: [],
    providers: [
      {
        id: "openai",
        base_url: "http://127.0.0.1:18901",
        max_concurrency: 12,
        routes: {},
        keys: [
          {
            name: "main",
            enabled: true,
            api_key_fingerprint: "fp-main",
            max_concurrency: 6,
            capabilities: { models: ["gpt-4o-mini", "gpt-4o"], checked_at: "2026-10-24T08:00:00Z" },
          },
          {
            name: "backup",
            enabled: true,
            api_key_fingerprint: "fp-backup",
            max_concurrency: null,
            capabilities: { models: ["gpt-4o"], checked_at: "2026-10-24T08:00:00Z" },
          },
        ],
      },
      {
        id: "deepseek",
        base_url: "https://api.deepseek.com",
        max_concurrency: null,
        routes: {},
        keys: [
          {
            name: "main",
            enabled: true,
            api_key_fingerprint: "fp-ds",
            max_concurrency: null,
            capabilities: { models: ["deepseek-chat", "deepseek-reasoner"], checked_at: "2026-10-24T08:00:00Z" },
          },
        ],
      },
    ],
    routes: [
      {
        id: "gpt-4o-mini",
        aliases: ["fast-mini"],
        routing_mode: "round_robin",
        targets: [
          { provider: "openai", key: "main", upstream_model: "gpt-4o-mini" },
          { provider: "deepseek", key: "main", upstream_model: "deepseek-chat" },
        ],
        endpoints: null,
      },
      {
        id: "gpt-4o",
        aliases: [],
        routing_mode: "priority",
        targets: [
          { provider: "openai", key: "main", upstream_model: "gpt-4o" },
          { provider: "openai", key: "backup", upstream_model: "gpt-4o" },
        ],
        endpoints: { image: { primary: { model: "gpt-4o", key: null } } },
      },
      {
        id: "deepseek-chat",
        aliases: ["ds"],
        routing_mode: "priority",
        targets: [{ provider: "deepseek", key: "main", upstream_model: "deepseek-chat" }],
        endpoints: null,
      },
    ],
    unified: {
      default: {
        primary: { model: "gpt-4o-mini", key: null },
        fallback: { model: "deepseek-chat", key: null },
      } as unknown as UnifiedModel['default'],
      image: null,
      embeddings: { primary: { model: "gpt-4o-mini", key: null } },
      speech: null,
      transcriptions: null,
      video: null,
      rerank: null,
    },
  };
}

export function buildModels(state: FakeState): ModelSummaryResponse[] {
  return state.routes.map((route) => {
    const bindings = new Map<string, { name: string; enabled?: boolean }>();
    for (const target of route.targets) {
      const provider = state.providers.find((item) => item.id === target.provider);
      const key = provider?.keys.find((item) => item.name === target.key);
      bindings.set(target.key, { name: target.key, enabled: key?.enabled });
    }
    return {
      id: route.id,
      aliases: route.aliases || [],
      routing_mode: route.routing_mode,
      reasoning_effort: state.efforts[route.id] ?? null,
      keys: [...bindings.values()],
      endpoints: route.endpoints || null,
    };
  });
}

function kindOfModel(name: string): { kind: string; endpoint: string } {
  if (name.includes("embedding")) return { kind: "embedding", endpoint: "embeddings" };
  if (name.includes("image") || name.includes("dall")) return { kind: "image", endpoint: "images" };
  if (name.includes("tts") || name.includes("speech")) return { kind: "tts", endpoint: "speech" };
  if (name.includes("whisper")) return { kind: "stt", endpoint: "transcriptions" };
  if (name.includes("rerank")) return { kind: "rerank", endpoint: "rerank" };
  if (name.includes("video")) return { kind: "video", endpoint: "video" };
  return { kind: "text", endpoint: "chat" };
}

export function createFakeBackend(seed: Partial<FakeState> = {}) {
  const state: FakeState = { ...seedState(), ...clone(seed) };
  const calls: ApiCall[] = [];

  function bump(): string {
    state.counter += 1;
    state.revision = `rev-${state.counter}`;
    return state.revision;
  }

  function response(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }

  function conflict(message = "config revision 冲突"): Response {
    return response({ error: message }, 409);
  }

  function checkRevision(body: Record<string, unknown> | null): boolean {
    return !body?.config_revision || body.config_revision === state.revision;
  }

  function providerOf(id: string): Provider | undefined {
    return state.providers.find((item) => item.id === id);
  }

  function keyOf(provider: Provider, name: string) {
    return provider.keys.find((item) => item.name === name);
  }

  function updateReferences(oldId: string, newId: string) {
    for (const route of state.routes) {
      for (const target of route.targets) {
        if (target.provider === oldId) target.provider = newId;
        if (target.key && target.provider === newId && !keyOf(providerOf(newId) as Provider, target.key)) {
          // 供应商改名不影响 Key 名。
        }
      }
    }
    if (state.unified) {
      const plans = state.unified as Record<string, { primary?: { model?: string }; fallback?: { model?: string } } | null>;
      for (const plan of Object.values(plans)) {
        if (plan?.primary?.model === oldId) plan.primary.model = newId;
        if (plan?.fallback?.model === oldId) plan.fallback.model = newId;
      }
    }
  }

  async function handle(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = new URL(typeof input === "string" ? input : input.toString(), "http://localhost");
    const path = decodeURIComponent(url.pathname);
    const method = (init?.method || "GET").toUpperCase();
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : null;
    calls.push({ method, path, search: url.search, body });

    if (path === "/ui/model-kinds.json" && method === "GET") {
      const names = url.searchParams.getAll("model");
      const models: Record<string, unknown> = {};
      for (const name of names) {
        const info = kindOfModel(name);
        models[name] = { kinds: [info.kind], primary: info.kind, endpoints: [info.endpoint] };
      }
      return response({ catalog_available: true, models });
    }

    if (path === "/api/quota-freezes" && method === "GET") {
      // 真实后端按内置计时器懒惰清理到期冻结；假后端也过滤一次，才能验收
      // 「到点 refetch 后徽标自己消失」。
      const now = Date.now();
      return response({
        fetched_at: "2026-10-24T17:00:00+08:00",
        enabled: true,
        interval_seconds: 60,
        last_evaluated_at: "2026-10-24T17:00:00+08:00",
        last_error: "",
        freezes: clone(state.freezes).filter((freeze) => {
          const expires = Date.parse(String(freeze.expires_at ?? ""));
          return !Number.isFinite(expires) || expires > now;
        }),
      });
    }
    const unfreeze = path.match(/^\/api\/quota-freezes\/([^/]+)\/([^/]+)$/);
    if (unfreeze && method === "DELETE") {
      const before = state.freezes.length;
      state.freezes = state.freezes.filter(
        (freeze) => !(freeze.provider_id === unfreeze[1] && freeze.key_name === unfreeze[2]),
      );
      if (state.freezes.length === before) return response({ detail: "当前没有冻结" }, 404);
      return response({
        released: true,
        provider_id: unfreeze[1],
        key_name: unfreeze[2],
        suppressed_until: "2026-10-24T17:10:00+08:00",
      });
    }

    if (path === "/api/providers" && method === "GET") {
      return response({ providers: clone(state.providers), config_revision: state.revision });
    }
    if (path === "/api/providers" && method === "POST") {
      if (!checkRevision(body)) return conflict();
      const id = String(body?.id || "");
      if (providerOf(id)) return conflict("供应商已存在");
      const provider: Provider = { id, base_url: String(body?.base_url || ""), max_concurrency: null, routes: {}, keys: [] };
      state.providers.push(provider);
      bump();
      return response(provider);
    }

    let match = path.match(/^\/api\/providers\/([^/]+)\/keys\/([^/]+)\/models$/);
    if (match && method === "GET") {
      const provider = providerOf(match[1]);
      if (!provider) return response({ error: "not found" }, 404);
      const models = state.routes
        .filter((route) => route.targets.some((target) => target.provider === match![1] && target.key === match![2]))
        .map((route) => route.id);
      return response({ models, config_revision: state.revision });
    }
    if (match && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      const key = provider ? keyOf(provider, match[2]) : undefined;
      if (!provider || !key) return response({ error: "not found" }, 404);
      const nextModels = (body?.models as string[] | undefined) || [];
      const current = state.routes
        .filter((route) => route.targets.some((target) => target.provider === provider.id && target.key === key.name))
        .map((route) => route.id);
      const removed = current.filter((model) => !nextModels.includes(model));
      if (url.searchParams.get("dry_run") === "1") {
        return response({ config_revision: state.revision, removed_models: removed });
      }
      for (const route of state.routes) {
        route.targets = route.targets.filter((target) => !(target.provider === provider.id && target.key === key.name));
      }
      state.routes = state.routes.filter((route) => route.targets.length > 0);
      for (const model of nextModels) {
        let route = state.routes.find((item) => item.id === model);
        if (!route) {
          route = { id: model, aliases: [], routing_mode: "", targets: [], endpoints: null };
          state.routes.push(route);
        }
        if (!route.targets.some((target) => target.provider === provider.id && target.key === key.name)) {
          route.targets.push({ provider: provider.id, key: key.name, upstream_model: model });
        }
      }
      bump();
      return response({ config_revision: state.revision });
    }

    match = path.match(/^\/api\/providers\/([^/]+)\/keys\/([^/]+)\/probe$/);
    if (match && method === "POST") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      const key = provider ? keyOf(provider, match[2]) : undefined;
      if (!provider || !key) return response({ error: "not found" }, 404);
      const models = provider.id === "openai" ? ["gpt-4o-mini", "gpt-4o"] : ["deepseek-chat"];
      key.capabilities = { models, checked_at: new Date().toISOString() };
      bump();
      return response({ status: "complete", config_revision: state.revision });
    }

    match = path.match(/^\/api\/providers\/([^/]+)\/keys\/([^/]+)$/);
    if (match && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      const key = provider ? keyOf(provider, match[2]) : undefined;
      if (!provider || !key) return response({ error: "not found" }, 404);
      if (body?.name && body.name !== key.name) {
        const nextName = String(body.name);
        if (keyOf(provider, nextName)) return conflict("Key 已存在");
        for (const route of state.routes) {
          for (const target of route.targets) {
            if (target.provider === provider.id && target.key === key.name) target.key = nextName;
          }
        }
        key.name = nextName;
      }
      if ("enabled" in (body || {})) key.enabled = Boolean(body?.enabled);
      if ("max_concurrency" in (body || {})) key.max_concurrency = (body?.max_concurrency as number | null) ?? null;
      if (body?.api_key) key.api_key_fingerprint = `fp-${String(body.api_key).slice(0, 4)}`;
      bump();
      return response(key);
    }
    if (match && method === "DELETE") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      const key = provider ? keyOf(provider, match[2]) : undefined;
      if (!provider || !key) return response({ error: "not found" }, 404);
      const affected = state.routes.filter((route) => route.targets.some((target) => target.provider === provider.id && target.key === key.name)).map((route) => route.id);
      if (url.searchParams.get("dry_run") === "1") {
        return response({ config_revision: state.revision, removed_models: affected });
      }
      provider.keys = provider.keys.filter((item) => item.name !== key.name);
      for (const route of state.routes) {
        route.targets = route.targets.filter((target) => !(target.provider === provider.id && target.key === key.name));
      }
      state.routes = state.routes.filter((route) => route.targets.length > 0);
      bump();
      return response({ config_revision: state.revision });
    }

    match = path.match(/^\/api\/providers\/([^/]+)\/keys$/);
    if (match && method === "POST") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      if (!provider) return response({ error: "not found" }, 404);
      const name = String(body?.name || "");
      if (keyOf(provider, name)) return conflict("Key 已存在");
      const key = {
        name,
        enabled: true,
        api_key_fingerprint: `fp-${name}`,
        max_concurrency: "max_concurrency" in (body || {}) ? body?.max_concurrency as number | null : null,
        capabilities: null,
      };
      provider.keys.push(key);
      bump();
      return response(key);
    }

    match = path.match(/^\/api\/providers\/([^/]+)$/);
    if (match && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      if (!provider) return response({ error: "not found" }, 404);
      const nextId = String(body?.id || provider.id);
      if (nextId !== provider.id && providerOf(nextId)) return conflict("供应商已存在");
      provider.id = nextId;
      provider.base_url = String(body?.base_url || provider.base_url);
      provider.max_concurrency = (body?.max_concurrency as number | null) ?? null;
      const routes = (body?.routes || {}) as Record<string, string | null>;
      provider.routes = provider.routes || {};
      for (const [mode, value] of Object.entries(routes)) {
        if (value === null) delete provider.routes[mode];
        else provider.routes[mode] = value;
      }
      if (nextId !== match![1]) updateReferences(match![1], nextId);
      bump();
      return response(provider);
    }
    if (match && method === "DELETE") {
      if (!checkRevision(body)) return conflict();
      const provider = providerOf(match[1]);
      if (!provider) return response({ error: "not found" }, 404);
      const affected = state.routes.filter((route) => route.targets.some((target) => target.provider === provider.id)).map((route) => route.id);
      if (url.searchParams.get("dry_run") === "1") {
        return response({ config_revision: state.revision, removed_models: affected });
      }
      state.providers = state.providers.filter((item) => item.id !== provider.id);
      for (const route of state.routes) {
        route.targets = route.targets.filter((target) => target.provider !== provider.id);
      }
      state.routes = state.routes.filter((route) => route.targets.length > 0);
      bump();
      return response({ config_revision: state.revision });
    }

    if (path === "/api/routes" && method === "GET") {
      return response({ routes: clone(state.routes), config_revision: state.revision });
    }
    if (path === "/api/routes" && method === "POST") {
      if (!checkRevision(body)) return conflict();
      const id = String(body?.id || "");
      if (state.routes.some((route) => route.id === id)) return conflict("模型名称已存在");
      const route: RouteEntry = {
        id,
        aliases: (body?.aliases as string[]) || [],
        routing_mode: (body?.routing_mode as string) || "",
        targets: (body?.targets as RouteEntry["targets"]) || [],
        endpoints: (body?.endpoints as RouteEntry["endpoints"]) ?? null,
      };
      state.routes.push(route);
      bump();
      return response(route);
    }

    match = path.match(/^\/api\/routes\/([^/]+)$/);
    if (match && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const route = state.routes.find((item) => item.id === match![1]);
      if (!route) return response({ error: "not found" }, 404);
      const nextId = body?.id ? String(body.id) : route.id;
      if (nextId !== route.id && state.routes.some((item) => item.id === nextId)) return conflict("模型名称已存在");
      route.id = nextId;
      route.aliases = (body?.aliases as string[]) || [];
      route.routing_mode = (body?.routing_mode as string) || "";
      route.targets = (body?.targets as RouteEntry["targets"]) || [];
      if ("endpoints" in (body || {})) route.endpoints = body?.endpoints as RouteEntry["endpoints"];
      if (!route.targets.length) state.routes = state.routes.filter((item) => item !== route);
      if (nextId !== match![1]) updateReferences(match![1], nextId);
      bump();
      return response(route);
    }
    if (match && method === "DELETE") {
      if (!checkRevision(body)) return conflict();
      const route = state.routes.find((item) => item.id === match![1]);
      if (!route) return response({ error: "not found" }, 404);
      if (url.searchParams.get("dry_run") === "1") {
        return response({ config_revision: state.revision, removed_models: [route.id] });
      }
      state.routes = state.routes.filter((item) => item.id !== route.id);
      if (state.unified) {
        const plans = state.unified as Record<string, { primary?: { model?: string }; fallback?: { model?: string } } | null>;
        for (const plan of Object.values(plans)) {
          if (plan?.primary?.model === route.id) plan.primary.model = "";
          if (plan?.fallback?.model === route.id) plan.fallback = { model: "" };
        }
      }
      bump();
      return response({ config_revision: state.revision });
    }

    if (path === "/api/models" && method === "GET") {
      return response({ models: buildModels(state), config_revision: state.revision });
    }
    match = path.match(/^\/api\/models\/([^/]+)$/);
    if (match && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const id = match[1];
      if (!state.routes.some((route) => route.id === id)) return response({ error: "not found" }, 404);
      state.efforts[id] = (body?.reasoning_effort as string | null) || "";
      bump();
      return response({ id, config_revision: state.revision });
    }

    if (path === "/api/unified-model" && method === "GET") {
      return response({ unified_model: clone(state.unified), config_revision: state.revision });
    }
    if (path === "/api/unified-model" && method === "PUT") {
      if (!checkRevision(body)) return conflict();
      const next = { ...(state.unified || {}) };
      for (const key of ["default", "image", "embeddings", "speech", "transcriptions", "video", "rerank"]) {
        if (key in (body || {})) (next as Record<string, unknown>)[key] = body?.[key] ?? null;
      }
      state.unified = next;
      bump();
      return response({ unified_model: state.unified, config_revision: state.revision });
    }
    if (path === "/api/unified-model" && method === "DELETE") {
      if (!checkRevision(body)) return conflict();
      state.unified = null;
      bump();
      return response({ config_revision: state.revision });
    }

    if (path === "/api/probes/keys" && method === "POST") {
      return response({
        probe_id: "probe-1",
        status: "complete",
        results: [{ key_name: "main", endpoint: "http://127.0.0.1:18901/v1/models?x=1", models: ["gpt-4o-mini"], latency_ms: 12 }],
      });
    }
    if (path === "/api/probes/probe-1" && method === "GET") {
      return response({ probe_id: "probe-1", status: "complete", results: [{ key_name: "main", models: ["gpt-4o-mini"] }] });
    }
    if (path === "/api/probes/probe-1/cancel" && method === "POST") {
      return response({ probe_id: "probe-1", status: "cancelled", results: [] });
    }

    return response({ error: `unhandled ${method} ${path}` }, 500);
  }

  return {
    state,
    calls,
    fetch: vi.fn(handle),
    snapshot: () => ({ providers: clone(state.providers), config_revision: state.revision }),
    resetCalls: () => { calls.length = 0; },
  };
}

export function installBackend(backend: ReturnType<typeof createFakeBackend>) {
  vi.stubGlobal("fetch", backend.fetch);
}

export function renderWithProviders(ui: ReactElement): RenderResult {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={client}>
        <ConfirmProvider>
          <ImpactProvider>{children}</ImpactProvider>
        </ConfirmProvider>
      </QueryClientProvider>
    );
  }
  return render(ui, { wrapper: Wrapper });
}
