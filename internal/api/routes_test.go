package api

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"syntropy_update/internal/webui"
)

// apiEntryRe 匹配 app.js 里 API 表的每一行，例如 `  components: "GET /v1/admin/components",`。
var apiEntryRe = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+):\s*"([A-Z]+) (/[^"]*)",\s*$`)

// TestAdminPageRoutesMatchServer 保证管理页引用的接口与服务端注册的路由一致：
// 页面指向不存在的接口、或后台加了接口忘了接页面，都会在这里失败。
func TestAdminPageRoutesMatchServer(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	adminOnly := map[string]bool{}
	for _, entry := range (&Server{}).routes() {
		registered[entry.pattern] = true
		if entry.scope == scopeAdmin {
			adminOnly[entry.pattern] = true
		}
	}

	page := map[string]string{}
	for _, match := range apiEntryRe.FindAllStringSubmatch(readAsset(t, "app.js"), -1) {
		page[match[2]+" "+match[3]] = match[1]
	}
	if len(page) == 0 {
		t.Fatal("管理页的 API 表为空：app.js 的解析规则可能已经失效")
	}

	for pattern, name := range page {
		if !registered[pattern] {
			t.Errorf("管理页引用了未注册的接口：%s（API.%s）", pattern, name)
		}
		delete(adminOnly, pattern)
	}
	for pattern := range adminOnly {
		t.Errorf("管理接口没有出现在管理页上：%s", pattern)
	}
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	content, err := fs.ReadFile(webui.Assets(), name)
	if err != nil {
		t.Fatalf("读取内嵌资源 %s: %v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}
