package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"syntropy_update/internal/api"
	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/config"
	"syntropy_update/internal/release"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

// newTokenlessServer 起一个没配管理令牌的服务：管理页要靠 /healthz 认出这种状态。
func newTokenlessServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	metadata, err := store.Open(filepath.Join(dir, "update.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("open blobs: %v", err)
	}
	keys, err := sign.GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	service := release.New(config.Default(), metadata, blobs, keys.Private)
	server := httptest.NewServer(api.New(service, metadata, blobs, auth.New(nil, nil, false, false), nil).Handler())
	t.Cleanup(server.Close)
	return server
}

// TestHealthReportsAuthState：/healthz 要能告诉管理页「管理接口到底有没有开」，
// 否则运维只会看到一个无法解释的 401。
func TestHealthReportsAuthState(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	response, content := doRequest(t, server.Client(), http.MethodGet, server.URL+"/healthz", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", response.StatusCode)
	}
	var healthy struct {
		Status           string `json:"status"`
		AdminEnabled     bool   `json:"adminEnabled"`
		ClientTokensSet  bool   `json:"clientTokensSet"`
		DeviceAuthNeeded bool   `json:"deviceAuthRequired"`
		ReadAuthNeeded   bool   `json:"readAuthRequired"`
	}
	if err := json.Unmarshal(content, &healthy); err != nil {
		t.Fatalf("unmarshal healthz: %v (%s)", err, content)
	}
	if healthy.Status != "ok" || !healthy.AdminEnabled || healthy.ClientTokensSet || healthy.DeviceAuthNeeded || healthy.ReadAuthNeeded {
		t.Fatalf("healthz payload = %+v, want status=ok adminEnabled=true clientTokensSet=false deviceAuthRequired=false readAuthRequired=false", healthy)
	}

	tokenless := newTokenlessServer(t)
	_, content = doRequest(t, tokenless.Client(), http.MethodGet, tokenless.URL+"/healthz", nil, nil)
	if err := json.Unmarshal(content, &healthy); err != nil {
		t.Fatalf("unmarshal tokenless healthz: %v (%s)", err, content)
	}
	if healthy.AdminEnabled {
		t.Fatal("没有配置 adminTokens 时 adminEnabled 必须为 false")
	}
}

// TestAdminArtifactDownload：管理面按内容摘要取回制品（内容制品没有 os/arch，摘要是唯一的键）。
func TestAdminArtifactDownload(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	admin := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}

	body, _ := json.Marshal(map[string]any{"component": "desktop", "version": "0.4.0", "channel": "beta", "publish": true})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", body, admin)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create release status = %d body = %s", response.StatusCode, content)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &created); err != nil {
		t.Fatalf("unmarshal release: %v", err)
	}

	payload := []byte("windows-installer-payload")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/windows/amd64", payload, map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Filename":    "SyntropySetup.exe",
		"X-Sha256":      digest,
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", response.StatusCode, content)
	}

	// 管理面接口必须带令牌。
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/artifacts/"+digest, nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tokenless download status = %d, want 401", response.StatusCode)
	}

	response, downloaded := doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/artifacts/"+digest+"?filename=SyntropySetup.exe", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d body = %s", response.StatusCode, downloaded)
	}
	if !bytes.Equal(downloaded, payload) {
		t.Fatalf("downloaded %q, want %q", downloaded, payload)
	}
	if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, "SyntropySetup.exe") {
		t.Fatalf("Content-Disposition = %q, want the artifact filename", disposition)
	}

	// 未知摘要 404。
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/artifacts/"+strings.Repeat("0", 64), nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown digest status = %d, want 404", response.StatusCode)
	}

	// 文件名里的引号/换行不能原样进响应头。
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/artifacts/"+digest+"?filename=%22%0d%0aX-Evil:%201", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sanitized download status = %d", response.StatusCode)
	}
	if disposition := response.Header.Get("Content-Disposition"); strings.ContainsAny(disposition, "\r\n") {
		t.Fatalf("Content-Disposition 里出现了换行：%q", disposition)
	}
}

