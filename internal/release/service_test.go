package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"syntropy_update/internal/blob"
	"syntropy_update/internal/config"
	"syntropy_update/internal/model"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

type fixture struct {
	service *Service
	store   *store.Store
	blobs   *blob.Store
	public  ed25519.PublicKey
}

func newFixture(t *testing.T) *fixture {
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
	cfg := config.Default()
	cfg.PublicBaseURL = "https://app.syntropy-lab.com"
	cfg.ManifestTTLHours = 168
	return &fixture{
		service: New(cfg, metadata, blobs, keys.Private),
		store:   metadata,
		blobs:   blobs,
		public:  keys.Public,
	}
}

func (f *fixture) publish(t *testing.T, release model.Release) model.Release {
	t.Helper()
	saved, err := f.service.PublishRelease(release, "test")
	if err != nil {
		t.Fatalf("publish release: %v", err)
	}
	return saved
}

// publishDistribution 发布一份渠道基线：计划引擎按它决定「整条渠道上有什么」，桌面端目标正是
// 按这份基线展开的。
func (f *fixture) publishDistribution(t *testing.T, channel string, names ...string) {
	t.Helper()
	components := make([]model.DistributionComponent, 0, len(names))
	for _, name := range names {
		components = append(components, model.DistributionComponent{Name: name, Version: "1.2.0"})
	}
	if _, err := f.service.PublishDistribution(model.Distribution{
		Channel:        channel,
		ProductVersion: "1.2.0",
		Components:     components,
	}, "test"); err != nil {
		t.Fatalf("publish distribution: %v", err)
	}
}

func (f *fixture) upload(t *testing.T, releaseID, platformOS, arch string, content []byte) model.Artifact {
	t.Helper()
	artifact, err := f.service.UploadArtifact(releaseID, platformOS, arch, platformOS+"-"+arch+".bin", "", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	return artifact
}

func TestPublishUploadAndManifest(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "0.4.0", Channel: "beta"})
	windows := fixture.upload(t, saved.ID, "windows", "amd64", []byte("windows-installer"))
	darwin := fixture.upload(t, saved.ID, "darwin", "arm64", []byte("darwin-installer"))
	linux := fixture.upload(t, saved.ID, "linux", "amd64", []byte("linux-package"))

	encoded, err := fixture.service.Manifest(DesktopComponent, "beta")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	canonical, payload := verifyManifest(t, fixture.public, encoded)

	if payload["channel"] != "beta" || payload["version"] != "0.4.0" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload["minimumSupportedVersion"] != "0.0.0" {
		t.Fatalf("minimumSupportedVersion = %v", payload["minimumSupportedVersion"])
	}
	if payload["killSwitch"] != nil {
		t.Fatalf("killSwitch = %v, want nil", payload["killSwitch"])
	}
	if !strings.Contains(canonical, `"platform":"win32"`) || !strings.Contains(canonical, `"arch":"x64"`) {
		t.Fatalf("expected win32/x64 mapping in %s", canonical)
	}
	// linux 与 darwin/win32 并列：客户端 PlatformSchema 是 darwin|linux|win32，
	// 少映射一个平台会让那个平台的自更新清单直接报错。
	if !strings.Contains(canonical, `"platform":"linux"`) {
		t.Fatalf("expected linux/x64 mapping in %s", canonical)
	}

	artifacts, ok := payload["artifacts"].([]any)
	if !ok || len(artifacts) != 3 {
		t.Fatalf("artifacts = %v, want 3 entries", payload["artifacts"])
	}
	digests := map[string]bool{windows.SHA256: true, darwin.SHA256: true, linux.SHA256: true}
	for _, entry := range artifacts {
		item := entry.(map[string]any)
		if !strings.HasPrefix(item["url"].(string), "https://app.syntropy-lab.com/v1/blobs/") {
			t.Fatalf("unexpected artifact url %v", item["url"])
		}
		digest := strings.TrimPrefix(item["url"].(string), "https://app.syntropy-lab.com/v1/blobs/")
		if !digests[digest] {
			t.Fatalf("unexpected artifact digest %s", digest)
		}
	}
}

