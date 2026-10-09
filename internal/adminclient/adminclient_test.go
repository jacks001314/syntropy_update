package adminclient_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"syntropy_update/internal/adminclient"
	"syntropy_update/internal/api"
	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/bundle"
	"syntropy_update/internal/config"
	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

const adminToken = "admin-secret"

// releaseManifestJSON 是一份最小但形状完整的 release-manifest.json（windows/amd64，
// 五个产品槽位各一件 + codex + node-runtime），用来走「构建清单 → 计划 → 远端灌库」全程。
// 摘要一律留空：Import 会现算，服务端再校验一遍字节，测试盯的是链路而不是那些数字。
const releaseManifestJSON = `{
  "schemaVersion": 1,
  "productVersion": "0.4.3",
  "target": { "os": "windows", "arch": "amd64" },
  "components": {
    "master": { "path": "bin/win32-x64/syntropy-master.exe", "size": 7, "sha256": "" },
    "webServer": { "path": "bin/win32-x64/syntropy-app-server.exe", "size": 10, "sha256": "" }
  },
  "codex": {
    "version": "0.160.0",
    "artifacts": [
      { "os": "windows", "arch": "amd64", "path": "node/artifacts/codex-windows-amd64.tgz", "sha256": "", "size": 5 }
    ]
  },
  "nodeRuntime": {
    "version": "24.14.0",
    "source": "https://nodejs.org/dist/v24.14.0",
    "artifacts": [
      { "path": "node/artifacts/node-runtime-windows-amd64.exe", "size": 12, "sha256": "" }
    ]
  },
  "nodeArtifacts": [
    { "path": "node/artifacts/nbot-windows-amd64.exe", "size": 4, "sha256": "" }
  ],
  "nodeReplArtifacts": [
    { "path": "node/artifacts/node-repl-windows-amd64.exe", "size": 9, "sha256": "" }
  ],
  "masterArtifacts": []
}`

// newServer 起一个真实的管理面服务：领域服务 + 存储 + blob + 签名密钥，与生产同一套代码。
func newServer(t *testing.T) (*httptest.Server, ed25519.PublicKey, func()) {
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
	cfg.PublicBaseURL = "https://update.test"
	service := release.New(cfg, metadata, blobs, keys.Private)
	handler := api.New(service, metadata, blobs, auth.New([]string{adminToken}, nil, false, false), nil).Handler()
	server := httptest.NewServer(handler)
	return server, keys.Public, func() {
		server.Close()
		_ = metadata.Close()
	}
}

func writeArtifact(t *testing.T, root, relative, content string) string {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
	return target
}