// TestAdminContentArtifactRoundTrip：平台无关制品没有 os/arch，而路由段不能为空，
// 所以上传走 artifacts/any/any 这个哨兵。管理页就按这条路径上传，这里把它钉住。
func TestAdminContentArtifactRoundTrip(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	admin := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}

	body, _ := json.Marshal(map[string]any{"name": "templates", "kind": "content", "versioning": "independent"})
	if response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/components", body, admin); response.StatusCode != http.StatusOK {
		t.Fatalf("component status = %d body = %s", response.StatusCode, content)
	}
	body, _ = json.Marshal(map[string]any{"component": "templates", "version": "1.0.0", "channel": "stable", "publish": true})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", body, admin)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create release status = %d body = %s", response.StatusCode, content)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &created); err != nil {
		t.Fatalf("unmarshal release: %v", err)
	}

	payload := []byte("fake-tarball-bytes")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/any/any", payload, map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Filename":    "templates-1.0.0.tar.gz",
		"X-Sha256":      digest,
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("content upload status = %d body = %s", response.StatusCode, content)
	}
	var artifact struct {
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(content, &artifact); err != nil {
		t.Fatalf("unmarshal artifact: %v", err)
	}
	if artifact.OS != "" || artifact.Arch != "" {
		t.Fatalf("any/any 必须落成平台无关，得到 os=%q arch=%q", artifact.OS, artifact.Arch)
	}
	if artifact.Filename != "templates-1.0.0.tar.gz" || artifact.Size != int64(len(payload)) {
		t.Fatalf("artifact = %+v", artifact)
	}

	// 内容组件的制品名必须严格等于 <组件>-<版本>.tar.gz。
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/any/any", payload, map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Filename":    "templates.tar.gz",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong content filename status = %d body = %s", response.StatusCode, content)
	}

	// 平台制品不能落在内容组件上（反向也要挡住）。
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/linux/amd64", payload, map[string]string{
		"Authorization": "Bearer admin-secret",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("platform artifact on content component status = %d body = %s", response.StatusCode, content)
	}

	response, downloaded := doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/artifacts/"+digest, nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK || !bytes.Equal(downloaded, payload) {
		t.Fatalf("content download status = %d body = %q", response.StatusCode, downloaded)
	}
}

// TestAdminUIAssetsAreServed：/admin/ 下的静态资源必须真的从内嵌 FS 取到。
// 挂在 /admin/ 却不剥前缀时，FileServer 在资源 FS 里找不到 admin/app.css，会把它当 SPA
// 深链回落成 index.html——状态码仍是 200，浏览器却只剩一个无样式、无脚本的壳。
// 本地 DOM 冒烟从仓库读 assets，看不出这个差别，所以要在真实路由上按 Content-Type 钉死。
func TestAdminUIAssetsAreServed(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()

	cases := []struct {
		path        string
		contentType string
		marker      string
	}{
		{"/admin/", "text/html", "class=\"shell\""},
		{"/admin/index.html", "text/html", "class=\"shell\""},
		{"/admin/app.css", "text/css", ".shell"},
		{"/admin/app.js", "javascript", "/v1/admin/"},
		{"/admin/plans", "text/html", "class=\"shell\""}, // 深链回落
	}
	for _, tc := range cases {
		response, content := doRequest(t, client, http.MethodGet, server.URL+tc.path, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", tc.path, response.StatusCode)
			continue
		}
		if got := response.Header.Get("Content-Type"); !strings.Contains(got, tc.contentType) {
			t.Errorf("%s Content-Type = %q, want 含 %q", tc.path, got, tc.contentType)
		}
		if !strings.Contains(string(content), tc.marker) {
			t.Errorf("%s 内容里没有 %q（是不是被 SPA 回落成 index.html 了）", tc.path, tc.marker)
		}
		if got := response.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", tc.path, got)
		}
	}
}
