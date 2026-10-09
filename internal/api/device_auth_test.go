package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"syntropy_update/internal/api"
	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/config"
	"syntropy_update/internal/release"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

// newDeviceAuthServer 起一个可以调客户端令牌策略的测试服务；其它用例的 newTestServer
// 固定「只配管理令牌」，这里需要按用例换令牌，所以单独搭一台。
func newDeviceAuthServer(t *testing.T, clientTokens []string, requireRead, requireDevice bool) (*httptest.Server, func()) {
	t.Helper()
	dir := t.TempDir()
	metadata, err := store.Open(filepath.Join(dir, "update.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("open blobs: %v", err)
	}
	keys, err := sign.GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	cfg := config.Default()
	cfg.PublicBaseURL = "https://app.syntropy-lab.com"
	service := release.New(cfg, metadata, blobs, keys.Private)
	handler := api.New(service, metadata, blobs, auth.New([]string{"admin-secret"}, clientTokens, requireRead, requireDevice), nil).Handler()
	server := httptest.NewServer(handler)
	return server, func() {
		server.Close()
		_ = metadata.Close()
	}
}

func planBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"channel": "stable",
		"targets": []map[string]any{
			{"id": "node-a", "kind": "node", "components": map[string]string{"mbot": "1.1.0"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal plan body: %v", err)
	}
	return body
}

func statusOf(t *testing.T, server *httptest.Server, method, path string, body []byte, headers map[string]string) int {
	t.Helper()
	response, _ := doRequest(t, server.Client(), method, server.URL+path, body, headers)
	return response.StatusCode
}

// TestDeviceEndpointsAnonymousWhenNotRequired：没配客户端令牌、也没开
// requireDeviceTokens 时保持老行为——上报类接口接受匿名写入，老部署升级二进制后不会断。
func TestDeviceEndpointsAnonymousWhenNotRequired(t *testing.T) {
	server, cleanup := newDeviceAuthServer(t, nil, false, false)
	defer cleanup()

	response, content := doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/plan", planBody(t),
		map[string]string{"Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("anonymous plan status = %d body = %s", response.StatusCode, content)
	}
	if got := statusOf(t, server, http.MethodPost, "/v1/targets/node-a/results",
		[]byte(`{"component":"mbot","state":"installed","from":"1.1.0","to":"1.2.0"}`),
		map[string]string{"Content-Type": "application/json"}); got != http.StatusCreated {
		t.Fatalf("anonymous result status = %d, want 201", got)
	}
}

// TestDeviceEndpointsRejectAnonymousWhenRequired：开了 requireDeviceTokens 又没配令牌时
// 一律 401——「没配令牌」不等于「谁都能写」；读取类接口仍要能匿名拉，thin 安装器首启靠它。
func TestDeviceEndpointsRejectAnonymousWhenRequired(t *testing.T) {
	server, cleanup := newDeviceAuthServer(t, nil, false, true)
	defer cleanup()

	if got := statusOf(t, server, http.MethodPost, "/v1/plan", planBody(t), nil); got != http.StatusUnauthorized {
		t.Fatalf("plan status = %d, want 401", got)
	}
	if got := statusOf(t, server, http.MethodPost, "/v1/targets/node-a/results",
		[]byte(`{"component":"mbot","state":"installed"}`), nil); got != http.StatusUnauthorized {
		t.Fatalf("result status = %d, want 401", got)
	}
	if got := statusOf(t, server, http.MethodPost, "/v1/inventory/node-a",
		[]byte(`{"id":"node-a","kind":"node","components":{}}`), nil); got != http.StatusUnauthorized {
		t.Fatalf("inventory status = %d, want 401", got)
	}
	// 读取类接口不受影响：thin 安装器首启时还没有任何令牌，这条请求必须走到 handler。
	// 这里没有发布过任何东西，handler 回 404 是对的；关键是它没有在鉴权层被拦成 401。
	if got := statusOf(t, server, http.MethodGet, "/v1/manifest?channel=stable", nil, nil); got == http.StatusUnauthorized {
		t.Fatal("manifest must stay readable anonymously so a thin installer can bootstrap")
	}

	// /healthz 明确告诉运维上报接口是要求令牌的。
	_, health := doRequest(t, server.Client(), http.MethodGet, server.URL+"/healthz", nil, nil)
	var payload struct {
		DeviceAuthRequired bool `json:"deviceAuthRequired"`
		ReadAuthRequired   bool `json:"readAuthRequired"`
	}
	if err := json.Unmarshal(health, &payload); err != nil {
		t.Fatalf("unmarshal healthz: %v body = %s", err, health)
	}
	if !payload.DeviceAuthRequired {
		t.Fatalf("healthz deviceAuthRequired = false, want true (body = %s)", health)
	}
	if payload.ReadAuthRequired {
		t.Fatalf("healthz readAuthRequired = true, want false (body = %s)", health)
	}
}

// TestReadEndpointsStayAnonymousUntilRequired：读取类的默认值是「公开」——thin 安装器首启
// 时本机还没有任何令牌；打开 requireReadTokens 才收紧，且那时没配令牌就是谁都进不来。
func TestReadEndpointsStayAnonymousUntilRequired(t *testing.T) {
	// 配了客户端令牌但不要求读取鉴权：匿名读取仍然放行（与老部署升级后的行为一致）。
	configured, cleanupConfigured := newDeviceAuthServer(t, []string{"cli-secret"}, false, false)
	defer cleanupConfigured()
	if got := statusOf(t, configured, http.MethodGet, "/v1/manifest?channel=stable", nil, nil); got == http.StatusUnauthorized {
		t.Fatal("a configured client token must not close the read endpoints by itself")
	}

	// 打开 requireReadTokens：匿名读取 401，带令牌才放行。
	strict, cleanupStrict := newDeviceAuthServer(t, []string{"cli-secret"}, true, true)
	defer cleanupStrict()
	if got := statusOf(t, strict, http.MethodGet, "/v1/manifest?channel=stable", nil, nil); got != http.StatusUnauthorized {
		t.Fatalf("anonymous read status = %d, want 401", got)
	}
	if got := statusOf(t, strict, http.MethodGet, "/v1/manifest?channel=stable", nil,
		map[string]string{"Authorization": "Bearer cli-secret"}); got == http.StatusUnauthorized {
		t.Fatal("a valid token must open the read endpoints when requireReadTokens is on")
	}
}

// TestDeviceEndpointsRequireMatchingToken：配了客户端令牌就必须带对：匿名与错令牌都
// 401，带对了才放行。
func TestDeviceEndpointsRequireMatchingToken(t *testing.T) {
	server, cleanup := newDeviceAuthServer(t, []string{"cli-secret"}, false, false)
	defer cleanup()

	if got := statusOf(t, server, http.MethodPost, "/v1/plan", planBody(t), nil); got != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", got)
	}
	wrong := map[string]string{"Authorization": "Bearer nope"}
	if got := statusOf(t, server, http.MethodPost, "/v1/plan", planBody(t), wrong); got != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", got)
	}
	right := map[string]string{"Authorization": "Bearer cli-secret"}
	if got := statusOf(t, server, http.MethodPost, "/v1/plan", planBody(t), right); got != http.StatusCreated {
		t.Fatalf("right-token status = %d, want 201", got)
	}
	// X-Api-Key 是同一条提取路径，也应当被接受。
	if got := statusOf(t, server, http.MethodPost, "/v1/plan", planBody(t), map[string]string{"X-Api-Key": "cli-secret"}); got != http.StatusCreated {
		t.Fatalf("x-api-key status = %d, want 201", got)
	}
}