// planFromBundle 用真实构建清单形状的材料跑一遍 bundle.PlanAssets；材料放在磁盘上，
// 因为 Import 要真的读它们、现算 sha256 再上传。
func planFromBundle(t *testing.T) (bundle.Plan, string) {
	t.Helper()
	root := t.TempDir()
	writeArtifact(t, root, "bin/win32-x64/syntropy-master.exe", "master!")
	writeArtifact(t, root, "bin/win32-x64/syntropy-app-server.exe", "appserver!")
	writeArtifact(t, root, "node/artifacts/codex-windows-amd64.tgz", "codex")
	writeArtifact(t, root, "node/artifacts/node-runtime-windows-amd64.exe", "noderuntime!")
	writeArtifact(t, root, "node/artifacts/nbot-windows-amd64.exe", "nbot")
	writeArtifact(t, root, "node/artifacts/node-repl-windows-amd64.exe", "noderepl!")
	writeArtifact(t, root, "content/templates-1.2.0.tar.gz", "templates")
	writeArtifact(t, root, "content/skills-1.0.0.tar.gz", "skills")
	installer := writeArtifact(t, root, "dist/installers/windows-amd64/SyntropySetup.exe", "installer")

	manifest, err := bundle.ParseManifest([]byte(releaseManifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	plan, err := bundle.PlanAssets(manifest, bundle.PlanOptions{
		ComponentVersions: map[string]string{"skills": "1.0.0", "templates": "1.2.0"},
		Content: []bundle.ContentArtifact{
			{Component: "templates", Path: "content/templates-1.2.0.tar.gz"},
			{Component: "skills", Path: "content/skills-1.0.0.tar.gz"},
		},
		Desktop: []bundle.Artifact{{
			Arch:     "amd64",
			Filename: "SyntropySetup.exe",
			OS:       "windows",
			Path:     installer,
		}},
		ManifestDir: root,
	})
	if err != nil {
		t.Fatalf("PlanAssets: %v", err)
	}
	return plan, root
}

func newClient(t *testing.T, baseURL string) *adminclient.Client {
	t.Helper()
	client, err := adminclient.New(baseURL, adminclient.Options{Actor: "release-publish", Token: adminToken})
	if err != nil {
		t.Fatalf("adminclient.New: %v", err)
	}
	return client
}

// TestImportBundleOverAdminAPI 是「本地发布」与「CI 发布」共用那条路径的回归测试：
// 构建清单 → bundle.PlanAssets → bundle.Import →（HTTP 管理面）→ 服务端落库 → 签名 BOM 可验。
func TestImportBundleOverAdminAPI(t *testing.T) {
	plan, _ := planFromBundle(t)
	server, public, cleanup := newServer(t)
	defer cleanup()
	client := newClient(t, server.URL)

	report, err := bundle.Import(client, plan, bundle.ImportOptions{
		Actor:   "release-publish",
		Channel: "stable",
		Publish: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Releases) != 5 {
		t.Fatalf("expected 5 managed components, got %d (%+v)", len(report.Releases), report.Releases)
	}
	product := report.Releases[0]
	if product.Component != "product" {
		t.Fatalf("first release should be the product component, got %q", product.Component)
	}
	if len(product.Uploaded) != 5 {
		t.Fatalf("product should carry 5 slots, got %d (%v)", len(product.Uploaded), product.Uploaded)
	}
	if !strings.Contains(report.Distribution, "已发布") {
		t.Fatalf("BOM should have been published, got %q", report.Distribution)
	}
	for _, expected := range []string{"product", "codex", "node-runtime", "templates", "skills"} {
		if _, err := client.FindRelease(expected, "stable", releaseVersionOf(t, plan, expected)); err != nil {
			t.Fatalf("release %s is not on the service: %v", expected, err)
		}
	}

	// 再灌一次：同一份输入必须只跳过，不新建、不重传。
	again, err := bundle.Import(client, plan, bundle.ImportOptions{Channel: "stable", Publish: true})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	for _, entry := range again.Releases {
		if entry.Created || len(entry.Uploaded) != 0 {
			t.Fatalf("%s was republished on a second run: %+v", entry.Component, entry)
		}
	}
	if !strings.Contains(again.Distribution, "已存在且一致") {
		t.Fatalf("second run should reuse the BOM, got %q", again.Distribution)
	}

	// 公开面：桌面端就是这样验签并选件的。
	payload := fetchDistributionPayload(t, server.URL, public)
	byName := map[string]distributionComponent{}
	for _, component := range payload.Components {
		byName[component.Name] = component
	}
	if len(byName) != 5 {
		t.Fatalf("signed BOM should carry 5 components, got %d", len(byName))
	}
	if byName["product"].Version != "0.4.3" || byName["codex"].Version != "0.160.0" || byName["node-runtime"].Version != "24.14.0" {
		t.Fatalf("versions drifted: %+v", byName)
	}
	slots := make(map[string]string, len(byName["product"].Artifacts))
	for _, artifact := range byName["product"].Artifacts {
		slots[artifact.Slot] = artifact.Filename
	}
	wantSlots := map[string]string{
		"app-server": "syntropy-app-server.exe",
		"desktop":    "SyntropySetup.exe",
		"mbot":       "mbot-windows-amd64.exe",
		"nbot":       "nbot-windows-amd64.exe",
		"node-repl":  "node-repl-windows-amd64.exe",
	}
	for slot, filename := range wantSlots {
		if slots[slot] != filename {
			t.Fatalf("product slot %s = %q, want %q (got %v)", slot, slots[slot], filename, slots)
		}
	}
	templates := byName["templates"].Artifacts
	if len(templates) != 1 || templates[0].Slot != "templates" || templates[0].OS != "" || templates[0].Arch != "" {
		t.Fatalf("templates should be one platform-independent artifact, got %+v", templates)
	}
	if templates[0].Filename != "templates-1.2.0.tar.gz" {
		t.Fatalf("templates filename = %q", templates[0].Filename)
	}
	skills := byName["skills"].Artifacts
	if len(skills) != 1 || skills[0].Slot != "skills" || skills[0].Filename != "skills-1.0.0.tar.gz" {
		t.Fatalf("skills should be one platform-independent artifact, got %+v", skills)
	}
}

// TestAdminClientMapsServerErrors 把服务端错误码映射回领域哨兵值，否则调用方只能去认
// HTTP 状态码——那正是本地模式与远端模式开始分叉的地方。
func TestAdminClientMapsServerErrors(t *testing.T) {
	server, _, cleanup := newServer(t)
	defer cleanup()
	client := newClient(t, server.URL)

	if _, err := client.GetDistribution("stable", "9.9.9"); !errors.Is(err, release.ErrNotFound) {
		t.Fatalf("missing distribution should be ErrNotFound, got %v", err)
	}
	if _, err := client.FindRelease("product", "stable", "9.9.9"); !errors.Is(err, release.ErrNotFound) {
		t.Fatalf("missing release should be ErrNotFound, got %v", err)
	}

	created, err := client.CreateRelease(model.Release{Channel: "stable", Component: "product", Version: "0.9.9"}, "release-publish")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	_, err = client.UploadArtifact(created.ID, "no-such-slot", "windows", "amd64", "", "", strings.NewReader("x"))
	if !errors.Is(err, release.ErrValidation) {
		t.Fatalf("bad slot should be ErrValidation, got %v", err)
	}

	unauthorized, err := adminclient.New(server.URL, adminclient.Options{Token: "wrong"})
	if err != nil {
		t.Fatalf("adminclient.New: %v", err)
	}
	_, err = unauthorized.FindRelease("product", "stable", "0.4.3")
	var apiErr *adminclient.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("wrong token should surface the HTTP status, got %v", err)
	}
}

type distributionArtifact struct {
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
	OS       string `json:"os"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Slot     string `json:"slot"`
	URL      string `json:"url"`
}

type distributionComponent struct {
	Artifacts []distributionArtifact `json:"artifacts"`
	Kind      string                 `json:"kind"`
	Name      string                 `json:"name"`
	Version   string                 `json:"version"`
}

type distributionPayload struct {
	Channel        string                  `json:"channel"`
	Components     []distributionComponent `json:"components"`
	ProductVersion string                  `json:"productVersion"`
}

func fetchDistributionPayload(t *testing.T, baseURL string, public ed25519.PublicKey) distributionPayload {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/distributions/latest?channel=stable")
	if err != nil {
		t.Fatalf("fetch distribution: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read distribution: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("distribution status = %d (%s)", response.StatusCode, body)
	}
	var envelope sign.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if _, err := sign.VerifyEnvelope(public, envelope); err != nil {
		t.Fatalf("verify distribution signature: %v", err)
	}
	var payload distributionPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ProductVersion != "0.4.3" {
		t.Fatalf("product version = %q", payload.ProductVersion)
	}
	return payload
}

func releaseVersionOf(t *testing.T, plan bundle.Plan, component string) string {
	t.Helper()
	for _, entry := range plan.Components {
		if entry.Name == component {
			return entry.Version
		}
	}
	t.Fatalf("component %s is missing from the plan", component)
	return ""
}
