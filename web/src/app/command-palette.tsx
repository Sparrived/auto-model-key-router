import { useEffect, useMemo, useRef } from "react";
import { Command } from "cmdk";
import { Dialog } from "radix-ui";
import { useQueryClient } from "@tanstack/react-query";
import { Check, CornerDownLeft, Search, Server, Settings, Sparkles, Waypoints } from "lucide-react";
import { toast } from "sonner";
import { navigate } from "@/app/router";
import { PAGE_GROUPS } from "@/app/pages/registry";
import { queryKeys } from "@/lib/query-keys";
import type {
  AccessKeysResponse,
  HealthResponse,
  ProvidersResponse,
  RoutesResponse,
  TasksResponse,
} from "@/lib/types";
import { copyText } from "@/lib/format";
import { apiBase } from "@/lib/api";
import { cycleTheme, THEME_OPTIONS } from "@/lib/theme";

interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  health?: HealthResponse;
}

interface ObjectCommand {
  id: string;
  label: string;
  detail: string;
  page: string;
  params: Record<string, string>;
}

// 命令面板只读取已缓存的配置查询：打开一下搜索框不应顺带把四个配置接口全打一遍。
function cachedObjects(client: ReturnType<typeof useQueryClient>): ObjectCommand[] {
  const providers = client.getQueryData<ProvidersResponse>(queryKeys.providers())?.providers ?? [];
  const routes = client.getQueryData<RoutesResponse>(queryKeys.routes())?.routes ?? [];
  const tasks = client.getQueryData<TasksResponse>(queryKeys.tasks())?.tasks ?? [];
  const keys = client.getQueryData<AccessKeysResponse>(queryKeys.accessKeys())?.access_keys ?? [];

  return [
    ...providers.map((provider) => ({
      id: `provider:${provider.id}`,
      label: provider.id,
      detail: "供应商",
      page: "providers",
      params: { id: provider.id },
    })),
    ...routes.map((route) => ({
      id: `route:${route.id}`,
      label: route.id,
      detail: "模型路由",
      page: "routing",
      params: { id: route.id },
    })),
    ...tasks.map((task) => ({
      id: `task:${task.name}`,
      label: task.display_name || task.name,
      detail: task.workspace ? `任务路由 · ${task.workspace}` : "任务路由",
      page: "tasks",
      params: task.workspace ? { id: task.name, ws: task.workspace } : { id: task.name },
    })),
    ...keys.map((key) => ({
      id: `access-key:${key.id}`,
      label: key.name || key.id,
      detail: "访问密钥",
      page: "access-keys",
      params: { id: key.id },
    })),
  ];
}

function copyApiBase(health: HealthResponse | undefined): string {
  const base = typeof health?.base_url === "string" ? health.base_url : "";
  return base || `${window.location.origin}${apiBase()}`;
}

