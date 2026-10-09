"use strict";

/**
 * 管理页调用的接口表，逐条写成「方法 + 路径」的字面量。
 * internal/api 的结构测试会把这里的每一条与注册路由逐一比对：任何一侧改了而另一侧
 * 没跟上，测试就会失败——管理页最怕的就是悄悄指向一个不存在的接口。
 *
 * 只写「方法 + 路径模板」：查询串一律走 call 的 query 参数，不要写进字面量，
 * 否则结构测试会把它当成一条不存在的路由。
 */
const API = {
  artifact: "PUT /v1/admin/releases/{id}/artifacts/{os}/{arch}",
  artifactDownload: "GET /v1/admin/artifacts/{sha256}",
  audit: "GET /v1/admin/audit",
  component: "POST /v1/admin/components",
  components: "GET /v1/admin/components",
  distribution: "POST /v1/admin/distributions",
  distributions: "GET /v1/admin/distributions",
  health: "GET /healthz",
  ignores: "GET /v1/admin/ignores",
  inventory: "GET /v1/admin/inventory",
  plan: "GET /v1/admin/plans/{id}",
  plans: "GET /v1/admin/plans",
  policies: "GET /v1/admin/policies",
  policy: "PUT /v1/admin/policies/{component}/{channel}",
  pubkey: "GET /v1/pubkey",
  publish: "POST /v1/admin/releases/{id}/publish",
  release: "POST /v1/admin/releases",
  releaseDetail: "GET /v1/admin/releases/{id}",
  releases: "GET /v1/admin/releases",
  results: "GET /v1/admin/results",
  yank: "POST /v1/admin/releases/{id}/yank",
};

const TOKEN_KEY = "syntropy-update-admin-token";
const CHANNELS = ["stable", "beta", "alpha"];
const KINDS = ["binary", "archive", "installer", "content"];
const VERSIONING = ["product", "independent"];
const APPROVALS = ["auto", "confirm", "manual"];
const RESULT_LABEL = {
  installed: "已安装",
  "handed-off": "已交给安装器",
  failed: "失败",
  unsupported: "本机不支持",
};
const RESULT_TONE = {
  installed: "ok",
  "handed-off": "warn",
  failed: "error",
  unsupported: "info",
};
const PLATFORMS = ["windows", "linux", "darwin"];
const ARCHES = ["amd64", "arm64"];

const KIND_LABEL = { binary: "二进制", archive: "压缩包", installer: "安装器", content: "内容制品" };
const APPROVAL_LABEL = { auto: "自动安装", confirm: "安装前确认", manual: "仅手动" };
const APPROVAL_TONE = { auto: "ok", confirm: "warn", manual: "info" };
const ACTION_LABEL = { install: "安装", upgrade: "升级", downgrade: "降级", skip: "跳过" };
const STATE_LABEL = { pending: "待执行", deferred: "窗口外", blocked: "被拦下" };
const STATE_TONE = { pending: "ok", deferred: "warn", blocked: "danger" };

const state = {
  busy: false,
  token: sessionStorage.getItem(TOKEN_KEY) ?? "",
  health: null,
  counts: null,
  // 上传/上架会触发整页重渲染：记住正在看哪份发布的制品，重渲染后自动展开。
  openReleaseId: "",
  filters: {},
};

const nodes = {
  status: document.getElementById("status"),
  tabs: document.getElementById("tabs"),
  token: document.getElementById("token"),
  view: document.getElementById("view"),
  conn: document.getElementById("conn"),
  pageTitle: document.getElementById("page-title"),
  pageHint: document.getElementById("page-hint"),
};

/** setStatus 统一走 textContent：接口返回的错误信息不会被当成 HTML 执行。 */
function setStatus(message, tone) {
  nodes.status.textContent = message ?? "";
  nodes.status.classList.toggle("error", tone === "error");
  nodes.status.classList.toggle("ok", tone === "ok");
}

function renderConn() {
  const health = state.health;
  nodes.conn.classList.toggle("ok", Boolean(health?.online));
  nodes.conn.classList.toggle("bad", health ? !health.online : false);
  if (!health) nodes.conn.textContent = "正在检测服务…";
  else if (!health.online) nodes.conn.textContent = "服务不可达：" + health.error;
  else if (health.adminEnabled === false) nodes.conn.textContent = "服务在线 · 管理接口未启用";
  else if (!state.token) nodes.conn.textContent = "服务在线 · 待登录";
  else nodes.conn.textContent = "服务在线 · 已带令牌";
}

/** el 建节点：文本一律走 textContent，接口返回的内容不会被当成 HTML 执行。 */
function el(tag, props, children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props ?? {})) {
    if (value === undefined || value === null) continue;
    if (key === "text") node.textContent = String(value);
    else if (key === "class" && !value) continue;
    else if (key === "onClick") node.addEventListener("click", value);
    else if (key === "onInput") node.addEventListener("input", value);
    else node.setAttribute(key, String(value));
  }
  for (const child of children ?? []) if (child) node.append(child);
  return node;
}

function cell(content) {
  return content instanceof Node ? content : document.createTextNode(String(content ?? ""));
}

function badge(label, tone) {
  return el("span", { class: "badge" + (tone ? " " + tone : ""), text: label });
}

function mono(text) {
  return el("code", { text: String(text ?? "") });
}

function copyChip(value, label) {
  const button = el("button", { type: "button", class: "copy", title: value, text: label ?? "复制" });
  button.addEventListener("click", () => void writeClipboard(String(value)));
  return button;
}

async function writeClipboard(value) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
    } else {
      const scratch = el("textarea", {});
      scratch.value = value;
      document.body.append(scratch);
      scratch.select();
      document.execCommand("copy");
      scratch.remove();
    }
    setStatus("已复制：" + value.slice(0, 60), "ok");
  } catch (error) {
    setStatus("复制失败：" + describeError(error), "error");
  }
}

function table(headers, rows, options) {
  const body = el(
    "tbody",
    {},
    rows.map((row) => el("tr", {}, row.map((entry) => el("td", {}, [cell(entry)])))),
  );
  if (!rows.length) {
    body.append(
      el("tr", {}, [el("td", { class: "empty", colspan: String(headers.length), text: options?.empty ?? "暂无数据" })]),
    );
  }
  return el("div", { class: "table-wrap" }, [
    el("table", {}, [el("thead", {}, [el("tr", {}, headers.map((header) => el("th", { text: header })))]), body]),
  ]);
}

/** card 生成一张卡片：title + 可选说明/操作，body 默认带内边距（放表格时用 flush）。 */
function card(title, children, options) {
  const header = el("header", {}, [
    el("h2", { text: title }),
    options?.subtitle ? el("span", { class: "muted", text: options.subtitle }) : null,
    ...(options?.actions ?? []),
  ]);
  return el("section", { class: "card" }, [
    header,
    el("div", { class: "card-body" + (options?.flush ? " flush" : "") }, children),
  ]);
}

function note(text) {
  return el("p", { class: "hint", text });
}

function field(label, input) {
  return el("label", { class: "field" }, [el("span", { text: label }), input]);
}

function textInput(placeholder, value) {
  return el("input", { type: "text", placeholder: placeholder ?? "", value: value ?? "" });
}

function numberInput(value, min, max) {
  return el("input", { type: "number", min: min ?? 0, max: max ?? 100, value: String(value ?? 0) });
}

