package updclient_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"syntropy_update/internal/api"
	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/config"
	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
	"syntropy_update/pkg/updclient"
)

// newTestStack 起一个真实的升级服务（内存 bbolt + 临时 blob），返回客户端与制品内容。
func newTestStack(t *testing.T) (*updclient.Client, []byte) {
	t.Helper()
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

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
	cfg := config.Default()
	cfg.PublicBaseURL = server.URL
	service := release.New(cfg, metadata, blobs, keys.Private)
	handler = api.New(service, metadata, blobs, auth.New(nil, nil, false, false), nil).Handler()

	saved, err := service.PublishRelease(model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"}, "test")
	if err != nil {
		t.Fatalf("publish release: %v", err)
	}
	content := []byte("mbot-linux-amd64-binary")
	if _, err := service.UploadArtifact(saved.ID, "linux", "amd64", "mbot-linux-amd64", "", bytes.NewReader(content)); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	return &updclient.Client{
		BaseURL:    server.URL,
		Channel:    "stable",
		HTTPClient: server.Client(),
		PublicKey:  keys.Public,
	}, content
}

func TestFetchComponentManifestAndDownload(t *testing.T) {
	client, content := newTestStack(t)
	manifest, err := client.FetchComponentManifest(context.Background(), "mbot")
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	if manifest.Component != "mbot" || manifest.Version != "1.2.0" || manifest.Channel != "stable" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.ExpiresAt.IsZero() || manifest.PublishedAt.IsZero() {
		t.Fatalf("timestamps not parsed: %+v", manifest)
	}
	artifact, ok := manifest.Pick("linux", "amd64")
	if !ok {
		t.Fatalf("artifact not found: %+v", manifest.Artifacts)
	}
	if artifact.Size != int64(len(content)) {
		t.Fatalf("artifact size = %d, want %d", artifact.Size, len(content))
	}

	destination := filepath.Join(t.TempDir(), "mbot-linux-amd64")
	if err := client.Download(context.Background(), artifact, destination); err != nil {
		t.Fatalf("download: %v", err)
	}
	read, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(read, content) {
		t.Fatal("downloaded content mismatch")
	}
	if _, ok := manifest.Pick("windows", "amd64"); ok {
		t.Fatal("unexpected artifact for windows/amd64")
	}
}

func TestFetchRejectsWrongPublicKey(t *testing.T) {
	client, _ := newTestStack(t)
	other, err := sign.GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	client.PublicKey = other.Public
	if _, err := client.FetchComponentManifest(context.Background(), "mbot"); err == nil {
		t.Fatal("expected verification failure with a wrong public key")
	}
}

func TestDownloadRejectsDigestMismatch(t *testing.T) {
	client, _ := newTestStack(t)
	manifest, err := client.FetchComponentManifest(context.Background(), "mbot")
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	artifact, ok := manifest.Pick("linux", "amd64")
	if !ok {
		t.Fatal("artifact not found")
	}
	artifact.SHA256 = "0" + artifact.SHA256[1:]
	if err := client.Download(context.Background(), artifact, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestPickFallsBackToPlatformIndependentArtifact(t *testing.T) {
	t.Parallel()

	manifest := updclient.Manifest{Artifacts: []updclient.Artifact{{Filename: "templates-1.0.0.tar.gz"}}}
	artifact, ok := manifest.Pick("linux", "amd64")
	if !ok || artifact.Filename != "templates-1.0.0.tar.gz" {
		t.Fatalf("pick = %+v, ok = %v", artifact, ok)
	}
	if _, ok := manifest.Pick("windows", "arm64"); !ok {
		t.Fatal("content artifact must serve every platform")
	}

	platform := updclient.Manifest{Artifacts: []updclient.Artifact{
		{Filename: "templates-1.0.0.tar.gz"},
		{Arch: "amd64", Filename: "mbot-linux-amd64", OS: "linux"},
	}}
	got, ok := platform.Pick("linux", "amd64")
	if !ok || got.Filename != "mbot-linux-amd64" {
		t.Fatalf("platform artifact must win: %+v", got)
	}
}
