// 登录页的入口。与 main.js / panel-main.js / guest-main.js 并列的**第四个**入口：
// 同一份静态资源与样式，但有自己的 HTML 页（login.html）与鉴权流程。
//
// 它只做"收凭据"这一件事；主界面负责"发现没有可用凭据就跳过来"。两边的分工与理由
// 见 pages/login.js 的文件头。
import { bootLogin } from "./pages/login.js";
import { initTheme } from "./theme.js";

initTheme();
bootLogin();
