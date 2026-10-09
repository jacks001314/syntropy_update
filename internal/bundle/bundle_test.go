package bundle

import (
	"path/filepath"
	"strings"
	"testing"

	"syntropy_update/internal/model"
)

// realManifestJSON 复刻 desktop_app 构建产物 release-manifest.json 的真实形状，
// 摘要取自一次真实构建。测试盯的是解析与规划，不是这些数字本身。
const realManifestJSON = `{
  "schemaVersion": 1,
  "productVersion": "0.3.1.2026-10-05",
  "builtAtUTC": "2026-10-05T03:25:46.058Z",
  "target": { "os": "windows", "arch": "amd64", "electronPlatform": "win32", "electronArch": "x64" },
  "components": {
    "master": { "path": "bin/win32-x64/syntropy-master.exe", "size": 30515712, "sha256": "5b8ab9d7acd3b9c0593cc59c5751d1bc6d441830a438f59edb3f9607343bd719" },
    "webServer": { "path": "bin/win32-x64/syntropy-app-server.exe", "size": 28690944, "sha256": "f056014717937bc4d73f2f9accd2258108290a6d9d7e83f41ef12e0a34cd7c5e" },
    "nodeTemplate": { "path": "node/conf/nbot.json.tpl", "size": 511, "sha256": "2f96a556b12d4d2cf4c83a47ec5eb1849649996437352ad34b78c5202e05dbb0" },
    "masterTemplate": { "path": "node/conf/mbot.json.tpl", "size": 619, "sha256": "97bed91d178b93f16b6d69ad3ffca36d0c570836adad36708bb04f9c0b73f684" },
    "masterDefaults": { "path": "config/master.default.json", "size": 369, "sha256": "00929768b2a4f13911f7b7a1b2d01542071b762a6f4372aa57b6f53a94b49a26" },
    "webDefaults": { "path": "config/web.default.json", "size": 133, "sha256": "7042041b9b2f65ce28be5c5df51216164a2493988127fef592c56fdc365fef3e" }
  },
  "codex": {
    "version": "0.160.0",
    "artifacts": [
      { "os": "linux", "arch": "amd64", "path": "node/artifacts/codex-linux-amd64.tgz", "sha256": "37a41d61c3399182b8c727b77090cc7a1566bd849d0f09070a0bbc6fec4c58dc", "size": 163273896 },
      { "os": "windows", "arch": "amd64", "path": "node/artifacts/codex-windows-amd64.tgz", "sha256": "1f4c46470317bf5fabb308ed3e39a80ff544069cedc2cb477005545e694058b9", "size": 162643563 },
      { "os": "darwin", "arch": "amd64", "path": "node/artifacts/codex-darwin-amd64.tgz", "sha256": "d90ef1be135b605c88af1b7595bac768a02c7ea410688240961899655ccb5d2e", "size": 143411935 },
      { "os": "darwin", "arch": "arm64", "path": "node/artifacts/codex-darwin-arm64.tgz", "sha256": "fc789bcd655d903f92e1a23c8dc5315ba38f43b3586eafb7bd3b195970b57466", "size": 134311083 }
    ]
  },
  "nodeArtifacts": [
    { "path": "node/artifacts/nbot-linux-amd64", "size": 20828322, "sha256": "3ac0057454afa4624a1240f5627090571ffaee72a7089212528598534e8cef10" },
    { "path": "node/artifacts/nbot-linux-arm64", "size": 19660962, "sha256": "cbba5f5a4182c41ad4b98666eb008ff43a8fe375b3e26bc57ba1986dc24a6b23" },
    { "path": "node/artifacts/nbot-windows-amd64.exe", "size": 21680640, "sha256": "3cdffc183676b052d51f06e607eb37178d2b8587f64ca79e0b3659a31ed2be89" },
    { "path": "node/artifacts/nbot-darwin-amd64", "size": 21334944, "sha256": "35e1d2f19a3e93702c8b32421581050c1c615a5e44d63409e06e18065d3ba7ba" },
    { "path": "node/artifacts/nbot-darwin-arm64", "size": 20192530, "sha256": "c679f7c8962197e7354baa7164a34a6ca494f0a8314210e892d66ed7416dddff" }
  ],
  "nodeReplArtifacts": [
    { "path": "node/artifacts/node-repl-linux-amd64", "size": 15388834, "sha256": "273cc04fee9b67373bc82870255340bb3149696c982bbf8c82513df96098d179" },
    { "path": "node/artifacts/node-repl-linux-arm64", "size": 14483618, "sha256": "3a5af6ca4ca1a6a6b9eed2b4821fcfc0a6717d10bb7d605dfab0dfebc6198356" },
    { "path": "node/artifacts/node-repl-windows-amd64.exe", "size": 15922688, "sha256": "121b02c08cbbc293045ace3a224e60c37720eda9a626dc3027206ecc63d3c7b4" },
    { "path": "node/artifacts/node-repl-darwin-amd64", "size": 15658144, "sha256": "bf16adbe1db85d901a1778dfb4d18d2d194518ca0e685efadbaa1bfda12c657e" },
    { "path": "node/artifacts/node-repl-darwin-arm64", "size": 14752242, "sha256": "cb94c65fdb040d5dad60dc4605e10aa39ab4b88133102a66faee2176b69759d6" }
  ],
  "masterArtifacts": [
    { "path": "node/artifacts/mbot-linux-amd64", "size": 29786274, "sha256": "e2d369380e9f340af46aaa1a62e5bd052d48bbff2338019801220e1e0ffecba8" },
    { "path": "node/artifacts/mbot-linux-arm64", "size": 27984034, "sha256": "1e787d8fb1f8f95a2e4ee694943ca76ef4aea4d97811f724f425af62f12dd696" },
    { "path": "node/artifacts/mbot-windows-amd64.exe", "size": 30515712, "sha256": "5b8ab9d7acd3b9c0593cc59c5751d1bc6d441830a438f59edb3f9607343bd719" },
    { "path": "node/artifacts/mbot-darwin-amd64", "size": 30581856, "sha256": "b714c1b59cf4b0a20127d9d01f9a8c34bbff469f9cdad6ecb0ca7531a3b4d540" },
    { "path": "node/artifacts/mbot-darwin-arm64", "size": 28867794, "sha256": "b167a3ab3403af73bf8946d476c3589073eabeff2d336e9d665dd5ba4019ed25" }
  ]
}`

