package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

func newTestServer(t *testing.T) (*httptest.Server, func()) {
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
	handler := api.New(service, metadata, blobs, auth.New([]string{"admin-secret"}, nil, false, false), nil).Handler()
	server := httptest.NewServer(handler)
	return server, func() {
		server.Close()
		_ = metadata.Close()
	}
}

func doRequest(t *testing.T, client *http.Client, method, url string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response, content
}

func TestHealthAndPublicKey(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()

	response, _ := doRequest(t, client, http.MethodGet, server.URL+"/healthz", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", response.StatusCode)
	}
	response, content := doRequest(t, client, http.MethodGet, server.URL+"/v1/pubkey", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pubkey status = %d", response.StatusCode)
	}
	if _, err := sign.ParsePublicKeyPEM(content); err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}
}

func TestAdminRequiresToken(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	body, _ := json.Marshal(map[string]any{"component": "desktop", "version": "0.4.0", "channel": "beta"})
	response, _ := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", body, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestPublishUploadManifestAndBlob(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	adminHeaders := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}

	body, _ := json.Marshal(map[string]any{
		"component": "desktop",
		"version":   "0.4.0",
		"channel":   "beta",
		"publish":   true,
	})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", body, adminHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create release status = %d body = %s", response.StatusCode, content)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &created); err != nil {
		t.Fatalf("unmarshal release: %v", err)
	}

	artifactBytes := []byte("windows-installer-payload")
	sum := sha256.Sum256(artifactBytes)
	digest := hex.EncodeToString(sum[:])
	uploadHeaders := map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Filename":    "SyntropySetup.exe",
		"X-Sha256":      digest,
	}
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/windows/amd64", artifactBytes, uploadHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", response.StatusCode, content)
	}

	// 摘要不符必须被拒绝。
	response, _ = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/darwin/arm64", []byte("x"), map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Sha256":      strings.Repeat("0", 64),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched digest status = %d, want 400", response.StatusCode)
	}

	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/manifest?channel=beta", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d body = %s", response.StatusCode, content)
	}
	var envelope sign.Envelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	publicResponse, publicContent := doRequest(t, client, http.MethodGet, server.URL+"/v1/pubkey", nil, nil)
	if publicResponse.StatusCode != http.StatusOK {
		t.Fatalf("pubkey status = %d", publicResponse.StatusCode)
	}
	publicKey, err := sign.ParsePublicKeyPEM(publicContent)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}
	if _, err := sign.VerifyEnvelope(publicKey, envelope); err != nil {
		t.Fatalf("verify manifest: %v", err)
	}

	response, blobContent := doRequest(t, client, http.MethodGet, server.URL+"/v1/blobs/"+digest, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("blob status = %d", response.StatusCode)
	}
	if !bytes.Equal(blobContent, artifactBytes) {
		t.Fatalf("blob content mismatch")
	}
	if response.Header.Get("ETag") != `"`+digest+`"` {
		t.Fatalf("etag = %s", response.Header.Get("ETag"))
	}

	response, partial := doRequest(t, client, http.MethodGet, server.URL+"/v1/blobs/"+digest, nil, map[string]string{"Range": "bytes=0-6"})
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", response.StatusCode)
	}
	if string(partial) != "windows" {
		t.Fatalf("partial = %q", partial)
	}

	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/audit?limit=20", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("audit status = %d", response.StatusCode)
	}
	var audit struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(content, &audit); err != nil {
		t.Fatalf("unmarshal audit: %v", err)
	}
	if len(audit.Entries) == 0 {
		t.Fatal("expected audit entries")
	}
}

