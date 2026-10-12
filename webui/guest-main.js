// 访客看板的入口。与 main.js / panel-main.js 并列的**第三个**入口：
// 同一份静态资源与样式，但有自己的 HTML 页（guest.html）与鉴权流程。
//
// 为什么不让访客看板并进管理面：管理面是**完整权限**的界面，把它的导航与按钮配上一
// 个受限凭据，用户看到的每个链接都会 401（工作空间面板也是同一个理由，见 panel.js）。
// 访客只需要"我用了多少"，那就只给它这一页。
import { bootGuest } from "./pages/guest.js";
import { initTheme } from "./theme.js";

initTheme();
bootGuest();
