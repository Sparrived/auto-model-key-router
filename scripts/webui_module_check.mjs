// 开发用：静态检查 webui 所有模块能否被解析并完成 import 图求值。
// 用法：node scripts/webui_module_check.mjs
// 说明：app.js / main.js 依赖浏览器全局（location/document），这里提供最小垫片后再导入。

import { pathToFileURL } from "node:url";
import { readdirSync, statSync } from "node:fs";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "../webui");

const define = (name, value) =>
  Object.defineProperty(global, name, { value, writable: true, configurable: true });

// 最小浏览器垫片：只为让模块求值到"没有语法/导入错误"这一步。
class FakeNode {
  constructor(tag = "div") {
    this.tagName = tag; this.children = []; this.style = {}; this.attrs = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
  }
  append(...nodes) { this.children.push(...nodes.flat()); }
  appendChild(node) { this.children.push(node); return node; }
  replaceChildren(...nodes) { this.children = nodes.flat(); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener() {}
  removeEventListener() {}
  remove() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
  contains() { return false; }
  prepend() {}
  replaceWith() {}
  focus() {}
}
// location.hash/reload 给 app.js，search/replace 给登录页（pages/login.js 会用
// location.replace 跳回内页）。少了它们，这个检查会在导入入口模块时直接抛栈，
// 看起来像"模块坏了"，而实际只是垫片没覆盖用到的浏览器 API。
define("location", { hash: "#/overview", pathname: "/ui/", search: "", reload() {}, replace() {} });
define("window", { addEventListener() {}, isSecureContext: true, location: global.location });
define("navigator", { clipboard: null });
define("Node", FakeNode);
define("document", {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => ({ textContent: String(text), children: [] }),
  getElementById: () => new FakeNode("div"),
  body: new FakeNode("body"),
  addEventListener() {}, removeEventListener() {},
});
define("setInterval", () => 0);
define("clearInterval", () => {});
define("setTimeout", () => 0);
define("localStorage", { getItem: () => null, setItem() {}, removeItem() {} });
define("fetch", async () => ({ ok: true, status: 200, async text() { return "{}"; } }));

function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    if (statSync(full).isDirectory()) walk(full, out);
    else if (entry.endsWith(".js")) out.push(full);
  }
  return out;
}

const failures = [];
for (const file of walk(WEBUI)) {
  const rel = path.relative(WEBUI, file);
  try {
    await import(pathToFileURL(file).href);
    console.log(`OK   ${rel}`);
  } catch (error) {
    failures.push(`${rel}: ${error.message}`);
    console.log(`FAIL ${rel}: ${error.message}`);
  }
}

if (failures.length) {
  console.error(`\n${failures.length} 个模块加载失败`);
  process.exit(1);
}

// 页面 render 契约：app.js 把 page.render(ctx) 的返回值直接 mount 进 #content，
// 所以它必须**同步**返回节点。async 函数返回 Promise，会被 dom.js 的 append 当成
// 子节点渲染成整页的 "[object Promise]"——「访问密钥」页正是这么坏掉的，而它语法、
// 导入全都正确，上面那条加载检查抓不到。
const { PAGES } = await import(pathToFileURL(path.join(WEBUI, "app.js")).href);
const asyncPages = PAGES.flatMap((group) => group.items)
  .filter((item) => item.render.constructor.name === "AsyncFunction")
  .map((item) => item.id);
if (asyncPages.length) {
  console.error(`以下页面的 render 是 async 函数（会渲染成 [object Promise]）：${asyncPages.join(", ")}`);
  process.exit(1);
}

console.log(`\n全部 ${walk(WEBUI).length} 个模块加载正常`);