func TestManifestWithoutReleaseIsNotFound(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	response, _ := doRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/manifest?channel=stable", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestComponentManifestAndDistributionEndpoints(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	adminHeaders := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}

	createBody, _ := json.Marshal(map[string]any{"component": "mbot", "version": "1.2.0", "channel": "stable", "publish": true})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", createBody, adminHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create component release status = %d body = %s", response.StatusCode, content)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &created); err != nil {
		t.Fatalf("unmarshal release: %v", err)
	}

	binary := []byte("mbot-binary")
	sum := sha256.Sum256(binary)
	digest := hex.EncodeToString(sum[:])
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/releases/"+created.ID+"/artifacts/linux/amd64", binary, map[string]string{
		"Authorization": "Bearer admin-secret",
		"X-Filename":    "mbot-linux-amd64",
		"X-Sha256":      digest,
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", response.StatusCode, content)
	}

	publicResponse, publicContent := doRequest(t, client, http.MethodGet, server.URL+"/v1/pubkey", nil, nil)
	if publicResponse.StatusCode != http.StatusOK {
		t.Fatalf("pubkey status = %d", publicResponse.StatusCode)
	}
	publicKey, err := sign.ParsePublicKeyPEM(publicContent)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}

	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/components/mbot/manifest?channel=stable", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("component manifest status = %d body = %s", response.StatusCode, content)
	}
	var componentEnvelope sign.Envelope
	if err := json.Unmarshal(content, &componentEnvelope); err != nil {
		t.Fatalf("unmarshal component manifest: %v", err)
	}
	if _, err := sign.VerifyEnvelope(publicKey, componentEnvelope); err != nil {
		t.Fatalf("verify component manifest: %v", err)
	}

	distributionBody, _ := json.Marshal(map[string]any{
		"channel":        "stable",
		"productVersion": "1.2.0",
		"components":     []map[string]string{{"name": "mbot", "version": "1.2.0"}},
	})
	response, content = doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/distributions", distributionBody, adminHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("publish distribution status = %d body = %s", response.StatusCode, content)
	}

	for _, path := range []string{"/v1/distributions/stable/1.2.0", "/v1/distributions/latest?channel=stable"} {
		response, content = doRequest(t, client, http.MethodGet, server.URL+path, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d body = %s", path, response.StatusCode, content)
		}
		var envelope sign.Envelope
		if err := json.Unmarshal(content, &envelope); err != nil {
			t.Fatalf("unmarshal distribution %s: %v", path, err)
		}
		if _, err := sign.VerifyEnvelope(publicKey, envelope); err != nil {
			t.Fatalf("verify distribution %s: %v", path, err)
		}
	}
}

type planItemView struct {
	TargetID  string `json:"targetId"`
	Component string `json:"component"`
	Action    string `json:"action"`
	State     string `json:"state"`
	Approval  string `json:"approval"`
	From      string `json:"from"`
	To        string `json:"to"`
}

type planView struct {
	ID    string         `json:"id"`
	Items []planItemView `json:"items"`
}

func postPlan(t *testing.T, client *http.Client, url string, payload map[string]any) (int, planView) {
	t.Helper()
	body, _ := json.Marshal(payload)
	response, content := doRequest(t, client, http.MethodPost, url, body, nil)
	var plan planView
	if response.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(content, &plan); err != nil {
			t.Fatalf("unmarshal plan: %v body = %s", err, content)
		}
	}
	return response.StatusCode, plan
}

