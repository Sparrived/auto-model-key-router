import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { Toaster } from "sonner";
import { PanelPage } from "@/standalone/panel/PanelPage";
import "@/styles/index.css";

// 面板可能嵌在第三方面板里：不读 amkr.theme（那会碰 localStorage），只跟随宿主的
// 系统深浅色偏好。真正的安全边界在 PanelPage/API 客户端，这里只是首屏观感。
function systemTheme(): "light" | "dark" {
  try {
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  } catch {
    return "light";
  }
}

function StandaloneToaster() {
  const [theme, setTheme] = useState<"light" | "dark">(systemTheme);
  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const change = () => setTheme(media.matches ? "dark" : "light");
    media.addEventListener?.("change", change);
    return () => media.removeEventListener?.("change", change);
  }, []);
  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    document.documentElement.setAttribute("data-theme-preference", "system");
  }, [theme]);
  return <Toaster theme={theme} position="bottom-right" closeButton />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PanelPage />
    <StandaloneToaster />
  </StrictMode>,
);
