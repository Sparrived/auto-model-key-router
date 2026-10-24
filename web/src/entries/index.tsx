import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { queryClient } from "@/app/query-client";
import { useAuth } from "@/app/auth";
import { AppShell } from "@/app/shell/AppShell";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { ImpactProvider } from "@/components/ui/ImpactDialog";
import { SecretProvider } from "@/components/ui/SecretDialog";
import { TooltipProvider } from "@/components/ui/Tooltip";
import { getResolvedTheme, initTheme, onThemeChange, type ResolvedTheme } from "@/lib/theme";
import "@/styles/index.css";

initTheme();

function AuthGate() {
  const auth = useAuth();
  // 未授权时一个节点都不渲染：登录页接管，避免带着无效 Key 把 401 缓存进各页查询。
  if (auth.status !== "ok") return null;
  return <AppShell />;
}

function ThemedToaster() {
  const [theme, setTheme] = useState<ResolvedTheme>(getResolvedTheme);
  useEffect(() => onThemeChange((info) => setTheme(info.resolved)), []);
  return <Toaster theme={theme} position="bottom-right" closeButton />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={200}>
        <ConfirmProvider>
          <ImpactProvider>
            <SecretProvider>
              <AuthGate />
              <ThemedToaster />
            </SecretProvider>
          </ImpactProvider>
        </ConfirmProvider>
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
);