func TestPlanPolicyAndInventoryEndpoints(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	client := server.Client()
	adminHeaders := map[string]string{"Authorization": "Bearer admin-secret", "Content-Type": "application/json"}

	componentBody, _ := json.Marshal(map[string]any{"name": "mbot", "kind": "binary", "versioning": "semver"})
	response, content := doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/components", componentBody, adminHeaders)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put component status = %d body = %s", response.StatusCode, content)
	}
	releaseBody, _ := json.Marshal(map[string]any{"component": "mbot", "version": "1.2.0", "channel": "stable", "publish": true})
	response, content = doRequest(t, client, http.MethodPost, server.URL+"/v1/admin/releases", releaseBody, adminHeaders)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create release status = %d body = %s", response.StatusCode, content)
	}

	target := func(version string) []map[string]any {
		return []map[string]any{{
			"id":         "node-a",
			"kind":       "node",
			"components": map[string]string{"mbot": version},
		}}
	}

	status, plan := postPlan(t, client, server.URL+"/v1/plan", map[string]any{"channel": "stable", "targets": target("1.1.0")})
	if status != http.StatusCreated {
		t.Fatalf("plan status = %d", status)
	}
	if plan.ID == "" || len(plan.Items) != 1 {
		t.Fatalf("unexpected plan %+v", plan)
	}
	item := plan.Items[0]
	if item.Action != "upgrade" || item.State != "pending" || item.Approval != "confirm" || item.From != "1.1.0" || item.To != "1.2.0" {
		t.Fatalf("unexpected plan item %+v", item)
	}

	// 已是最新版本时不产生步骤。
	status, upToDate := postPlan(t, client, server.URL+"/v1/plan", map[string]any{"channel": "stable", "targets": target("1.2.0")})
	if status != http.StatusCreated {
		t.Fatalf("plan status = %d", status)
	}
	if len(upToDate.Items) != 0 {
		t.Fatalf("expected no items, got %+v", upToDate.Items)
	}

	// 灰度百分比为 0 时计划项进入 deferred。
	policyBody, _ := json.Marshal(map[string]any{"approval": "confirm", "percentage": 0})
	response, content = doRequest(t, client, http.MethodPut, server.URL+"/v1/admin/policies/mbot/stable", policyBody, adminHeaders)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set policy status = %d body = %s", response.StatusCode, content)
	}
	var savedPolicy struct {
		Component  string `json:"component"`
		Channel    string `json:"channel"`
		Percentage int    `json:"percentage"`
	}
	if err := json.Unmarshal(content, &savedPolicy); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if savedPolicy.Component != "mbot" || savedPolicy.Channel != "stable" || savedPolicy.Percentage != 0 {
		t.Fatalf("unexpected policy %+v", savedPolicy)
	}

	status, deferred := postPlan(t, client, server.URL+"/v1/plan", map[string]any{"channel": "stable", "targets": target("1.1.0")})
	if status != http.StatusCreated || len(deferred.Items) != 1 || deferred.Items[0].State != "deferred" {
		t.Fatalf("expected deferred plan, got status=%d plan=%+v", status, deferred)
	}

	// 未配置 token 的管理接口必须拒绝。
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/policies", nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("policies without token status = %d, want 401", response.StatusCode)
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/policies?channel=stable", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list policies status = %d body = %s", response.StatusCode, content)
	}
	var policies struct {
		Policies []struct {
			Component string `json:"component"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(content, &policies); err != nil {
		t.Fatalf("unmarshal policies: %v", err)
	}
	if len(policies.Policies) != 1 || policies.Policies[0].Component != "mbot" {
		t.Fatalf("unexpected policies %+v", policies.Policies)
	}

	// 单目标库存上报与列表。
	inventoryBody, _ := json.Marshal(map[string]any{"kind": "node", "components": map[string]string{"mbot": "1.0.0"}})
	response, content = doRequest(t, client, http.MethodPost, server.URL+"/v1/inventory/node-b", inventoryBody, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("report inventory status = %d body = %s", response.StatusCode, content)
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/inventory", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list inventory status = %d", response.StatusCode)
	}
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(content, &targets); err != nil {
		t.Fatalf("unmarshal inventory: %v", err)
	}
	ids := map[string]bool{}
	for _, entry := range targets.Targets {
		ids[entry.ID] = true
	}
	if !ids["node-a"] || !ids["node-b"] {
		t.Fatalf("unexpected inventory ids %+v", ids)
	}

	// 计划列表与详情。
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/plans?limit=10", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list plans status = %d", response.StatusCode)
	}
	var plans struct {
		Plans []planView `json:"plans"`
	}
	if err := json.Unmarshal(content, &plans); err != nil {
		t.Fatalf("unmarshal plans: %v", err)
	}
	if len(plans.Plans) == 0 {
		t.Fatal("expected plans")
	}
	response, content = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/plans/"+plan.ID, nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get plan status = %d body = %s", response.StatusCode, content)
	}
	var fetched planView
	if err := json.Unmarshal(content, &fetched); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if fetched.ID != plan.ID || len(fetched.Items) != 1 {
		t.Fatalf("unexpected fetched plan %+v", fetched)
	}
	response, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/admin/plans/plan_missing", nil, map[string]string{"Authorization": "Bearer admin-secret"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing plan status = %d, want 404", response.StatusCode)
	}

	// 空库存与未知渠道必须被拒绝。
	status, _ = postPlan(t, client, server.URL+"/v1/plan", map[string]any{"channel": "stable", "targets": []map[string]any{}})
	if status != http.StatusBadRequest {
		t.Fatalf("empty inventory status = %d, want 400", status)
	}
	status, _ = postPlan(t, client, server.URL+"/v1/plan", map[string]any{"channel": "nope", "targets": target("1.0.0")})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown channel status = %d, want 400", status)
	}
}