function select(options, selected) {
  const node = el(
    "select",
    {},
    options.map((option) => el("option", { text: option, value: option })),
  );
  if (selected) node.value = selected;
  return node;
}

function selectWithLabel(label, options, selected) {
  return field(label, select(options, selected));
}

function checkbox(label, checked) {
  const input = el("input", { type: "checkbox" });
  input.checked = Boolean(checked);
  return el("label", { class: "row" }, [input, el("span", { text: label })]);
}

function timestamp(value) {
  const milliseconds = Number(value);
  if (!value || !Number.isFinite(milliseconds)) return "";
  return new Date(milliseconds).toISOString().replace("T", " ").slice(0, 19) + "Z";
}

function humanSize(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return (index === 0 ? String(size) : size.toFixed(1)) + " " + units[index];
}

function jsonDetails(value, label) {
  return el("details", {}, [
    el("summary", { text: label ?? "原始 JSON" }),
    el("pre", { text: JSON.stringify(value, null, 2) }),
  ]);
}

function versionChips(components) {
  const entries = Object.entries(components ?? {});
  if (!entries.length) return el("span", { class: "muted", text: "—" });
  return el(
    "div",
    { class: "row" },
    entries
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([name, version]) =>
        el("span", { class: "chip" }, [mono(name), el("span", { class: "muted", text: "@" + version })]),
      ),
  );
}

function tiles(entries) {
  return el(
    "div",
    { class: "tiles" },
    entries.map(([label, value, sub]) =>
      el("div", { class: "tile" }, [
        el("div", { class: "label", text: label }),
        el("div", { class: "value", text: String(value) }),
        sub ? el("div", { class: "sub", text: sub }) : null,
      ]),
    ),
  );
}

/** filterInput 记住每个页面各自的过滤条件，重渲染后不丢。 */
function filterInput(page, name, placeholder, initial) {
  const key = page + ":" + name;
  if (state.filters[key] === undefined) state.filters[key] = initial ?? "";
  return el("input", { type: "search", placeholder: placeholder ?? "", value: state.filters[key] });
}

function filterValue(page, name, initial) {
  const key = page + ":" + name;
  if (state.filters[key] === undefined) state.filters[key] = initial ?? "";
  return state.filters[key];
}

function bindFilter(input, page, name, repaint) {
  input.addEventListener("input", () => {
    state.filters[page + ":" + name] = input.value;
    repaint();
  });
}

function matches(needle, haystack) {
  const text = String(needle ?? "").trim().toLowerCase();
  if (!text) return true;
  return String(haystack ?? "").toLowerCase().includes(text);
}

function destructiveButton(label, message, action) {
  const button = el("button", { type: "button", class: "danger tiny", text: label });
  button.addEventListener("click", () => {
    if (window.confirm(message)) void action();
  });
  return button;
}
function splitEntry(entry) {
  const index = entry.indexOf(" ");
  return { method: entry.slice(0, index), path: entry.slice(index + 1) };
}

function apiPath(entry, params) {
  let path = splitEntry(entry).path;
  for (const [key, replacement] of Object.entries(params ?? {})) {
    path = path.split("{" + key + "}").join(encodeURIComponent(String(replacement)));
  }
  return path;
}

/** request 发一次请求；body 是 Blob/File 时按原始字节送出（制品上传走这条路）。 */
async function request(entry, params, options) {
  const opts = options ?? {};
  const raw = opts.body instanceof Blob;
  const url = new URL(apiPath(entry, params), location.origin);
  for (const [key, value] of Object.entries(opts.query ?? {})) {
    if (value === undefined || value === null || value === "") continue;
    url.searchParams.set(key, String(value));
  }
  const headers = {
    ...(state.token ? { Authorization: "Bearer " + state.token } : {}),
    ...(opts.body === undefined || raw ? {} : { "Content-Type": "application/json" }),
    "X-Actor": opts.actor ?? "admin-ui",
    ...(opts.headers ?? {}),
  };
  let response;
  try {
    response = await fetch(url, {
      body: opts.body === undefined ? undefined : raw ? opts.body : JSON.stringify(opts.body),
      headers,
      method: opts.method ?? splitEntry(entry).method,
    });
  } catch (error) {
    throw new Error("无法连接服务：" + describeError(error));
  }
  return response;
}

/** call 发一次请求并把 JSON 响应当返回值；非 2xx 统一抛错。 */
async function call(entry, params, options) {
  const response = await request(entry, params, options);
  const text = await response.text();
  let payload = null;
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    payload = null;
  }
  if (!response.ok) {
    const message = payload?.message ?? payload?.error ?? "HTTP " + response.status;
    const error = new Error(String(message));
    error.status = response.status;
    throw error;
  }
  if (payload === null) {
    throw new Error("服务返回了非 JSON 响应（HTTP " + response.status + "）");
  }
  return payload;
}

async function callText(entry, params, options) {
  const response = await request(entry, params, options);
  const body = await response.text();
  if (!response.ok) {
    const error = new Error("HTTP " + response.status);
    error.status = response.status;
    throw error;
  }
  return body;
}

function describeError(error) {
  const message = error instanceof Error ? error.message : String(error ?? "未知错误");
  if (error?.status === 401) return "未授权：令牌无效，或服务端没有配置 adminTokens（管理接口被整个禁用）";
  if (error?.status === 403) return "已被拒绝（403）：" + message;
  return message;
}

/** run 包住一次写操作：统一处理忙碌标记、错误提示与刷新。 */
async function run(label, action) {
  if (state.busy) return;
  state.busy = true;
  setStatus(label + "…");
  try {
    await action();
    setStatus(label + "完成", "ok");
  } catch (error) {
    setStatus(label + "失败：" + describeError(error), "error");
  } finally {
    state.busy = false;
    await render();
  }
}

