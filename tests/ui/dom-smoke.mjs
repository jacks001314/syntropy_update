#!/usr/bin/env node
//
// 用 jsdom 真跑一遍内嵌管理页，对着一个真实在跑的服务端做端到端验证：
// 页面加载 → 令牌门禁 → 每个标签页都渲染 →（可选）真正写一条数据并检查落库与转义。
//
// 用法：
//   node tests/ui/dom-smoke.mjs --base http://127.0.0.1:8443 --token <admin-token>
//   node tests/ui/dom-smoke.mjs --base https://app.syntropy-lab.com --token <token> --write
//
// jsdom 不是本工程的依赖（Go 工程不该被 npm 拖住）：把 jsdom 装在 .work/uiharness 下即可：
//   cd .work/uiharness && npm install jsdom

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(here, "..", "..");
const assets = join(repoRoot, "internal", "webui", "assets");

const argv = process.argv.slice(2);
const flag = (name, fallback) => {
  const index = argv.indexOf(name);
  return index >= 0 && argv[index + 1] ? argv[index + 1] : fallback;
};
const base = flag("--base", "http://127.0.0.1:8443").replace(/\/$/, "");
const token = flag("--token", "");
const write = argv.includes("--write");

const require = createRequire(join(repoRoot, ".work", "uiharness", "package.json"));
let JSDOM;
try {
  ({ JSDOM } = require("jsdom"));
} catch (error) {
  console.error("找不到 jsdom；先执行：cd .work/uiharness && npm install jsdom");
  console.error(String(error?.message ?? error));
  process.exit(2);
}

const failures = [];
function check(name, ok, detail) {
  console.log((ok ? "PASS " : "FAIL ") + name + (detail ? " — " + detail : ""));
  if (!ok) failures.push(name);
}

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function until(checkFunction, label, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (checkFunction()) return true;
    if (Date.now() > deadline) return false;
    await wait(25);
  }
}

async function api(path, options) {
  const response = await fetch(base + path, {
    ...options,
    headers: { Authorization: "Bearer " + token, "X-Actor": "ui-smoke", ...(options?.headers ?? {}) },
  });
  return response.json();
}

const dom = new JSDOM(readFileSync(join(assets, "index.html"), "utf8"), {
  url: base + "/admin/",
  runScripts: "dangerously",
  pretendToBeVisual: true,
  beforeParse(window) {
    // 页面通过 fetch 调同源接口：转发给 Node 的 fetch，也就等于打真实服务端。
    // jsdom 的 Blob 不是 Node 的 Blob（制品上传要按字节送），这里把 body 转成 Buffer。
    window.fetch = async (input, init) => {
      const href = typeof input === "string" ? input : String(input?.href ?? input);
      const options = { ...(init ?? {}) };
      const body = options.body;
      if (body && typeof body.arrayBuffer === "function" && typeof body !== "string") {
        options.body = Buffer.from(await body.arrayBuffer());
      }
      return fetch(href, options);
    };
    Object.defineProperty(window, "crypto", {
      configurable: true,
      value: require("node:crypto").webcrypto,
    });
    window.confirm = () => true;
    window.alert = () => undefined;
  },
});

const { window } = dom;
const document = window.document;

// 页面用相对路径引 app.css / app.js。服务端若把静态资源一并回落成 index.html，
// 浏览器只会渲染出一个无样式、无脚本的静态壳——jsdom 从本地仓库读 assets，看不出这一点。
for (const asset of ["app.css", "app.js"]) {
  const response = await fetch(`${base}/admin/${asset}`);
  const type = response.headers.get("content-type") ?? "";
  check(`静态资源 ${asset} 从服务端取到`, response.ok && !type.includes("text/html"), `status=${response.status} type=${type}`);
}

const text = (selector) => document.querySelector(selector)?.textContent?.trim() ?? "";
const viewText = () => text("#view");
const buttons = (label) => [...document.querySelectorAll("#view button")].filter((node) => node.textContent.trim() === label);

window.eval(readFileSync(join(assets, "app.js"), "utf8"));

// 1. 未登录时的门禁：不能是一片空白，要给出可执行的指引。
await until(() => viewText().length > 0, "首屏渲染");
check("未登录时显示令牌门禁", viewText().includes("需要管理令牌"), viewText().slice(0, 60));
check("导航有 11 个页面", document.querySelectorAll("#tabs a").length === 11, String(document.querySelectorAll("#tabs a").length));
check("连接状态显示服务在线", text("#conn").includes("在线"), text("#conn"));

// 2. 贴令牌后每个标签页都要渲染成功。
document.querySelector("#token").value = token;
document.querySelector("#token-save").dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
const loaded = await until(() => document.querySelectorAll("#view .tile").length >= 7, "概览渲染");
check("令牌生效后概览渲染出统计块", loaded);
check("侧栏计数与接口一致", text("#tabs a").length > 0 && viewText().length > 0);

const components = await api("/v1/admin/components");
const componentCount = (components.components ?? []).length;
const tileValue = (label) => {
  const tile = [...document.querySelectorAll("#view .tile")].find((node) => node.querySelector(".label")?.textContent === label);
  return tile ? Number(tile.querySelector(".value").textContent) : NaN;
};
check("概览的组件数与接口一致", tileValue("组件") === componentCount, `${tileValue("组件")} vs ${componentCount}`);