func planFromRealManifest(t *testing.T, options PlanOptions) Plan {
	t.Helper()
	manifest, err := ParseManifest([]byte(realManifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest(realManifestJSON): %v", err)
	}
	plan, err := PlanAssets(manifest, options)
	if err != nil {
		t.Fatalf("PlanAssets: %v", err)
	}
	return plan
}

func componentNames(plan Plan) []string {
	names := make([]string, 0, len(plan.Components))
	for _, component := range plan.Components {
		names = append(names, component.Name)
	}
	return names
}

func componentByName(t *testing.T, plan Plan, name string) ComponentPlan {
	t.Helper()
	for _, component := range plan.Components {
		if component.Name == name {
			return component
		}
	}
	t.Fatalf("component %q is missing from the plan (%v)", name, componentNames(plan))
	return ComponentPlan{}
}

func artifactByPlatform(t *testing.T, component ComponentPlan, platform string) Artifact {
	t.Helper()
	for _, artifact := range component.Artifacts {
		if artifact.OS+"/"+artifact.Arch == platform {
			return artifact
		}
	}
	t.Fatalf("%s has no %s artifact (%+v)", component.Name, platform, component.Artifacts)
	return Artifact{}
}

func TestParseManifestRejectsUnknownSchema(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"schemaVersion": 2}`)); err == nil {
		t.Fatal("schemaVersion 2 should be rejected")
	}
	if _, err := ParseManifest([]byte("not json")); err == nil {
		t.Fatal("malformed JSON should be rejected")
	}
	manifest, err := ParseManifest([]byte(realManifestJSON))
	if err != nil {
		t.Fatalf("real manifest should parse: %v", err)
	}
	if manifest.ProductVersion != "0.3.1.2026-10-05" || manifest.Codex.Version != "0.160.0" {
		t.Fatalf("manifest parsed into %+v", manifest)
	}
}

func TestNormalizeProductVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.3.1", "0.3.1"},
		{"0.3.1.2026-10-05", "0.3.1"},
		{"0.3.1.0", "0.3.1"},
		{"1.2.3.4", "1.2.3"},
		{" 0.10.2 ", "0.10.2"},
		{"0.3", ""},
		{"", ""},
		{"v0.3.1", ""},
		{"nightly", ""},
	}
	for _, tc := range cases {
		if got := NormalizeProductVersion(tc.in); got != tc.want {
			t.Errorf("NormalizeProductVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPlanAssetsFromRealManifest(t *testing.T) {
	installer := Artifact{
		OS:       "windows",
		Arch:     "amd64",
		Path:     `C:\dist\SyntropySetup-0.3.1.exe`,
		Filename: "SyntropySetup-0.3.1.exe",
	}
	plan := planFromRealManifest(t, PlanOptions{
		ManifestDir: `C:\bundle`,
		Desktop:     []Artifact{installer},
	})

	if plan.ProductVersion != "0.3.1" {
		t.Fatalf("product version = %q, want 0.3.1", plan.ProductVersion)
	}
	wantOrder := []string{"mbot", "app-server", "nbot", "node-repl", "codex", "desktop"}
	if got := componentNames(plan); strings.Join(got, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("component order = %v, want %v", got, wantOrder)
	}

	for _, want := range []struct {
		name       string
		kind       string
		versioning string
		version    string
	}{
		{"mbot", model.KindBinary, model.VersioningProduct, "0.3.1"},
		{"app-server", model.KindBinary, model.VersioningProduct, "0.3.1"},
		{"nbot", model.KindBinary, model.VersioningProduct, "0.3.1"},
		{"node-repl", model.KindBinary, model.VersioningIndependent, "0.3.1"},
		{"codex", model.KindArchive, model.VersioningIndependent, "0.160.0"},
		{"desktop", model.KindInstaller, model.VersioningProduct, "0.3.1"},
	} {
		component := componentByName(t, plan, want.name)
		if component.Kind != want.kind || component.Versioning != want.versioning || component.Version != want.version {
			t.Errorf("%s = kind %s / versioning %s / version %s, want %s / %s / %s",
				want.name, component.Kind, component.Versioning, component.Version,
				want.kind, want.versioning, want.version)
		}
	}

	wantArtifacts := map[string][]string{
		"mbot": {
			"darwin/amd64=mbot-darwin-amd64",
			"darwin/arm64=mbot-darwin-arm64",
			"linux/amd64=mbot-linux-amd64",
			"linux/arm64=mbot-linux-arm64",
			"windows/amd64=mbot-windows-amd64.exe",
		},
		"nbot": {
			"darwin/amd64=nbot-darwin-amd64",
			"darwin/arm64=nbot-darwin-arm64",
			"linux/amd64=nbot-linux-amd64",
			"linux/arm64=nbot-linux-arm64",
			"windows/amd64=nbot-windows-amd64.exe",
		},
		"node-repl": {
			"darwin/amd64=node-repl-darwin-amd64",
			"darwin/arm64=node-repl-darwin-arm64",
			"linux/amd64=node-repl-linux-amd64",
			"linux/arm64=node-repl-linux-arm64",
			"windows/amd64=node-repl-windows-amd64.exe",
		},
		"codex": {
			"darwin/amd64=codex-darwin-amd64.tgz",
			"darwin/arm64=codex-darwin-arm64.tgz",
			"linux/amd64=codex-linux-amd64.tgz",
			"windows/amd64=codex-windows-amd64.tgz",
		},
	}
	for name, want := range wantArtifacts {
		component := componentByName(t, plan, name)
		got := make([]string, 0, len(component.Artifacts))
		for _, artifact := range component.Artifacts {
			got = append(got, artifact.OS+"/"+artifact.Arch+"="+artifact.Filename)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s artifacts = %v, want %v", name, got, want)
		}
	}

	// mbot 的两处来源里，node/artifacts 那份才是客户端会去取的，宿主二进制只补空缺。
	mbotWindows := artifactByPlatform(t, componentByName(t, plan, "mbot"), "windows/amd64")
	if want := filepath.Join(`C:\bundle`, "node", "artifacts", "mbot-windows-amd64.exe"); mbotWindows.Path != want {
		t.Errorf("mbot windows path = %q, want %q", mbotWindows.Path, want)
	}
	if mbotWindows.SHA256 == "" {
		t.Error("mbot windows artifact lost its manifest digest")
	}

	appServer := componentByName(t, plan, "app-server")
	if len(appServer.Artifacts) != 1 {
		t.Fatalf("app-server has %d artifacts, want 1", len(appServer.Artifacts))
	}
	if got := appServer.Artifacts[0]; got.OS != "windows" || got.Arch != "amd64" || got.Filename != "syntropy-app-server.exe" {
		t.Errorf("app-server artifact = %+v", got)
	}

	desktop := componentByName(t, plan, "desktop")
	if len(desktop.Artifacts) != 1 || desktop.Artifacts[0].Filename != installer.Filename || desktop.Artifacts[0].Path != installer.Path {
		t.Errorf("desktop plan = %+v, want the passed-in installer", desktop.Artifacts)
	}

	duplicate := false
	for _, note := range plan.Notes {
		if strings.Contains(note, "mbot windows/amd64 重复") {
			duplicate = true
		}
	}
	if !duplicate {
		t.Errorf("notes should report the mbot windows/amd64 duplicate, got %v", plan.Notes)
	}
}

func TestPlanAssetsPathAndVersionOverrides(t *testing.T) {
	plan := planFromRealManifest(t, PlanOptions{
		AssetDir:          "/assets",
		ManifestDir:       "/ignored",
		ProductVersion:    "9.9.9",
		ComponentVersions: map[string]string{"node-repl": "1.4.0"},
	})

	if plan.ProductVersion != "9.9.9" {
		t.Fatalf("product version = %q, want 9.9.9", plan.ProductVersion)
	}
	if got := componentByName(t, plan, "node-repl").Version; got != "1.4.0" {
		t.Errorf("node-repl version = %q, want 1.4.0", got)
	}
	if got := componentByName(t, plan, "mbot").Version; got != "9.9.9" {
		t.Errorf("mbot version = %q, want 9.9.9", got)
	}
	if got := componentByName(t, plan, "codex").Version; got != "0.160.0" {
		t.Errorf("codex version = %q, want 0.160.0", got)
	}
	want := filepath.Join("/assets", "node", "artifacts", "mbot-linux-amd64")
	if got := artifactByPlatform(t, componentByName(t, plan, "mbot"), "linux/amd64").Path; got != want {
		t.Errorf("mbot linux path = %q, want %q", got, want)
	}
}

func TestPlanAssetsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		options PlanOptions
		want    string
	}{
		{
			name:   "unknown arch",
			mutate: func(m *Manifest) { m.NodeArtifacts[0].Path = "node/artifacts/nbot-linux-x86_64" },
			want:   "unknown os/arch",
		},
		{
			name:   "filename the desktop client cannot use",
			mutate: func(m *Manifest) { m.NodeReplArtifacts[0].Path = "node/artifacts/node_repl-linux-amd64" },
			want:   `the client expects "node-repl-linux-amd64"`,
		},
		{
			name:   "codex version is not semver",
			mutate: func(m *Manifest) { m.Codex.Version = "0.160" },
			want:   "codex version",
		},
		{
			name:   "manifest product version is unusable",
			mutate: func(m *Manifest) { m.ProductVersion = "nightly" },
			want:   "MAJOR.MINOR.PATCH",
		},
		{
			name:    "product version override is not semver",
			options: PlanOptions{ProductVersion: "1.2"},
			want:    "MAJOR.MINOR.PATCH",
		},
		{
			name:    "component version override is not semver",
			options: PlanOptions{ComponentVersions: map[string]string{"mbot": "1.2.3.4"}},
			want:    "mbot version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := ParseManifest([]byte(realManifestJSON))
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			if tc.mutate != nil {
				tc.mutate(&manifest)
			}
			options := tc.options
			if options.ManifestDir == "" && options.AssetDir == "" {
				options.ManifestDir = "/bundle"
			}
			_, err = PlanAssets(manifest, options)
			if err == nil {
				t.Fatalf("PlanAssets should fail with %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestPlanAssetsRequiresAssetRoot(t *testing.T) {
	manifest, err := ParseManifest([]byte(realManifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if _, err := PlanAssets(manifest, PlanOptions{}); err == nil {
		t.Fatal("PlanAssets without an asset directory should fail")
	}
}

func TestPlanAssetsNotesMissingDesktopInstallers(t *testing.T) {
	plan := planFromRealManifest(t, PlanOptions{ManifestDir: "/bundle"})
	for _, component := range plan.Components {
		if component.Name == ComponentDesktop {
			t.Fatal("desktop should not be planned without a desktop installer")
		}
	}
	noted := false
	for _, note := range plan.Notes {
		if strings.Contains(note, "清单里没有 desktop 的制品") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("notes should mention the missing desktop installer, got %v", plan.Notes)
	}
}

func TestPlanAssetsRejectsManifestWithoutArtifacts(t *testing.T) {
	manifest := Manifest{SchemaVersion: 1, ProductVersion: "1.2.3", Target: Target{OS: "windows", Arch: "amd64"}}
	if _, err := PlanAssets(manifest, PlanOptions{ManifestDir: "/bundle"}); err == nil {
		t.Fatal("a manifest with no artifacts should fail")
	}
}

// contentArtifactOf 取组件唯一那份平台无关制品；没有就当场失败。
func contentArtifactOf(t *testing.T, component ComponentPlan) Artifact {
	t.Helper()
	for _, artifact := range component.Artifacts {
		if artifact.OS == "" && artifact.Arch == "" {
			return artifact
		}
	}
	t.Fatalf("%s has no platform-independent artifact (%+v)", component.Name, component.Artifacts)
	return Artifact{}
}

func hasNote(notes []string, want string) bool {
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}

func TestPlanAssetsPlansPlatformIndependentContent(t *testing.T) {
	t.Parallel()

	plan := planFromRealManifest(t, PlanOptions{
		AssetDir:          "/bundle",
		ComponentVersions: map[string]string{"templates": "2.1.0"},
		Content: []ContentArtifact{
			{Component: "templates", Path: "content/templates-2.1.0.tar.gz", SHA256: strings.Repeat("a", 64)},
			{Component: "skills", Path: "content/skills-0.3.1.tar.gz"},
		},
	})

	templates := componentByName(t, plan, "templates")
	if templates.Kind != model.KindContent || templates.Versioning != model.VersioningIndependent {
		t.Fatalf("templates = %+v", templates)
	}
	if templates.Version != "2.1.0" {
		t.Fatalf("templates version = %q, want 2.1.0", templates.Version)
	}
	if len(templates.Artifacts) != 1 {
		t.Fatalf("templates artifacts = %+v", templates.Artifacts)
	}
	artifact := contentArtifactOf(t, templates)
	if artifact.Filename != "templates-2.1.0.tar.gz" {
		t.Errorf("filename = %q", artifact.Filename)
	}
	if artifact.Path != filepath.Join("/bundle", "content", "templates-2.1.0.tar.gz") {
		t.Errorf("path = %q", artifact.Path)
	}
	if artifact.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("sha256 = %q", artifact.SHA256)
	}

	// 内容组件没有随包版本来源：默认跟产品版本，并且要说明白是怎么来的。
	skills := componentByName(t, plan, "skills")
	if skills.Version != "0.3.1" {
		t.Fatalf("skills version = %q, want the product version 0.3.1", skills.Version)
	}
	if !hasNote(plan.Notes, "-component-version skills=") {
		t.Fatalf("notes = %v", plan.Notes)
	}
}

func TestPlanAssetsRejectsBadContentArtifacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content []ContentArtifact
		want    string
	}{
		{
			name:    "component is not managed",
			content: []ContentArtifact{{Component: "plugins", Path: "plugins-0.3.1.tar.gz"}},
			want:    "not a managed component",
		},
		{
			name:    "file name carries another version",
			content: []ContentArtifact{{Component: "templates", Path: "templates-9.9.9.tar.gz"}},
			want:    `the client expects "templates-0.3.1.tar.gz"`,
		},
		{
			name:    "file name is not the agreed shape",
			content: []ContentArtifact{{Component: "skills", Path: "skills-0.3.1.tgz"}},
			want:    `the client expects "skills-0.3.1.tar.gz"`,
		},
		{
			name:    "path is missing",
			content: []ContentArtifact{{Component: "templates"}},
			want:    "needs a path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := ParseManifest([]byte(realManifestJSON))
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			_, err = PlanAssets(manifest, PlanOptions{ManifestDir: "/bundle", Content: tc.content})
			if err == nil {
				t.Fatalf("PlanAssets should fail with %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