// TestManifestCoversEveryDesktopPlatform 钉住三平台映射：客户端 PlatformSchema 是
// darwin|linux|win32，任何一侧少一个平台都会让那个平台的自更新清单直接报错。
func TestManifestCoversEveryDesktopPlatform(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "0.5.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "windows", "amd64", []byte("windows-installer"))
	fixture.upload(t, saved.ID, "darwin", "arm64", []byte("darwin-installer"))
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("linux-package"))

	encoded, err := fixture.service.Manifest(DesktopComponent, "stable")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	_, payload := verifyManifest(t, fixture.public, encoded)
	artifacts, ok := payload["artifacts"].([]any)
	if !ok || len(artifacts) != 3 {
		t.Fatalf("artifacts = %v, want 3 entries", payload["artifacts"])
	}
	got := map[string]string{}
	for _, entry := range artifacts {
		item := entry.(map[string]any)
		platform, _ := item["platform"].(string)
		arch, _ := item["arch"].(string)
		got[platform] = arch
	}
	want := map[string]string{"win32": "x64", "darwin": "arm64", "linux": "x64"}
	for platform, arch := range want {
		if got[platform] != arch {
			t.Fatalf("platform %s arch = %q, want %q (all: %v)", platform, got[platform], arch, got)
		}
	}
}

func TestManifestPayloadHasExactlySchemaKeys(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "1.0.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "darwin", "amd64", []byte("payload"))
	encoded, err := fixture.service.Manifest("", "")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var envelope sign.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"artifacts", "channel", "expiresAt", "killSwitch", "minimumSupportedVersion", "publishedAt", "releaseNotesUrl", "version"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("payload keys = %v, want %v", keys, want)
	}
}

func TestManifestIncludesKillSwitch(t *testing.T) {
	fixture := newFixture(t)
	fixture.publish(t, model.Release{
		Component:               DesktopComponent,
		Version:                 "0.5.0",
		Channel:                 "stable",
		MinimumSupportedVersion: "0.3.0",
		KillSwitch:              &model.KillSwitch{ExpiresAt: "2099-01-01T00:00:00.000Z", MinimumSafeVersion: "0.3.1"},
	})
	saved, err := fixture.store.FindRelease(DesktopComponent, "stable", "0.5.0")
	if err != nil {
		t.Fatalf("find release: %v", err)
	}
	fixture.upload(t, saved.ID, "windows", "amd64", []byte("installer"))
	encoded, err := fixture.service.Manifest("", "stable")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	_, payload := verifyManifest(t, fixture.public, encoded)
	killSwitch, ok := payload["killSwitch"].(map[string]any)
	if !ok {
		t.Fatalf("killSwitch = %v", payload["killSwitch"])
	}
	if killSwitch["minimumSafeVersion"] != "0.3.1" {
		t.Fatalf("killSwitch = %+v", killSwitch)
	}
	if payload["minimumSupportedVersion"] != "0.3.0" {
		t.Fatalf("minimumSupportedVersion = %v", payload["minimumSupportedVersion"])
	}
}

