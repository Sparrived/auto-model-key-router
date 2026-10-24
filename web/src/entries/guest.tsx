import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { Toaster } from "sonner";
import { GuestPage } from "@/standalone/guest/GuestPage";
import "@/styles/index.css";

// 访客看板是独立入口：不读主题存储（它有自己的 amkr.guestAccessKey 边界），只跟随
// 系统深浅色。页面与 API 客户端也不会触碰 amkr.apiKey。
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
    <GuestPage />
    <StandaloneToaster />
  </StrictMode>,
);
