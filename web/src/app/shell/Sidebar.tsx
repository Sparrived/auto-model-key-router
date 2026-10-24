import { useEffect, useRef, useState, type RefObject } from "react";
import { Dialog } from "radix-ui";
import { ChevronLeft, ChevronRight, X } from "lucide-react";
import { navigate } from "@/app/router";
import { PAGE_GROUPS, type PageDef } from "@/app/pages/registry";
import { readStorage, writeStorage } from "@/lib/storage";
import { cn } from "@/lib/cn";

const COLLAPSE_KEY = "amkr.ui.sidebar";

function initialCollapsed(): boolean {
  const saved = readStorage(COLLAPSE_KEY);
  if (saved === "1") return true;
  if (saved === "0") return false;
  return typeof window !== "undefined" && window.innerWidth < 1024;
}

interface NavLinkProps {
  page: PageDef;
  active: boolean;
  collapsed: boolean;
  onNavigate: () => void;
}

function NavLink({ page, active, collapsed, onNavigate }: NavLinkProps) {
  const Icon = page.icon;
  return (
    <a
      href={`#/${page.id}`}
      aria-current={active ? "page" : undefined}
      aria-label={collapsed ? page.title : undefined}
      title={collapsed ? page.title : undefined}
      onClick={(event) => {
        event.preventDefault();
        navigate(page.id);
        onNavigate();
      }}
      className={cn(
        "relative flex h-9 items-center gap-2.5 rounded-control px-2.5 text-dense transition-colors",
        "hover:bg-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-offset-2",
        active ? "bg-select font-medium text-ink" : "text-ink-2 hover:text-ink",
        collapsed && "justify-center px-0",
      )}
    >
      {active ? (
        <span aria-hidden className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-accent" />
      ) : null}
      <Icon className="size-[18px] shrink-0" aria-hidden />
      {!collapsed ? <span className="truncate">{page.title}</span> : null}
    </a>
  );
}

interface SidebarContentProps {
  activePage: string;
  collapsed: boolean;
  version?: string;
  onToggleCollapsed: () => void;
  onNavigate: () => void;
  mobile?: boolean;
  onCloseMobile?: () => void;
}

function SidebarContent({
  activePage,
  collapsed,
  version,
  onToggleCollapsed,
  onNavigate,
  mobile = false,
}: SidebarContentProps) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className={cn("flex h-14 shrink-0 items-center border-b border-rule px-3", collapsed && !mobile && "justify-center px-0")}>
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-chip bg-accent text-on-accent">
          <span className="text-[15px] leading-none">◆</span>
        </span>
        {!collapsed || mobile ? (
          <div className="ml-2.5 min-w-0">
            <div className="text-body font-semibold tracking-tight text-ink">AMKR</div>
            <div className="truncate text-[10px] text-ink-3">Auto Model Key Router</div>
          </div>
        ) : null}
        {mobile ? (
          <button
            type="button"
            aria-label="关闭主导航"
            className="ml-auto grid size-8 place-items-center rounded-control text-ink-2 hover:bg-hover hover:text-ink"
            onClick={onNavigate}
          >
            <X className="size-4" aria-hidden />
          </button>
        ) : null}
      </div>

      <nav aria-label="主导航" className="min-h-0 flex-1 overflow-y-auto px-2 py-3">
        {PAGE_GROUPS.map((group) => (
          <div key={group.label} className="mb-3 last:mb-0">
            {!collapsed || mobile ? (
              <div className="px-2.5 pb-1 text-[11px] font-medium tracking-wide text-ink-3">{group.label}</div>
            ) : (
              <div aria-hidden className="mx-2 mb-1 h-px bg-rule" />
            )}
            <div className="space-y-0.5">
              {group.items.map((page) => (
                <NavLink
                  key={page.id}
                  page={page}
                  active={page.id === activePage}
                  collapsed={collapsed && !mobile}
                  onNavigate={onNavigate}
                />
              ))}
            </div>
          </div>
        ))}
      </nav>

      <div className={cn("flex shrink-0 items-center gap-2 border-t border-rule px-3 py-3", collapsed && !mobile && "justify-center px-0")}>
        {!collapsed || mobile ? (
          <div className="min-w-0 flex-1">
            <div className="text-[11px] text-ink-3">AMKR WebUI</div>
            <div className="font-mono text-[11px] text-ink-2">{version ? `v${version}` : "版本读取中"}</div>
          </div>
        ) : null}
        {!mobile ? (
          <button
            type="button"
            aria-label={collapsed ? "展开侧栏" : "折叠侧栏"}
            title={collapsed ? "展开侧栏" : "折叠侧栏"}
            className="grid size-8 shrink-0 place-items-center rounded-control text-ink-2 hover:bg-hover hover:text-ink"
            onClick={onToggleCollapsed}
          >
            {collapsed ? <ChevronRight className="size-4" aria-hidden /> : <ChevronLeft className="size-4" aria-hidden />}
          </button>
        ) : null}
      </div>
    </div>
  );
}

interface SidebarProps {
  activePage: string;
  version?: string;
  mobileOpen: boolean;
  onMobileOpenChange: (open: boolean) => void;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
}

export function Sidebar({ activePage, version, mobileOpen, onMobileOpenChange, returnFocusRef }: SidebarProps) {
  const [collapsed, setCollapsed] = useState(initialCollapsed);
  const wasMobileOpen = useRef(false);
  if (mobileOpen) wasMobileOpen.current = true;

  useEffect(() => {
    if (!mobileOpen && wasMobileOpen.current) {
      returnFocusRef?.current?.focus();
    }
  }, [mobileOpen, returnFocusRef]);

  const toggle = () => {
    setCollapsed((value) => {
      const next = !value;
      writeStorage(COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });
  };

  return (
    <>
      <aside
        aria-label="主导航"
        className={cn(
          "hidden h-screen shrink-0 border-r border-rule bg-panel transition-[width] duration-200 md:sticky md:top-0 md:flex md:flex-col",
          collapsed ? "md:w-16" : "md:w-56",
        )}
      >
        <SidebarContent
          activePage={activePage}
          collapsed={collapsed}
          version={version}
          onToggleCollapsed={toggle}
          onNavigate={() => {}}
        />
      </aside>

      <Dialog.Root open={mobileOpen} onOpenChange={onMobileOpenChange}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 z-50 bg-black/40 md:hidden" />
          <Dialog.Content
            aria-label="主导航"
            aria-describedby={undefined}
            className="fixed inset-y-0 left-0 z-50 w-64 border-r border-rule bg-panel shadow-[var(--shadow-float)] outline-none md:hidden"
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              returnFocusRef?.current?.focus();
            }}
          >
            <Dialog.Title className="sr-only">主导航</Dialog.Title>
            <SidebarContent
              mobile
              activePage={activePage}
              collapsed={false}
              version={version}
              onToggleCollapsed={() => {}}
              onNavigate={() => onMobileOpenChange(false)}
            />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </>
  );
}