func TestPublishValidation(t *testing.T) {
	fixture := newFixture(t)
	cases := []struct {
		name    string
		release model.Release
	}{
		{"bad version", model.Release{Component: DesktopComponent, Version: "0.4", Channel: "beta"}},
		{"unknown channel", model.Release{Component: DesktopComponent, Version: "0.4.0", Channel: "nightly"}},
		{"missing component", model.Release{Version: "0.4.0", Channel: "beta"}},
		{"bad kill switch", model.Release{Component: DesktopComponent, Version: "0.4.0", Channel: "beta", KillSwitch: &model.KillSwitch{ExpiresAt: "not-a-time", MinimumSafeVersion: "0.3.1"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := fixture.service.PublishRelease(testCase.release, "test"); !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
	if _, err := fixture.service.PublishRelease(model.Release{Component: DesktopComponent, Version: "0.4.0", Channel: "beta"}, "test"); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := fixture.service.PublishRelease(model.Release{Component: DesktopComponent, Version: "0.4.0", Channel: "beta"}, "test"); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected duplicate to be rejected, got %v", err)
	}
}

func TestManifestWithoutReleaseAndAfterYank(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.Manifest(DesktopComponent, "stable"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for empty channel, got %v", err)
	}
	saved := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "0.6.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "windows", "amd64", []byte("installer"))
	if _, err := fixture.service.Manifest("", "stable"); err != nil {
		t.Fatalf("manifest before yank: %v", err)
	}
	if _, err := fixture.service.YankRelease(saved.ID, "test"); err != nil {
		t.Fatalf("yank: %v", err)
	}
	if _, err := fixture.service.Manifest("", "stable"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after yank, got %v", err)
	}
}

func TestUploadArtifactVerifiesExpectedDigest(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "0.7.0", Channel: "alpha"})
	if _, err := fixture.service.UploadArtifact(saved.ID, "windows", "amd64", "a.exe", strings.Repeat("0", 64), bytes.NewReader([]byte("x"))); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func verifyManifest(t *testing.T, public ed25519.PublicKey, encoded []byte) (string, map[string]any) {
	t.Helper()
	var envelope sign.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	canonical, err := sign.VerifyEnvelope(public, envelope)
	if err != nil {
		t.Fatalf("verify envelope: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return canonical, payload
}

// ensureContentComponent 预登记一个内容组件：内容制品的平台无关口径挂在组件类型上，
// 灌库（bundle）与 CLI 都会先 EnsureComponent，测试里也要走同一条路。
func assureContentComponent(t *testing.T, f *fixture, name string) {
	t.Helper()
	if _, err := f.service.EnsureComponent(model.Component{
		Name:       name,
		Kind:       model.KindContent,
		Versioning: model.VersioningIndependent,
	}); err != nil {
		t.Fatalf("ensure component %s: %v", name, err)
	}
}

func TestUploadContentArtifactIsPlatformIndependent(t *testing.T) {
	fixture := newFixture(t)
	assureContentComponent(t, fixture, "templates")
	saved := fixture.publish(t, model.Release{Component: "templates", Version: "1.2.0", Channel: "stable"})

	artifact, err := fixture.service.UploadArtifact(saved.ID, "", "", "", "", bytes.NewReader([]byte("tarball")))
	if err != nil {
		t.Fatalf("upload content artifact: %v", err)
	}
	if artifact.OS != "" || artifact.Arch != "" {
		t.Fatalf("content artifact must not carry an os/arch: %+v", artifact)
	}
	if artifact.Filename != "templates-1.2.0.tar.gz" {
		t.Fatalf("filename = %q, want templates-1.2.0.tar.gz", artifact.Filename)
	}

	// 内容制品进 BOM 时 os/arch 为空，桌面端据此回落到平台无关的那一件。
	if _, err := fixture.service.PublishDistribution(model.Distribution{
		Channel:        "stable",
		ProductVersion: "0.3.1",
		Components:     []model.DistributionComponent{{Name: "templates", Version: "1.2.0"}},
	}, "test"); err != nil {
		t.Fatalf("publish distribution: %v", err)
	}
	encoded, err := fixture.service.SignedDistribution("stable", "0.3.1")
	if err != nil {
		t.Fatalf("signed distribution: %v", err)
	}
	_, payload := verifyManifest(t, fixture.public, encoded)
	components, ok := payload["components"].([]any)
	if !ok || len(components) != 1 {
		t.Fatalf("components = %#v", payload["components"])
	}
	component, ok := components[0].(map[string]any)
	if !ok || component["kind"] != model.KindContent {
		t.Fatalf("component = %#v", components[0])
	}
	artifacts, ok := component["artifacts"].([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v", component["artifacts"])
	}
	entry, ok := artifacts[0].(map[string]any)
	if !ok {
		t.Fatalf("artifact = %#v", artifacts[0])
	}
	if entry["os"] != "" || entry["arch"] != "" || entry["filename"] != "templates-1.2.0.tar.gz" {
		t.Fatalf("content artifact in BOM = %#v", entry)
	}
	if !strings.HasPrefix(entry["url"].(string), "https://app.syntropy-lab.com/v1/blobs/") {
		t.Fatalf("artifact url = %v", entry["url"])
	}
}

func TestUploadContentArtifactRejectsInconsistentRequest(t *testing.T) {
	fixture := newFixture(t)
	assureContentComponent(t, fixture, "skills")
	content := fixture.publish(t, model.Release{Component: "skills", Version: "1.0.0", Channel: "stable"})
	binary := fixture.publish(t, model.Release{Component: DesktopComponent, Version: "0.8.0", Channel: "stable"})

	cases := []struct {
		name     string
		release  string
		os       string
		arch     string
		filename string
	}{
		{name: "half a platform", release: content.ID, os: "windows", filename: "skills-1.0.0.tar.gz"},
		{name: "content file name carries another version", release: content.ID, filename: "skills-2.0.0.tar.gz"},
		{name: "platform artifact for a content component", release: content.ID, os: "windows", arch: "amd64", filename: "skills-1.0.0.tar.gz"},
		{name: "platform-independent artifact for a binary component", release: binary.ID, filename: "desktop-0.8.0.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fixture.service.UploadArtifact(tc.release, tc.os, tc.arch, tc.filename, "", bytes.NewReader([]byte("x")))
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
		})
	}
}
