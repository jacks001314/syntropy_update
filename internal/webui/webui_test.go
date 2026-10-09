package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func asset(t *testing.T, name string) string {
	t.Helper()
	content, err := fs.ReadFile(Assets(), name)
	if err != nil {
		t.Fatalf("读取内嵌资源 %s: %v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func TestHandlerServesTheAdminPage(t *testing.T) {
	recorder := get(t, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, marker := range []string{
		"Syntropy 升级服务",
		"app.js",
		"app.css",
		"管理令牌",
		`id="tabs"`,
		`id="view"`,
		"<noscript>",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("page is missing %q", marker)
		}
	}
}

// 管理页带着令牌操作：既不能进缓存，也要有能挡住 XSS / 点击劫持的响应头。
func TestHandlerSendsHardeningHeaders(t *testing.T) {
	recorder := get(t, "/")
	headers := map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for name, want := range headers {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	policy := recorder.Header().Get("Content-Security-Policy")
	for _, marker := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(policy, marker) {
			t.Errorf("Content-Security-Policy 缺少 %q：%s", marker, policy)
		}
	}
}

func TestHandlerFallsBackToTheSinglePage(t *testing.T) {
	// 单页应用按 hash 路由，刷新深层链接必须回到同一份 index.html 而不是 404。
	recorder := get(t, "/whatever/deep/link")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "管理令牌") {
		t.Fatal("fallback did not serve the admin page")
	}
}

func TestAssetsCarryTheUI(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "app.css"} {
		info, err := fs.Stat(Assets(), name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

// TestAdminPageNeverInjectsHTML：接口返回的任何字段都只经 textContent / createTextNode 落屏，
// 页面里不允许出现把字符串当 HTML 解析的 API。
func TestAdminPageNeverInjectsHTML(t *testing.T) {
	script := asset(t, "app.js")
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(script, forbidden) {
			t.Errorf("app.js 出现 %s：接口返回值必须只经 textContent 渲染", forbidden)
		}
	}
	if !strings.Contains(script, "textContent") {
		t.Error("app.js 没有用 textContent 渲染文本")
	}
}

// pageEntryRe 匹配 app.js 里 PAGES 表的每一行，例如
// `  { key: "overview", label: "概览", title: "服务概览", hint: "…", render: overviewView },`
var pageEntryRe = regexp.MustCompile(`(?m)^\s*\{ key: "([a-z]+)", label: "[^"]+", title: "[^"]+", hint: "[^"]+", render: ([A-Za-z0-9_]+) \},\s*$`)

// TestAdminPageEveryTabHasAView：导航项、页面注册表与视图函数三者不能各改各的。
func TestAdminPageEveryTabHasAView(t *testing.T) {
	script := asset(t, "app.js")
	entries := pageEntryRe.FindAllStringSubmatch(script, -1)
	if len(entries) < 10 {
		t.Fatalf("PAGES 只解析出 %d 项，页面注册表的写法可能已经变了", len(entries))
	}
	keys := map[string]bool{}
	for _, entry := range entries {
		if keys[entry[1]] {
			t.Errorf("页面 key 重复：%s", entry[1])
		}
		keys[entry[1]] = true
		if !strings.Contains(script, "function "+entry[2]+"(") {
			t.Errorf("页面 %s 指向不存在的视图函数 %s", entry[1], entry[2])
		}
	}
	// 令牌门禁与登录流程必须存在，否则管理员打开页面只会看到一片空白。
	for _, marker := range []string{"tokenGateView", "probeHealth", "TOKEN_KEY"} {
		if !strings.Contains(script, marker) {
			t.Errorf("app.js 缺少 %s", marker)
		}
	}
}

// TestAdminPageExplainsTokenBootstrap：管理接口被禁用时，页面必须给出可执行的排查步骤，
// 而不是只显示一个「未授权」。
func TestAdminPageExplainsTokenBootstrap(t *testing.T) {
	script := asset(t, "app.js")
	for _, marker := range []string{
		"adminTokens",
		"systemctl restart syntropy-update",
		"sessionStorage",
		"管理接口未启用",
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("app.js 缺少令牌引导文案 %q", marker)
		}
	}
}

func TestAdminPageStylesCarryTheShell(t *testing.T) {
	css := asset(t, "app.css")
	for _, marker := range []string{
		".sidebar",
		".nav",
		".tiles",
		".badge",
		"@media (prefers-color-scheme: light)",
		"@media (max-width: 880px)",
	} {
		if !strings.Contains(css, marker) {
			t.Errorf("app.css 缺少 %q", marker)
		}
	}
}