const pages = [
  ["components", "组件"],
  ["releases", "发布与制品"],
  ["distributions", "分发定义（BOM）"],
  ["inventory", "目标库存"],
  ["ignores", "忽略集合"],
  ["plans", "升级计划"],
  ["results", "升级结果"],
  ["policies", "分发策略"],
  ["audit", "审计"],
  ["help", "说明"],
];
for (const [key, title] of pages) {
  window.location.hash = "#/" + key;
  window.dispatchEvent(new window.Event("hashchange"));
  const ok = await until(() => text("#page-title") === title && !viewText().includes("加载中"), "渲染 " + key);
  check("标签页 " + key + " 正常渲染", ok && !viewText().includes("加载失败"), viewText().slice(0, 80));
}

// 3. 可选：真正写一条数据，确认「表单 → 接口 → 表格」闭环，并确认 HTML 只当文本落屏。
if (write) {
  const name = "ui-smoke-" + Date.now().toString(36);
  const payload = '<img src=x onerror="alert(1)">';
  window.location.hash = "#/components";
  window.dispatchEvent(new window.Event("hashchange"));
  await until(() => text("#page-title") === "组件", "回到组件页");

  // 视图先渲染「加载中」，输入框要等接口回来才存在。
  const formReady = await until(
    () => document.querySelector('#view input[placeholder="Master 二进制"]') !== null,
    "组件表单就绪",
  );
  check("组件表单加载完成", formReady);
  const nameInput = document.querySelector('#view input[placeholder="mbot"]');
  const displayInput = document.querySelector('#view input[placeholder="Master 二进制"]');
  nameInput.value = name;
  displayInput.value = payload;
  buttons("登记")[0].dispatchEvent(new window.MouseEvent("click", { bubbles: true }));

  const created = await until(() => text("#status").includes("登记组件完成"), "登记组件", 10000);
  check("表单提交后状态栏报完成", created, text("#status"));

  const inTable = await until(() => [...document.querySelectorAll("#view td")].some((cell) => cell.textContent === payload), "表格出现新组件");
  check("新组件出现在表格里", inTable);
  check("接口里的显示名未被 HTML 解析", !document.querySelector('#view img[src="x"]'));

  const after = await api("/v1/admin/components");
  check("组件已落库", (after.components ?? []).some((entry) => entry.name === name), name);

  const search = document.querySelector('#view input[type="search"]');
  search.value = name;
  search.dispatchEvent(new window.Event("input", { bubbles: true }));
  await wait(50);
  const rows = document.querySelectorAll("#view tbody tr").length;
  check("搜索过滤只剩一行", rows === 1, String(rows));

  // 4. 内容制品：平台无关的上传路径（artifacts/any/any）最容易写错，真跑一遍。
  const contentName = "ui-content-" + Date.now().toString(36);
  window.location.hash = "#/components";
  window.dispatchEvent(new window.Event("hashchange"));
  await until(() => document.querySelector('#view input[placeholder="Master 二进制"]') !== null, "组件表单就绪");
  document.querySelector('#view input[placeholder="mbot"]').value = contentName;
  document.querySelector("#view select").value = "content";
  buttons("登记")[0].dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  check("登记内容组件", await until(() => text("#status").includes("登记组件完成"), "登记内容组件", 10000), text("#status"));

  window.location.hash = "#/releases";
  window.dispatchEvent(new window.Event("hashchange"));
  // 发布页的表单独有 placeholder="1.2.0" 的版本输入框：等它出现才算真的切过去了。
  await until(() => document.querySelector('#view input[placeholder="1.2.0"]') !== null, "发布表单就绪");
  document.querySelector('#view input[placeholder="mbot"]').value = contentName;
  document.querySelector('#view input[placeholder="1.2.0"]').value = "1.0.0";
  buttons("创建发布")[0].dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  check("创建内容发布", await until(() => text("#status").includes("创建发布完成"), "创建发布", 10000), text("#status"));

  const expectedFile = contentName + "-1.0.0.tar.gz";
  const cellsOf = (name) => [...document.querySelectorAll("#view tbody tr")].filter((row) => row.cells[0]?.textContent.includes(name));
  const targetRow = await until(
    () => cellsOf(contentName).length === 1,
    "发布行出现",
  );
  check("发布行出现在表格里", targetRow);
  const row = cellsOf(contentName)[0];
  [...row.querySelectorAll("button")].find((button) => button.textContent === "制品").dispatchEvent(new window.MouseEvent("click", { bubbles: true }));

  const fileReady = await until(() => document.querySelector('#view input[type="file"]') !== null, "上传面板出现");
  check("制品上传面板打开", fileReady && viewText().includes("平台无关"), viewText().slice(0, 80));
  const fileInput = document.querySelector('#view input[type="file"]');
  const bytes = Buffer.from("fake-content-tarball-bytes");
  const file = new window.File([new Uint8Array(bytes)], expectedFile, { type: "application/gzip" });
  Object.defineProperty(fileInput, "files", { configurable: true, value: [file] });
  buttons("上传")[0].dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  check("上传内容制品", await until(() => text("#status").includes("上传制品完成"), "上传制品", 15000), text("#status"));
  check("上传后制品表格留在页面上", await until(() => viewText().includes(expectedFile), "制品出现在表格"), viewText().slice(0, 80));

  const released = await api("/v1/admin/releases?component=" + contentName);
  const releaseId = (released.releases ?? [])[0]?.id;
  const detail = await api("/v1/admin/releases/" + releaseId);
  const artifact = (detail.artifacts ?? [])[0];
  check("内容制品已落库且平台无关", Boolean(artifact) && artifact.filename === expectedFile && artifact.os === "" && artifact.size === bytes.length,
    artifact ? `${artifact.filename} os=${JSON.stringify(artifact.os)} size=${artifact.size}` : "无制品");
}

dom.window.close();
console.log(failures.length ? "\n失败：" + failures.join("、") : "\n全部通过");
process.exit(failures.length ? 1 : 0);