async function sha256Hex(file) {
  if (!crypto.subtle) throw new Error("浏览器未提供安全上下文，请用 https 或 localhost 打开管理页");
  const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** probeHealth 打公开的 /healthz：页面未登录时也要能告诉运维服务端到底什么状态。 */
async function probeHealth() {
  try {
    const payload = await call(API.health);
    state.health = {
      online: true,
      adminEnabled: payload.adminEnabled !== false,
      clientRequired: payload.clientTokensSet === true,
      deviceRequired: payload.deviceAuthRequired === true,
      readRequired: payload.readAuthRequired === true,
    };
  } catch (error) {
    state.health = {
      online: false,
      adminEnabled: false,
      clientRequired: false,
      deviceRequired: false,
      readRequired: false,
      error: describeError(error),
    };
  }
  renderConn();
  return state.health;
}

function loadingCard(text) {
  return card("加载中", [note(text ?? "正在拉取数据…")]);
}

function tokenGateView() {
  const health = state.health ?? {};
  const children = [];
  if (health.online === false) {
    children.push(note("服务不可达：" + health.error));
    children.push(
      note("如果服务刚重启，稍后点右上角「刷新」；仍不可达就检查 systemd 状态与防火墙。"),
    );
  } else if (health.adminEnabled === false) {
    children.push(
      note("服务端没有配置管理令牌，/v1/admin/* 全部返回 401——这就是管理页什么都动不了的原因。"),
    );
    children.push(
      el("ol", { class: "steps" }, [
        el("li", { text: "在服务器上给配置的 adminTokens 数组加一个令牌（deploy/install.sh 会自动生成一个）。" }),
        el("li", { text: "重启服务：sudo systemctl restart syntropy-update。" }),
        el("li", { text: "把令牌粘到右上角的「管理令牌」输入框，点「使用」。" }),
      ]),
    );
    children.push(
      note("配置位置：/etc/syntropy-update/update.json 的 adminTokens；改完用 systemctl restart syntropy-update 生效。"),
    );
  } else if (state.token) {
    children.push(note("令牌被服务端拒绝（401）。请确认它与 adminTokens 里的一条完全一致。"));
  } else {
    children.push(note("管理接口都要 Bearer Token。把服务端配置里 adminTokens 的一条粘到右上角后点「使用」。"));
    children.push(
      note("令牌只存在于本标签页的 sessionStorage，不会写进 cookie，也不会随页面资源一起缓存。"),
    );
  }
  return card("需要管理令牌", children, { subtitle: "升级服务 · 管理面" });
}
function stateCounts(items) {
  const counts = {};
  for (const item of items ?? []) {
    const key = item.state ?? "unknown";
    counts[key] = (counts[key] ?? 0) + 1;
  }
  return counts;
}

function countBadges(counts, labels, tones) {
  const entries = Object.entries(counts);
  if (!entries.length) return el("span", { class: "muted", text: "—" });
  return el(
    "div",
    { class: "row" },
    entries.map(([key, value]) => badge((labels[key] ?? key) + " " + value, tones[key])),
  );
}

async function overviewView() {
  const [components, releases, distributions, inventory, plans, policies, ignores, results, audit] = await Promise.all([
    call(API.components),
    call(API.releases),
    call(API.distributions),
    call(API.inventory),
    call(API.plans),
    call(API.policies),
    call(API.ignores),
    call(API.results, undefined, { query: { limit: 8 } }),
    call(API.audit, undefined, { query: { limit: 8 } }),
  ]);

  const componentList = components.components ?? [];
  const releaseList = releases.releases ?? [];
  const distributionList = distributions.distributions ?? [];
  const targets = inventory.targets ?? [];
  const planList = plans.plans ?? [];
  const policyList = policies.policies ?? [];
  const ignoreList = ignores.ignores ?? [];
  const resultList = results.results ?? [];
  const auditEntries = audit.entries ?? [];

  state.counts = {
    components: componentList.length,
    releases: releaseList.length,
    distributions: distributionList.length,
    inventory: targets.length,
    plans: planList.length,
    policies: policyList.length,
    ignores: ignoreList.length,
    results: resultList.length,
  };
  renderTabs();

  const published = releaseList.filter((release) => !release.yanked).length;
  const yanked = releaseList.length - published;
  const health = state.health ?? {};

  const children = [
    tiles([
      ["组件", componentList.length, "受管对象"],
      ["发布", releaseList.length, published + " 上架 / " + yanked + " 撤回"],
      ["分发定义", distributionList.length, "按渠道的 BOM"],
      ["目标库存", targets.length, "已上报的桌面/节点"],
      ["升级计划", planList.length, "由库存计算"],
      ["分发策略", policyList.length, "(组件 × 渠道)"],
      ["忽略集合", ignoreList.length, "目标自己声明的"],
      ["升级结果", resultList.length, "最近回传的一次"],
    ]),
  ];

  const latest = planList.slice().sort((left, right) => (right.createdAtMs ?? 0) - (left.createdAtMs ?? 0))[0];
  if (latest) {
    const items = latest.items ?? [];
    const counts = stateCounts(items);
    const attention = items.filter((item) => item.state !== "pending").slice(0, 8);
    children.push(
      card("最近一次升级计划", [
        el("div", { class: "row" }, [
          mono(latest.id),
          badge("渠道 " + latest.channel, "info"),
          el("span", { class: "muted", text: "计算于 " + timestamp(latest.createdAtMs) }),
          el("button", {
            class: "ghost tiny",
            type: "button",
            text: "去计划页",
            onClick: () => {
              location.hash = "#/plans";
            },
          }),
        ]),
        countBadges(counts, STATE_LABEL, STATE_TONE),
        attention.length
          ? table(
              ["目标", "组件", "动作", "状态", "审批", "原因"],
              attention.map((item) => [
                item.targetKind + ":" + item.targetId,
                item.component,
                badge(ACTION_LABEL[item.action] ?? item.action, "info"),
                badge(STATE_LABEL[item.state] ?? item.state, STATE_TONE[item.state]),
                badge(APPROVAL_LABEL[item.approval] ?? item.approval, APPROVAL_TONE[item.approval]),
                item.reason,
              ]),
              { empty: "没有需要人工处理的项目" },
            )
          : note("计划里没有需要人工处理的步骤。"),
      ]),
    );
  }

  if (!componentList.length) {
    children.push(
      card("快速开始", [
        note("服务端还是空的。按这个顺序灌一次数据，桌面端就能走升级通道了："),
        el("ol", { class: "steps" }, [
          el("li", { text: "在「组件」页登记要分发的组件（mbot / nbot / desktop / templates …）。" }),
          el("li", { text: "在「发布」页新建发布并上传各平台制品（内容制品传平台无关的 .tar.gz）。" }),
          el("li", { text: "在「分发定义」页把各组件版本锁成一个 BOM（必须先发布到同渠道）。" }),
          el("li", { text: "在「分发策略」页设置审批口径与灰度百分比。" }),
          el("li", { text: "目标端上报库存后，「升级计划」页就能看到计划。" }),
        ]),
      ]),
    );
  }

  children.push(
    el("div", { class: "grid-2" }, [
      card(
        "服务",
        [
          el("div", { class: "row" }, [
            health.online ? badge("在线", "ok") : badge("不可达", "danger"),
            health.adminEnabled === false ? badge("管理接口未启用", "danger") : badge("管理接口已启用", "ok"),
            health.readRequired ? badge("读取接口鉴权：开", "info") : badge("读取接口鉴权：关", "warn"),
            health.deviceRequired ? badge("上报接口鉴权：开", "ok") : badge("上报接口鉴权：关", "warn"),
            health.clientRequired ? badge("客户端令牌：已配", "info") : badge("客户端令牌：未配", "warn"),
          ]),
          note("公开地址：" + location.origin),
          note("读取接口（清单 / 制品 / 分发定义）默认公开，thin 安装器首启才能拉件；要收紧就把 requireReadTokens 打开，并给目标端配好令牌。上报接口（计划 / 库存 / 忽略 / 升级结果）配了 clientTokens 即校验，requireDeviceTokens 让没配令牌时也拒绝匿名写入。管理接口始终要求令牌。"),
        ],
        { subtitle: "健康与鉴权" },
      ),
      card(
        "最近审计",
        [
          table(
            ["时间", "动作", "操作者", "对象"],
            auditEntries.map((entry) => [
              timestamp(entry.atMs),
              entry.action,
              entry.actor,
              entry.subject,
            ]),
            { empty: "还没有审计记录" },
          ),
        ],
        { subtitle: "最近 " + auditEntries.length + " 条", flush: true },
      ),
    ]),
  );

  return children;
}

async function componentsView() {
  const { components = [] } = await call(API.components);
  const page = "components";

  const name = textInput("mbot");
  const display = textInput("Master 二进制");
  const kind = select(KINDS);
  const versioning = select(VERSIONING);
  const minProduct = textInput("0.4.0");

  const detail = el("div", {});
  const showReleases = (component) =>
    void run("查询 " + component + " 的发布", async () => {
      const { releases = [] } = await call(API.releases, undefined, { query: { component } });
      detail.replaceChildren(
        card(
          component + " 的发布",
          [
            table(
              ["版本", "渠道", "状态", "发布时间", "发布 ID"],
              releases.map((entry) => [
                entry.version,
                badge(entry.channel, "info"),
                entry.yanked ? badge("已撤回", "danger") : badge("已上架", "ok"),
                timestamp(entry.publishedAtMs),
                el("span", { class: "row" }, [mono(entry.id), copyChip(entry.id, "复制")]),
              ]),
              { empty: "这个组件还没有发布记录" },
            ),
          ],
          { subtitle: releases.length + " 条", flush: true },
        ),
      );
    });

  const rows = () => {
    const query = filterValue(page, "q", "");
    const filtered = components.filter((component) =>
      matches(query, component.name + " " + component.displayName + " " + component.kind),
    );
    return filtered.map((component) => [
      el("span", { class: "row" }, [mono(component.name), copyChip(component.name, "复制")]),
      component.displayName,
      badge(KIND_LABEL[component.kind] ?? component.kind, "info"),
      component.versioning,
      component.minProductVersion,
      timestamp(component.updatedAtMs),
      el("button", {
        class: "ghost tiny",
        type: "button",
        text: "发布记录",
        onClick: () => showReleases(component.name),
      }),
    ]);
  };

  const host = el("div");
  const paint = () =>
    host.replaceChildren(
      table(
        ["名称", "显示名", "类型", "版本线", "最低产品版本", "更新时间", "操作"],
        rows(),
        { empty: filterValue(page, "q", "") ? "没有匹配的组件" : "还没有登记组件" },
      ),
    );
  const search = filterInput(page, "q", "按名称/显示名/类型过滤", "");
  bindFilter(search, page, "q", paint);
  paint();

  return [
    card(
      "登记组件",
      [
        note("组件是分发的原子单位：先登记，再发布版本，最后进 BOM。重名登记不会覆盖已有组件。"),
        el("div", { class: "form-grid" }, [
          field("名称", name),
          field("显示名", display),
          field("类型", kind),
          field("版本线", versioning),
          field("最低产品版本", minProduct),
          el("button", {
            type: "button",
            text: "登记",
            onClick: () =>
              run("登记组件", () =>
                call(API.component, undefined, {
                  body: {
                    displayName: display.value.trim() || name.value.trim(),
                    kind: kind.value,
                    name: name.value.trim(),
                    versioning: versioning.value,
                    ...(minProduct.value.trim() ? { minProductVersion: minProduct.value.trim() } : {}),
                  },
                }),
              ),
          }),
        ]),
        note("类型决定制品形态：content 是平台无关的 .tar.gz（templates/skills），其余按 (os, arch) 上传。"),
      ],
      { subtitle: "POST /v1/admin/components" },
    ),
    card("组件", [el("div", { class: "row" }, [search]), host], {
      subtitle: components.length + " 个",
    }),
    detail,
  ];
}

async function releasesView() {
  const [releasePage, componentPage] = await Promise.all([call(API.releases), call(API.components)]);
  const releases = releasePage.releases ?? [];
  const components = componentPage.components ?? [];
  const page = "releases";

  const component = textInput("mbot");
  component.setAttribute("list", "component-names");
  const datalist = el(
    "datalist",
    { id: "component-names" },
    components.map((entry) => el("option", { value: entry.name })),
  );
  const version = textInput("1.2.0");
  const channel = select(CHANNELS);
  const notes = textInput("发布说明");
  const notesUrl = textInput("https://…");
  const minSupported = textInput("0.3.0");
  const safeVersion = textInput("0.3.1");
  const safeUntil = textInput("2026-12-31T00:00:00Z");
  const publishNow = checkbox("创建后立即上架（指向渠道指针）", true);

  const detail = el("div", {});
  const uploadPanel = (entry) => {
    const isContent = components.find((item) => item.name === entry.component)?.kind === "content";
    const os = select(PLATFORMS);
    const arch = select(ARCHES);
    const file = el("input", { type: "file" });
    const expectedName = entry.component + "-" + entry.version + ".tar.gz";
    const submit = el("button", {
      type: "button",
      text: "上传",
      onClick: () =>
        run("上传制品", async () => {
          const picked = file.files?.[0];
          if (!picked) throw new Error("先选一个制品文件");
          if (isContent && picked.name !== expectedName) {
            throw new Error("内容制品必须命名为 " + expectedName);
          }
          state.openReleaseId = entry.id;
          const digest = await sha256Hex(picked);
          const artifact = await call(
            API.artifact,
            isContent
              // 路径里不能有空段：any/any 是服务端约定的「这份制品不挑平台」。
              ? { arch: "any", id: entry.id, os: "any" }
              : { arch: arch.value, id: entry.id, os: os.value },
            { body: picked, headers: { "X-Filename": picked.name, "X-Sha256": digest } },
          );
          setStatus("已上传 " + artifact.filename + "（" + humanSize(artifact.size) + "）", "ok");
        }),
    });
    return card(
      "上传制品 · " + entry.component + "@" + entry.version,
      [
        note(
          isContent
            ? "内容制品平台无关（上传到 artifacts/any/any）：不要选平台/架构，文件名必须是 " + expectedName + "（顶层即组件目录内容）。"
            : "按平台上传二进制/安装器；文件名随 X-Filename 一起记录，服务端会核对 sha256。",
        ),
        el("div", { class: "row" }, [
          ...(isContent ? [] : [field("平台", os), field("架构", arch)]),
          field("文件", file),
          submit,
        ]),
      ],
      { subtitle: "PUT /v1/admin/releases/{id}/artifacts/{os}/{arch}" },
    );
  };

  const artifactsTable = (artifacts) =>
    table(
      ["平台", "架构", "文件名", "大小", "sha256", "下载"],
      artifacts.map((artifact) => [
        artifact.os || "content",
        artifact.arch || "—",
        artifact.filename,
        humanSize(artifact.size),
        el("span", { class: "row" }, [mono(String(artifact.sha256).slice(0, 16) + "…"), copyChip(artifact.sha256)]),
        el("a", {
          href:
            apiPath(API.artifactDownload, { sha256: artifact.blobSha256 ?? artifact.sha256 }) +
            "?filename=" +
            encodeURIComponent(artifact.filename ?? ""),
          text: "下载",
          download: artifact.filename,
        }),
      ]),
      { empty: "还没有上传制品" },
    );

  const loadArtifacts = async (entry) => {
    const payload = await call(API.releaseDetail, { id: entry.id });
    detail.replaceChildren(
      card(
        "制品 · " + entry.component + "@" + entry.version,
        [uploadPanel(entry), artifactsTable(payload.artifacts ?? []), jsonDetails(payload.release, "发布记录 JSON")],
        { subtitle: entry.id },
      ),
    );
  };
  const showArtifacts = (entry) => {
    // 写操作都会走到 render()，把「正在看哪份发布」记下来，重渲染后自动展开同一份。
    state.openReleaseId = entry.id;
    void run("加载 " + entry.component + "@" + entry.version + " 的制品", () => loadArtifacts(entry));
  };

  const rows = () => {
    const componentFilter = filterValue(page, "component", "");
    const channelFilter = filterValue(page, "channel", "");
    const statusFilter = filterValue(page, "status", "");
    return releases
      .filter((entry) => matches(componentFilter, entry.component))
      .filter((entry) => !channelFilter || entry.channel === channelFilter)
      .filter((entry) => !statusFilter || (statusFilter === "yanked") === Boolean(entry.yanked))
      .map((entry) => [
        el("span", { class: "row" }, [mono(entry.component), copyChip(entry.component, "复制")]),
        entry.version,
        badge(entry.channel, "info"),
        entry.yanked ? badge("已撤回", "danger") : badge("已上架", "ok"),
        timestamp(entry.publishedAtMs),
        el("span", { class: "row" }, [
          el("button", { class: "ghost tiny", type: "button", text: "制品", onClick: () => showArtifacts(entry) }),
          el("button", {
            class: "ghost tiny",
            type: "button",
            text: "上架",
            onClick: () => run("上架", () => call(API.publish, { id: entry.id }, { body: {} })),
          }),
          entry.yanked
            ? el("button", {
                class: "ghost tiny",
                type: "button",
                text: "恢复",
                onClick: () => run("恢复发布", () => call(API.publish, { id: entry.id }, { body: {} })),
              })
            : destructiveButton(
                "撤回",
                "撤回后渠道指针不再指向 " + entry.component + "@" + entry.version + "，确定？",
                () => run("撤回", () => call(API.yank, { id: entry.id }, { body: {} })),
              ),
        ]),
      ]);
  };

  const host = el("div");
  const paint = () => {
    const view = rows();
    host.replaceChildren(
      table(["组件", "版本", "渠道", "状态", "发布时间", "操作"], view, {
        empty: releases.length ? "没有匹配的发布" : "还没有发布记录",
      }),
    );
  };
  const componentFilter = filterInput(page, "component", "按组件过滤", "");
  const channelFilter = select(["", ...CHANNELS]);
  channelFilter.value = filterValue(page, "channel", "");
  const statusFilter = select(["", "published", "yanked"], undefined);
  const statusSelect = el("select", {}, [
    el("option", { text: "全部状态", value: "" }),
    el("option", { text: "已上架", value: "published" }),
    el("option", { text: "已撤回", value: "yanked" }),
  ]);
  statusSelect.value = filterValue(page, "status", "");
  statusSelect.addEventListener("change", () => {
    state.filters[page + ":status"] = statusSelect.value;
    paint();
  });
  channelFilter.addEventListener("change", () => {
    state.filters[page + ":channel"] = channelFilter.value;
    paint();
  });
  bindFilter(componentFilter, page, "component", paint);
  paint();
  if (state.openReleaseId) {
    const reopened = releases.find((entry) => entry.id === state.openReleaseId);
    if (reopened) void loadArtifacts(reopened).catch(() => undefined);
  }

  return [
    datalist,
    card(
      "新建发布",
      [
        el("div", { class: "form-grid" }, [
          field("组件", component),
          field("版本", version),
          field("渠道", channel),
          field("说明", notes),
          field("说明链接", notesUrl),
          field("最低支持版本", minSupported),
          field("熔断最低安全版本", safeVersion),
          field("熔断到期（RFC3339）", safeUntil),
        ]),
        el("div", { class: "row" }, [
          publishNow,
          el("button", {
            type: "button",
            text: "创建发布",
            onClick: () =>
              run("创建发布", async () => {
                const killSwitch = safeVersion.value.trim()
                  ? { expiresAt: safeUntil.value.trim(), minimumSafeVersion: safeVersion.value.trim() }
                  : undefined;
                if (killSwitch && !killSwitch.expiresAt) {
                  throw new Error("填了熔断最低安全版本就必须填到期时间");
                }
                await call(API.release, undefined, {
                  body: {
                    channel: channel.value,
                    component: component.value.trim(),
                    version: version.value.trim(),
                    publish: publishNow.querySelector("input").checked,
                    ...(notes.value.trim() ? { notes: notes.value.trim() } : {}),
                    ...(notesUrl.value.trim() ? { notesUrl: notesUrl.value.trim() } : {}),
                    ...(minSupported.value.trim() ? { minimumSupportedVersion: minSupported.value.trim() } : {}),
                    ...(killSwitch ? { killSwitch } : {}),
                  },
                });
              }),
          }),
          el("button", {
            type: "button",
            class: "ghost",
            text: "清空表单",
            onClick: () => {
              component.value = "";
              version.value = "";
              notes.value = "";
              notesUrl.value = "";
              minSupported.value = "";
              safeVersion.value = "";
              safeUntil.value = "";
            },
          }),
        ]),
        note("版本必须是 MAJOR.MINOR.PATCH；同一 (组件, 渠道, 版本) 只能发布一次，重复会被拒绝。"),
      ],
      { subtitle: "POST /v1/admin/releases" },
    ),
    card(
      "发布记录",
      [el("div", { class: "row" }, [componentFilter, channelFilter, statusSelect]), host],
      { subtitle: releases.length + " 条" },
    ),
    detail,
  ];
}
async function distributionsView() {
  const { distributions = [] } = await call(API.distributions);
  const page = "distributions";
  const channel = select(CHANNELS);
  const productVersion = textInput("1.2.0");
  const pending = [];
  const list = el("div", { class: "row" });

  const addRow = (name, version) => {
    const nameInput = textInput("组件名", name);
    const versionInput = textInput("版本", version);
    const entry = { name: nameInput, version: versionInput };
    pending.push(entry);
    const row = el("div", { class: "row" }, [
      nameInput,
      versionInput,
      el("button", {
        class: "ghost tiny",
        type: "button",
        text: "移除",
        onClick: () => {
          const index = pending.indexOf(entry);
          if (index >= 0) pending.splice(index, 1);
          row.remove();
        },
      }),
    ]);
    list.append(row);
  };
  const quickName = textInput("mbot");
  const quickVersion = textInput("1.2.0");

  const componentCell = (distribution) => {
    const components = distribution.components ?? [];
    if (!components.length) return el("span", { class: "muted", text: "—" });
    return el(
      "div",
      { class: "row" },
      components.map((entry) =>
        el("span", { class: "chip" }, [mono(entry.name), el("span", { class: "muted", text: "@" + entry.version })]),
      ),
    );
  };

  const query = filterValue(page, "channel", "");
  const host = el("div");
  const paint = () => {
    const filtered = distributions.filter((entry) => !query || entry.channel === query);
    host.replaceChildren(
      table(
        ["渠道", "产品版本", "组件", "发布时间", "详情"],
        filtered.map((entry) => [
          badge(entry.channel, "info"),
          entry.productVersion,
          componentCell(entry),
          timestamp(entry.publishedAtMs),
          jsonDetails(entry, "BOM JSON"),
        ]),
        { empty: distributions.length ? "没有该渠道的分发定义" : "还没有分发定义" },
      ),
    );
  };
  const channelFilter = el("select", {}, [
    el("option", { text: "全部渠道", value: "" }),
    ...CHANNELS.map((name) => el("option", { text: name, value: name })),
  ]);
  channelFilter.value = query;
  channelFilter.addEventListener("change", () => {
    state.filters[page + ":channel"] = channelFilter.value;
    paint();
  });
  paint();

  return [
    card(
      "发布分发定义（BOM）",
      [
        el("div", { class: "form-grid" }, [field("渠道", channel), field("产品版本", productVersion)]),
        el("div", { class: "row" }, [
          el("button", { class: "ghost", type: "button", text: "添加一行", onClick: () => addRow("", "") }),
          el("button", {
            type: "button",
            text: "发布分发定义",
            onClick: () =>
              run("发布分发定义", () =>
                call(API.distribution, undefined, {
                  body: {
                    channel: channel.value,
                    components: pending
                      .map((entry) => ({ name: entry.name.value.trim(), version: entry.version.value.trim() }))
                      .filter((entry) => entry.name && entry.version),
                    productVersion: productVersion.value.trim(),
                  },
                }),
              ),
          }),
          el("span", { class: "muted", text: "快速添加：" }),
          quickName,
          quickVersion,
          el("button", {
            class: "ghost tiny",
            type: "button",
            text: "加入",
            onClick: () => addRow(quickName.value.trim(), quickVersion.value.trim()),
          }),
        ]),
        list,
        note("BOM 里的每个 (组件, 版本) 都必须已经在同一渠道发布过，否则服务端会拒绝；同一 (渠道, 产品版本) 不能重复发布。"),
      ],
      { subtitle: "POST /v1/admin/distributions" },
    ),
    card("已发布的分发定义", [el("div", { class: "row" }, [channelFilter]), host], {
      subtitle: distributions.length + " 条",
    }),
  ];
}

async function inventoryView() {
  const { targets = [] } = await call(API.inventory);
  const page = "inventory";
  const host = el("div");

  const componentsCell = (target) => {
    const entries = Object.entries(target.components ?? {});
    if (!entries.length) return el("span", { class: "muted", text: "—" });
    return el("div", { class: "row" }, [
      badge(entries.length + " 个组件", "info"),
      jsonDetails(target.components, "组件清单 JSON"),
    ]);
  };

  const paint = () => {
    const query = filterValue(page, "q", "");
    const filtered = targets.filter((target) => matches(query, target.id + " " + target.kind));
    host.replaceChildren(
      table(
        ["目标", "类型", "产品版本", "组件", "标签", "上报时间"],
        filtered.map((target) => [
          el("span", { class: "row" }, [mono(target.id), copyChip(target.id, "复制")]),
          badge(target.kind || "unknown", "info"),
          target.productVersion,
          componentsCell(target),
          Object.entries(target.labels ?? {}).length
            ? jsonDetails(target.labels, "标签 JSON")
            : el("span", { class: "muted", text: "—" }),
          timestamp(target.reportedAtMs),
        ]),
        { empty: targets.length ? "没有匹配的目标" : "还没有目标上报库存" },
      ),
    );
  };
  const search = filterInput(page, "q", "按目标 ID / 类型过滤", "");
  bindFilter(search, page, "q", paint);
  paint();

  return [
    card("上报的库存", [el("div", { class: "row" }, [search]), host], {
      subtitle: targets.length + " 个目标 · 由目标端上报（POST /v1/inventory/{id}）",
    }),
    card("怎么产生库存", [
      note("库存由目标自己上报：桌面端采集「本机组件 + 产品版本」，远端节点由 Master 汇总。"),
      note("本页只读；升级计划用 POST /v1/plan 按库存计算，侧栏「升级计划」页可看最近几次结果。"),
    ]),
  ];
}

async function ignoresView() {
  const { ignores = [] } = await call(API.ignores);
  const page = "ignores";
  const host = el("div");

  const paint = () => {
    const query = filterValue(page, "q", "");
    const filtered = ignores.filter((entry) => matches(query, entry.targetId + " " + entry.component + " " + entry.version));
    host.replaceChildren(
      table(
        ["目标", "组件", "被忽略的版本", "操作者", "更新时间"],
        filtered.map((entry) => [
          mono(entry.targetId),
          mono(entry.component),
          entry.version,
          entry.actor,
          timestamp(entry.updatedAtMs),
        ]),
        { empty: ignores.length ? "没有匹配的忽略记录" : "还没有目标忽略某个版本" },
      ),
    );
  };
  const search = filterInput(page, "q", "按目标/组件/版本过滤", "");
  bindFilter(search, page, "q", paint);
  paint();

  return [
    card("忽略集合", [el("div", { class: "row" }, [search]), host], { subtitle: ignores.length + " 条" }),
    card("口径", [
      note(
        "「忽略此版本」由目标端自己上报（对应 Codex app 的 dismissed_version）：一个目标、一个组件只记一个版本；" +
          "渠道上出现更新的版本时忽略自然失效。",
      ),
      note("目标端写：POST /v1/targets/{id}/ignores；撤销：DELETE /v1/targets/{id}/ignores/{component}。管理面只读。"),
    ]),
  ];
}

async function resultsView() {
  const { results = [] } = await call(API.results, undefined, { query: { limit: 200 } });
  const page = "results";
  const stateFilter = filterValue(page, "state", "");
  const host = el("div");

  const paint = () => {
    const query = filterValue(page, "q", "");
    const filtered = results.filter((entry) => {
      if (stateFilter && entry.state !== stateFilter) return false;
      return matches(query, entry.targetId + " " + entry.component + " " + entry.from + " " + entry.to + " " + entry.message);
    });
    host.replaceChildren(
      table(
        ["目标", "组件", "结果", "版本", "说明", "时间"],
        filtered.map((entry) => [
          mono(entry.targetId),
          mono(entry.component),
          badge(RESULT_LABEL[entry.state] ?? entry.state, RESULT_TONE[entry.state] ?? "info"),
          (entry.from || "?") + " → " + (entry.to || "?"),
          el("span", { class: "row" }, [
            el("span", { class: "muted", text: String(entry.message ?? "").slice(0, 60) }),
            jsonDetails(entry, "JSON"),
          ]),
          timestamp(entry.atMs),
        ]),
        { empty: results.length ? "没有匹配的升级结果" : "还没有目标回传升级结果" },
      ),
    );
  };

  const select = el(
    "select",
    {},
    [["", "全部结果"], ...Object.entries(RESULT_LABEL)].map(([value, label]) =>
      el("option", { text: label, value }),
    ),
  );
  select.value = stateFilter;
  select.addEventListener("change", () => {
    state.filters[page + ":state"] = select.value;
    paint();
  });

  const search = filterInput(page, "q", "按目标/组件/版本/说明过滤", "");
  bindFilter(search, page, "q", paint);
  paint();

  return [
    card("升级结果", [el("div", { class: "row" }, [select, search]), host], {
      subtitle: results.length + " 条（最近 200 条）",
    }),
    card("口径", [
      note(
        "库存回答「现在跑的是哪一版」，升级结果回答「这一次升级成没成、失败在哪一步」——" +
          "两者由目标端分别上报，服务端不替目标推断结果。",
      ),
      note("目标端写：POST /v1/targets/{id}/results；管理面只读。"),
    ]),
  ];
}

async function plansView() {
  const { plans = [] } = await call(API.plans);
  const page = "plans";
  const channelFilter = filterValue(page, "channel", "");
  const stateFilter = filterValue(page, "state", "");
  const detail = el("div", {});
  const host = el("div");

  const showPlan = (plan) =>
    void run("加载计划 " + plan.id, async () => {
      const payload = await call(API.plan, { id: plan.id });
      detail.replaceChildren(
        card(
          "计划明细 · " + payload.id,
          [
            el("div", { class: "row" }, [
              badge("渠道 " + payload.channel, "info"),
              el("span", { class: "muted", text: "计算于 " + timestamp(payload.createdAtMs) }),
              countBadges(stateCounts(payload.items), STATE_LABEL, STATE_TONE),
            ]),
            table(
              ["目标", "组件", "版本变化", "动作", "状态", "审批", "原因"],
              (payload.items ?? []).map((item) => [
                item.targetKind + ":" + item.targetId,
                mono(item.component),
                (item.from || "—") + " → " + (item.to || "—"),
                badge(ACTION_LABEL[item.action] ?? item.action, "info"),
                badge(STATE_LABEL[item.state] ?? item.state, STATE_TONE[item.state]),
                badge(APPROVAL_LABEL[item.approval] ?? item.approval, APPROVAL_TONE[item.approval]),
                item.reason,
              ]),
              { empty: "这次计划没有任何步骤（已经是最新）" },
            ),
            jsonDetails(payload, "计划 JSON"),
          ],
          { subtitle: (payload.items ?? []).length + " 步", flush: false },
        ),
      );
    });

  const paint = () => {
    const filtered = plans
      .filter((plan) => !channelFilter || plan.channel === channelFilter)
      .filter((plan) => {
        if (!stateFilter) return true;
        return (plan.items ?? []).some((item) => item.state === stateFilter);
      });
    host.replaceChildren(
      table(
        ["计划 ID", "渠道", "计算时间", "步骤", "状态分布", "操作"],
        filtered.map((plan) => [
          el("span", { class: "row" }, [mono(plan.id), copyChip(plan.id, "复制")]),
          badge(plan.channel, "info"),
          timestamp(plan.createdAtMs),
          String((plan.items ?? []).length),
          countBadges(stateCounts(plan.items), STATE_LABEL, STATE_TONE),
          el("button", { class: "ghost tiny", type: "button", text: "展开", onClick: () => showPlan(plan) }),
        ]),
        { empty: plans.length ? "没有匹配的计划" : "还没有升级计划（目标上报库存后按渠道计算）" },
      ),
    );
  };

  const channel = el("select", {}, [
    el("option", { text: "全部渠道", value: "" }),
    ...CHANNELS.map((name) => el("option", { text: name, value: name })),
  ]);
  channel.value = channelFilter;
  channel.addEventListener("change", () => {
    state.filters[page + ":channel"] = channel.value;
    paint();
  });
  const stateSelect = el("select", {}, [
    el("option", { text: "全部状态", value: "" }),
    ...Object.entries(STATE_LABEL).map(([value, label]) => el("option", { text: label, value })),
  ]);
  stateSelect.value = stateFilter;
  stateSelect.addEventListener("change", () => {
    state.filters[page + ":state"] = stateSelect.value;
    paint();
  });
  paint();

  return [
    card("最近的升级计划", [el("div", { class: "row" }, [channel, stateSelect]), host], {
      subtitle: plans.length + " 次计算",
    }),
    detail,
    card("口径", [
      note("计划由目标提交库存触发生成（POST /v1/plan）；pending 是会被执行的步骤，deferred 在维护窗口外，blocked 需要审批或人工确认。"),
    ]),
  ];
}
async function policiesView() {
  const [policyPage, componentPage] = await Promise.all([call(API.policies), call(API.components)]);
  const policies = policyPage.policies ?? [];
  const components = componentPage.components ?? [];

  const component = textInput("mbot");
  component.setAttribute("list", "policy-component-names");
  const datalist = el(
    "datalist",
    { id: "policy-component-names" },
    components.map((entry) => el("option", { value: entry.name })),
  );
  const channel = select(CHANNELS);
  const approval = select(APPROVALS, "confirm");
  const percentage = numberInput(100, 0, 100);
  const window = textInput("18:00-22:00");
  const targets = textInput("node-*, desktop-1");

  // 路径参数可能是字符串（表格行）也可能是输入节点（新建表单），统一在点击时取值。
  const readRef = (ref) => (ref && typeof ref === "object" ? String(ref.value ?? "").trim() : String(ref ?? "").trim());
  const saveButton = (componentRef, channelRef, inputs) =>
    el("button", {
      type: "button",
      text: "保存",
      onClick: () =>
        run("保存策略", async () => {
          const globs = inputs.targets.value
            .split(",")
            .map((value) => value.trim())
            .filter(Boolean);
          await call(
            API.policy,
            { channel: readRef(channelRef), component: readRef(componentRef) },
            {
              body: {
                approval: inputs.approval.value,
                percentage: Number(inputs.percentage.value),
                ...(inputs.window.value.trim() ? { window: inputs.window.value.trim() } : {}),
                ...(globs.length ? { targets: globs } : {}),
              },
            },
          );
        }),
    });

  const rows = policies.map((policy) => {
    const approvalInput = select(APPROVALS, policy.approval);
    const percentageInput = numberInput(policy.percentage ?? 100, 0, 100);
    const windowInput = textInput("不限", policy.window);
    const targetsInput = textInput("全部目标", (policy.targets ?? []).join(", "));
    return [
      mono(policy.component),
      badge(policy.channel, "info"),
      approvalInput,
      percentageInput,
      windowInput,
      targetsInput,
      el("div", { class: "row" }, [
        saveButton(policy.component, policy.channel, {
          approval: approvalInput,
          percentage: percentageInput,
          targets: targetsInput,
          window: windowInput,
        }),
        jsonDetails(policy, "JSON"),
      ]),
    ];
  });

  return [
    datalist,
    card(
      "新建 / 覆盖策略",
      [
        el("div", { class: "form-grid" }, [
          field("组件", component),
          field("渠道", channel),
          field("审批口径", approval),
          field("灰度百分比", percentage),
          field("维护窗口（UTC）", window),
          field("目标白名单", targets),
        ]),
        el("div", { class: "row" }, [saveButton(component, channel, { approval, percentage, targets, window })]),
        note("同 (组件, 渠道) 重复保存即覆盖。目标白名单是 glob，逗号分隔，留空表示全部目标。"),
      ],
      { subtitle: "PUT /v1/admin/policies/{component}/{channel}" },
    ),
    card(
      "分发策略",
      [
        table(["组件", "渠道", "审批", "灰度 %", "维护窗口(UTC)", "目标白名单", "操作"], rows, {
          empty: "还没有策略：没有策略时按组件类型取默认值（内容制品 auto，其余 confirm，100%）。",
        }),
        note("审批口径：auto 自动安装、confirm 安装前确认、manual 仅手动；灰度按 (目标, 组件, 发布) 的确定性分桶。"),
      ],
      { subtitle: policies.length + " 条" },
    ),
  ];
}

async function auditView() {
  const page = "audit";
  const limit = filterValue(page, "limit", "100");
  const { entries = [] } = await call(API.audit, undefined, { query: { limit } });
  const host = el("div");

  const paint = () => {
    const query = filterValue(page, "q", "");
    const filtered = entries.filter((entry) =>
      matches(query, [entry.action, entry.actor, entry.subject, entry.detail].join(" ")),
    );
    host.replaceChildren(
      table(
        ["时间", "动作", "操作者", "对象", "详情"],
        filtered.map((entry) => [
          timestamp(entry.atMs),
          badge(entry.action, "info"),
          entry.actor,
          entry.subject,
          el("span", { class: "row" }, [
            el("span", { class: "muted", text: String(entry.detail ?? "").slice(0, 60) }),
            entry.detail || entry.traceId ? jsonDetails(entry, "JSON") : null,
          ]),
        ]),
        { empty: entries.length ? "没有匹配的审计记录" : "还没有审计记录" },
      ),
    );
  };
  const search = filterInput(page, "q", "按动作/操作者/对象过滤", "");
  bindFilter(search, page, "q", paint);
  paint();

  const limitSelect = el("select", {}, [50, 100, 200, 500].map((value) => el("option", { text: "最近 " + value + " 条", value: String(value) })));
  limitSelect.value = limit;
  limitSelect.addEventListener("change", () => {
    state.filters[page + ":limit"] = limitSelect.value;
    void render();
  });

  return [
    card("审计", [el("div", { class: "row" }, [limitSelect, search]), host], {
      subtitle: "GET /v1/admin/audit?limit=" + limit,
    }),
    card("说明", [
      note("所有写操作都会落一条审计：发布、上传制品、渠道指针、BOM、策略、忽略、库存上报与计划计算。"),
      note("X-Actor 头会被记录下来；管理页发出的请求统一带 admin-ui。"),
    ]),
  ];
}

async function helpView() {
  let publicKey = "";
  try {
    publicKey = (await callText(API.pubkey)).trim();
  } catch (error) {
    publicKey = "读取失败：" + describeError(error);
  }

  const endpointRows = Object.entries(API).map(([name, entry]) => {
    const { method, path } = splitEntry(entry);
    return [badge(method, method === "GET" ? "info" : "warn"), mono(path), el("span", { class: "muted", text: name })];
  });

  return [
    card(
      "上线前必读",
      [
        el("ol", { class: "steps" }, [
          el("li", { text: "管理接口必须配 adminTokens；没配时 /v1/admin/* 一律 401，本页只会显示「管理接口未启用」。" }),
          el("li", { text: "令牌放在服务端 /etc/syntropy-update/update.json 的 adminTokens，改完 systemctl restart syntropy-update。" }),
          el("li", { text: "deploy/install.sh 会在首次部署时自动生成一个 48 位十六进制令牌并打印出来。" }),
          el("li", { text: "本页把令牌存在 sessionStorage，关闭标签页即失效；页面资源带 no-store，不会进缓存。" }),
        ]),
      ],
      { subtitle: "令牌与部署" },
    ),
    card(
      "更新签名公钥",
      [note("桌面端只认这个公钥签出来的清单与 BOM。换密钥等于换信任根，务必随桌面版本分发。"), el("pre", { text: publicKey })],
      { subtitle: "GET /v1/pubkey" },
    ),
    card(
      "本页调用的接口",
      [
        table(["方法", "路径", "名称"], endpointRows, { empty: "无" }),
        note("这张表就是 app.js 顶部的 API 常量表，internal/api 的结构测试会与 routes() 双向比对。"),
      ],
      { subtitle: Object.keys(API).length + " 条" },
    ),
    card("升级链路", [
      note("升级永远由桌面应用实施：本服务只提供事实（发布/制品/BOM/库存）与策略（审批/灰度/窗口/熔断）。"),
      note("桌面端 update.check → 验签 BOM → 取制品并校验 sha256 → 按计划暂存/安装；节点制品经本机 Master 落盘。"),
    ]),
  ];
}
const PAGES = [
  { key: "overview", label: "概览", title: "服务概览", hint: "组件 / 发布 / 库存 / 计划一眼看完", render: overviewView },
  { key: "components", label: "组件", title: "组件", hint: "登记受管组件：mbot / nbot / desktop / templates …", render: componentsView },
  { key: "releases", label: "发布", title: "发布与制品", hint: "新建发布、上传制品、上架与撤回", render: releasesView },
  { key: "distributions", label: "分发定义", title: "分发定义（BOM）", hint: "把各组件版本锁成一个渠道基线", render: distributionsView },
  { key: "inventory", label: "库存", title: "目标库存", hint: "桌面端与节点上报的组件版本", render: inventoryView },
  { key: "ignores", label: "忽略集合", title: "忽略集合", hint: "目标自己声明「这个版本先不升」", render: ignoresView },
  { key: "plans", label: "升级计划", title: "升级计划", hint: "按库存与策略算出来的升级步骤", render: plansView },
  { key: "results", label: "升级结果", title: "升级结果", hint: "目标端回传的每一次组件升级成没成、为什么", render: resultsView },
  { key: "policies", label: "分发策略", title: "分发策略", hint: "审批口径、灰度百分比、维护窗口、目标白名单", render: policiesView },
  { key: "audit", label: "审计", title: "审计", hint: "每一次管理动作都留痕", render: auditView },
  { key: "help", label: "说明", title: "说明", hint: "令牌、部署、公钥与接口清单", render: helpView },
];

const PAGE_MAP = new Map(PAGES.map((page) => [page.key, page]));

function currentPage() {
  const key = location.hash.replace(/^#\/?/, "");
  return PAGE_MAP.has(key) ? key : "overview";
}

function renderTabs() {
  const active = currentPage();
  nodes.tabs.replaceChildren(
    ...PAGES.map((page) => {
      const count = state.counts?.[page.key];
      return el("a", { href: "#/" + page.key, class: page.key === active ? "active" : "" }, [
        el("span", { text: page.label }),
        count === undefined ? null : el("span", { class: "count", text: String(count) }),
      ]);
    }),
  );
}

async function render() {
  renderTabs();
  const page = PAGE_MAP.get(currentPage());
  nodes.pageTitle.textContent = page.title;
  nodes.pageHint.textContent = page.hint;
  document.title = page.label + " · Syntropy 升级服务";
  renderConn();

  if (!state.token) {
    nodes.view.replaceChildren(tokenGateView());
    return;
  }

  nodes.view.replaceChildren(loadingCard());
  try {
    const children = await page.render();
    nodes.view.replaceChildren(...children);
  } catch (error) {
    const message = describeError(error);
    const children = [card("加载失败", [note(message)])];
    if (error?.status === 401) children.push(tokenGateView());
    children.push(
      card("排查建议", [
        note("服务端日志：journalctl -u syntropy-update -n 50 --no-pager"),
        note("命令行自检：curl -sS -H \"Authorization: Bearer $TOKEN\" " + location.origin + splitEntry(API.components).path),
      ]),
    );
    nodes.view.replaceChildren(...children);
    setStatus("加载失败：" + message, "error");
  }
}

nodes.token.value = state.token;
document.getElementById("token-save").addEventListener("click", () => {
  state.token = nodes.token.value.trim();
  if (state.token) sessionStorage.setItem(TOKEN_KEY, state.token);
  else sessionStorage.removeItem(TOKEN_KEY);
  setStatus("");
  void render();
});
document.getElementById("token-clear").addEventListener("click", () => {
  state.token = "";
  nodes.token.value = "";
  sessionStorage.removeItem(TOKEN_KEY);
  setStatus("");
  void render();
});
document.getElementById("refresh").addEventListener("click", () => {
  state.counts = null;
  setStatus("");
  void render();
});
window.addEventListener("hashchange", () => {
  setStatus("");
  void render();
});
document.getElementById("base-url").textContent = location.origin;
void probeHealth().then(() => render());