export function CommandPalette({ open, onOpenChange, health }: CommandPaletteProps) {
  const client = useQueryClient();
  const objects = useMemo(() => (open ? cachedObjects(client) : []), [client, open]);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  // Dialog 没有 Trigger 节点，Radix 不知道关闭后该把焦点还给谁；打开时记住
  // 当前焦点，关闭时主动归还，键盘用户才不会落回 body。
  useEffect(() => {
    if (open) {
      returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
  }, [open]);

  const run = (action: () => void) => {
    onOpenChange(false);
    action();
  };

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/40" />
        <Dialog.Content
          aria-label="命令面板"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            returnFocusRef.current?.focus();
          }}
          className="fixed left-1/2 top-[15vh] z-50 w-[min(640px,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-dialog border border-rule bg-raised shadow-[var(--shadow-float)] outline-none"
        >
          <Dialog.Title className="sr-only">命令面板</Dialog.Title>
          <Command loop className="flex max-h-[70vh] flex-col">
            <div className="flex items-center gap-2 border-b border-rule px-3">
              <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
              <Command.Input
                autoFocus
                aria-label="搜索页面、配置对象或操作"
                placeholder="搜索页面、配置对象或操作…"
                className="h-11 min-w-0 flex-1 bg-transparent text-body text-ink outline-none placeholder:text-ink-3"
              />
              <kbd className="rounded-chip border border-rule px-1.5 py-0.5 text-[10px] text-ink-3">Esc</kbd>
            </div>
            <Command.List className="min-h-0 flex-1 overflow-y-auto p-2">
              <Command.Empty className="px-3 py-8 text-center text-dense text-ink-3">没有匹配项</Command.Empty>

              <Command.Group heading="页面" className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-ink-3">
                {PAGE_GROUPS.flatMap((group) => group.items).map((page) => {
                  const Icon = page.icon;
                  return (
                    <Command.Item
                      key={page.id}
                      value={`${page.title} ${page.id}`}
                      onSelect={() => run(() => navigate(page.id))}
                      className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                    >
                      <Icon className="size-4 text-ink-3" aria-hidden />
                      <span>{page.title}</span>
                      <span className="ml-auto text-[11px] text-ink-3">{page.group}</span>
                    </Command.Item>
                  );
                })}
              </Command.Group>

              {objects.length ? (
                <Command.Group heading="配置对象" className="mt-2 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-ink-3">
                  {objects.map((object) => (
                    <Command.Item
                      key={object.id}
                      value={`${object.label} ${object.detail}`}
                      onSelect={() => run(() => navigate(object.page, object.params))}
                      className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                    >
                      {object.page === "providers" ? (
                        <Server className="size-4 text-ink-3" aria-hidden />
                      ) : object.page === "routing" ? (
                        <Waypoints className="size-4 text-ink-3" aria-hidden />
                      ) : (
                        <Settings className="size-4 text-ink-3" aria-hidden />
                      )}
                      <span className="min-w-0 truncate">{object.label}</span>
                      <span className="ml-auto shrink-0 text-[11px] text-ink-3">{object.detail}</span>
                    </Command.Item>
                  ))}
                </Command.Group>
              ) : null}

              <Command.Group heading="操作" className="mt-2 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-ink-3">
                <Command.Item
                  value="添加供应商"
                  onSelect={() => run(() => navigate("providers", { new: "1" }))}
                  className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                >
                  <Server className="size-4 text-ink-3" aria-hidden />
                  添加供应商
                </Command.Item>
                <Command.Item
                  value="新建访问密钥"
                  onSelect={() => run(() => navigate("access-keys", { new: "1" }))}
                  className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                >
                  <Sparkles className="size-4 text-ink-3" aria-hidden />
                  新建访问密钥
                </Command.Item>
                <Command.Item
                  value="新建任务"
                  onSelect={() => run(() => navigate("tasks", { new: "1" }))}
                  className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                >
                  <Check className="size-4 text-ink-3" aria-hidden />
                  新建任务
                </Command.Item>
                <Command.Item
                  value="切换主题"
                  onSelect={() =>
                    run(() => {
                      const next = cycleTheme();
                      const option = THEME_OPTIONS.find((item) => item.id === next) ?? THEME_OPTIONS[0];
                      toast.success(`主题已切换为${option.label}`);
                    })
                  }
                  className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                >
                  <Sparkles className="size-4 text-ink-3" aria-hidden />
                  切换主题
                </Command.Item>
                <Command.Item
                  value="复制 API 入口地址"
                  onSelect={() =>
                    run(() => {
                      void copyText(copyApiBase(health))
                        .then(() => toast.success("已复制"))
                        .catch(() => toast.error("复制失败，请手动选择地址"));
                    })
                  }
                  className="flex cursor-pointer items-center gap-2.5 rounded-control px-2 py-2 text-dense text-ink data-[selected=true]:bg-select"
                >
                  <CornerDownLeft className="size-4 text-ink-3" aria-hidden />
                  复制 API 入口地址
                </Command.Item>
              </Command.Group>
            </Command.List>
            <div className="border-t border-rule px-3 py-2 text-[11px] text-ink-3">↑↓ 选择 · ↵ 打开 · Esc 关闭</div>
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
