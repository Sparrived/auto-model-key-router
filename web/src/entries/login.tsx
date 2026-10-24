import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { LoginPage } from "@/standalone/login/LoginPage";
import "@/styles/index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <LoginPage />
  </StrictMode>,
);
