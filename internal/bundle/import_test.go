package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"

	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
)

// memoryOpener 让灌库测试不碰磁盘：制品内容直接来自内存。
type memoryOpener map[string][]byte

func (m memoryOpener) open(path string) (io.ReadCloser, error) {
	data, ok := m[path]
	if !ok {
		return nil, fmt.Errorf("no such fixture artifact: %s", path)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func releaseKey(component, channel, version string) string {
	return component + "|" + channel + "|" + version
}

func distributionKey(channel, productVersion string) string {
	return channel + "|" + productVersion
}

// fakeService 是内存版 Service：行为对齐真实存储（同槽位重传覆盖、ErrNotFound 语义）。
type fakeService struct {
	components    map[string]model.Component
	releases      map[string]model.Release
	artifacts     map[string][]model.Artifact
	pointers      map[string]string
	distributions map[string]model.Distribution
	uploads       []string
	nextID        int
}

func newFakeService() *fakeService {
	return &fakeService{
		components:    map[string]model.Component{},
		releases:      map[string]model.Release{},
		artifacts:     map[string][]model.Artifact{},
		pointers:      map[string]string{},
		distributions: map[string]model.Distribution{},
	}
}

func (f *fakeService) EnsureComponent(component model.Component) (model.Component, error) {
	if existing, ok := f.components[component.Name]; ok {
		return existing, nil
	}
	f.components[component.Name] = component
	return component, nil
}

func (f *fakeService) FindRelease(component, channel, version string) (model.Release, error) {
	value, ok := f.releases[releaseKey(component, channel, version)]
	if !ok {
		return model.Release{}, release.ErrNotFound
	}
	return value, nil
}

func (f *fakeService) CreateRelease(value model.Release, actor string) (model.Release, error) {
	f.nextID++
	value.ID = fmt.Sprintf("release-%d", f.nextID)
	f.releases[releaseKey(value.Component, value.Channel, value.Version)] = value
	return value, nil
}

func (f *fakeService) Artifacts(releaseID string) ([]model.Artifact, error) {
	return append([]model.Artifact{}, f.artifacts[releaseID]...), nil
}

func (f *fakeService) UploadArtifact(releaseID, slot, platformOS, arch, filename, expectedSHA256 string, reader io.Reader) (model.Artifact, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return model.Artifact{}, err
	}
	digest := digestOf(data)
	if expectedSHA256 != "" && !strings.EqualFold(expectedSHA256, digest) {
		return model.Artifact{}, fmt.Errorf("upload %s: sha256 %s != expected %s", filename, digest, expectedSHA256)
	}
	artifact := model.Artifact{
		ID:         fmt.Sprintf("artifact-%d-%d", f.nextID, len(f.uploads)),
		ReleaseID:  releaseID,
		Slot:       slot,
		OS:         platformOS,
		Arch:       arch,
		Filename:   filename,
		Size:       int64(len(data)),
		SHA256:     digest,
		BlobSHA256: digest,
	}
	f.uploads = append(f.uploads, releaseID+" "+slot+" "+platformOS+"/"+arch+" "+filename)
	replaced := false
	for index, current := range f.artifacts[releaseID] {
		if current.Slot == slot && current.OS == platformOS && current.Arch == arch {
			f.artifacts[releaseID][index] = artifact
			replaced = true
		}
	}
	if !replaced {
		f.artifacts[releaseID] = append(f.artifacts[releaseID], artifact)
	}
	return artifact, nil
}

func (f *fakeService) PointChannel(component, channel, releaseID, actor string) error {
	f.pointers[component+"|"+channel] = releaseID
	return nil
}

func (f *fakeService) GetDistribution(channel, productVersion string) (model.Distribution, error) {
	value, ok := f.distributions[distributionKey(channel, productVersion)]
	if !ok {
		return model.Distribution{}, release.ErrNotFound
	}
	return value, nil
}

func (f *fakeService) PublishDistribution(value model.Distribution, actor string) (model.Distribution, error) {
	f.distributions[distributionKey(value.Channel, value.ProductVersion)] = value
	return value, nil
}

func (f *fakeService) pointerFor(component, channel string) string {
	return f.pointers[component+"|"+channel]
}

func (f *fakeService) artifactCount(releaseID string) int {
	return len(f.artifacts[releaseID])
}

// samplePlan 是一份最小的产品发布计划：产品（product）一次装 mbot 与 desktop 两件，
// 再加一个独立版本线的 codex。产品只有一份发布，成员靠槽位区分。
func samplePlan() Plan {
	return Plan{
		ProductVersion: "0.3.1",
		Components: []ComponentPlan{
			{
				Name:       ComponentProduct,
				Kind:       model.KindProduct,
				Versioning: model.VersioningProduct,
				Version:    "0.3.1",
				Artifacts: []Artifact{
					{Path: "/bundle/node/artifacts/mbot-linux-amd64", Slot: ComponentMBot, OS: "linux", Arch: "amd64", Filename: "mbot-linux-amd64", SHA256: digestOf([]byte("mbot-linux"))},
					{Path: "/bundle/node/artifacts/mbot-windows-amd64.exe", Slot: ComponentMBot, OS: "windows", Arch: "amd64", Filename: "mbot-windows-amd64.exe", SHA256: digestOf([]byte("mbot-windows"))},
					// 安装器在清单里没有摘要：灌库要现算一份。
					{Path: "/dist/SyntropySetup-0.3.1.exe", Slot: ComponentDesktop, OS: "windows", Arch: "amd64", Filename: "SyntropySetup-0.3.1.exe"},
				},
			},
			{
				Name:       ComponentCodex,
				Kind:       model.KindArchive,
				Versioning: model.VersioningIndependent,
				Version:    "0.160.0",
				Artifacts: []Artifact{
					{Path: "/bundle/node/artifacts/codex-linux-amd64.tgz", Slot: ComponentCodex, OS: "linux", Arch: "amd64", Filename: "codex-linux-amd64.tgz", SHA256: digestOf([]byte("codex-linux"))},
				},
			},
		},
	}
}

func sampleArtifacts() memoryOpener {
	return memoryOpener{
		"/bundle/node/artifacts/mbot-linux-amd64":       []byte("mbot-linux"),
		"/bundle/node/artifacts/mbot-windows-amd64.exe": []byte("mbot-windows"),
		"/bundle/node/artifacts/codex-linux-amd64.tgz":  []byte("codex-linux"),
		"/dist/SyntropySetup-0.3.1.exe":                 []byte("installer-bytes"),
	}
}

func reportFor(t *testing.T, report Report, component string) ReleaseReport {
	t.Helper()
	for _, produced := range report.Releases {
		if produced.Component == component {
			return produced
		}
	}
	t.Fatalf("%s is missing from the report", component)
	return ReleaseReport{}
}

func TestImportPublishesReleasesAndBOM(t *testing.T) {
	service := newFakeService()
	report, err := Import(service, samplePlan(), ImportOptions{
		Channel: "stable",
		Actor:   "ci",
		Open:    sampleArtifacts().open,
		Publish: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Channel != "stable" || report.ProductVersion != "0.3.1" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Releases) != 2 {
		t.Fatalf("reported %d releases, want 2", len(report.Releases))
	}

	product := reportFor(t, report, ComponentProduct)
	if !product.Created || !product.Published || len(product.Uploaded) != 3 {
		t.Errorf("product report = %+v", product)
	}
	if got := service.pointerFor(ComponentProduct, "stable"); got != product.ReleaseID {
		t.Errorf("product channel pointer = %q, want %q", got, product.ReleaseID)
	}
	if got := service.artifactCount(product.ReleaseID); got != 3 {
		t.Errorf("product stored %d artifacts, want 3", got)
	}
	// 安装器那份在清单里没有摘要：上传时的摘要必须是现算出来的那一份。
	if !strings.Contains(product.Uploaded[2], shortSHA(digestOf([]byte("installer-bytes")))) {
		t.Errorf("desktop upload = %v, want the digest computed from the file", product.Uploaded)
	}

	codex := reportFor(t, report, ComponentCodex)
	if !codex.Created || !codex.Published || len(codex.Uploaded) != 1 {
		t.Errorf("codex report = %+v", codex)
	}

	distribution, err := service.GetDistribution("stable", "0.3.1")
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	if len(distribution.Components) != 2 ||
		distribution.Components[0].Name != ComponentProduct ||
		distribution.Components[1].Name != ComponentCodex {
		t.Errorf("distribution = %+v", distribution)
	}
	if !strings.Contains(report.Distribution, "已发布") {
		t.Errorf("report.Distribution = %q", report.Distribution)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	service := newFakeService()
	plan := samplePlan()
	options := ImportOptions{Channel: "stable", Actor: "ci", Open: sampleArtifacts().open, Publish: true}
	if _, err := Import(service, plan, options); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	uploadsAfterFirst := len(service.uploads)

	second, err := Import(service, plan, options)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if len(service.uploads) != uploadsAfterFirst {
		t.Errorf("second import uploaded %d more artifacts, want 0", len(service.uploads)-uploadsAfterFirst)
	}
	for _, produced := range second.Releases {
		if produced.Created {
			t.Errorf("%s was created again", produced.Component)
		}
		if len(produced.Uploaded) != 0 {
			t.Errorf("%s uploaded %v again", produced.Component, produced.Uploaded)
		}
	}
	if !strings.Contains(second.Distribution, "已存在且一致") {
		t.Errorf("second distribution note = %q, want it to report the existing BOM", second.Distribution)
	}
}

func TestImportRefusesChangedBytes(t *testing.T) {
	t.Run("declared digest", func(t *testing.T) {
		service := newFakeService()
		options := ImportOptions{Channel: "stable", Open: sampleArtifacts().open}
		if _, err := Import(service, samplePlan(), options); err != nil {
			t.Fatalf("first Import: %v", err)
		}
		changed := samplePlan()
		changed.Components[0].Artifacts[0].SHA256 = digestOf([]byte("tampered"))
		_, err := Import(service, changed, options)
		if err == nil || !strings.Contains(err.Error(), "不可变") {
			t.Fatalf("Import error = %v, want an immutability failure", err)
		}
	})

	t.Run("digest recomputed from the file", func(t *testing.T) {
		service := newFakeService()
		options := ImportOptions{Channel: "stable", Open: sampleArtifacts().open}
		if _, err := Import(service, samplePlan(), options); err != nil {
			t.Fatalf("first Import: %v", err)
		}
		tampered := sampleArtifacts()
		tampered["/dist/SyntropySetup-0.3.1.exe"] = []byte("installer-tampered")
		options.Open = tampered.open
		_, err := Import(service, samplePlan(), options)
		if err == nil || !strings.Contains(err.Error(), "不可变") {
			t.Fatalf("Import error = %v, want an immutability failure", err)
		}
	})
}

func TestImportNoPublishLeavesPointersAndBOMAlone(t *testing.T) {
	service := newFakeService()
	report, err := Import(service, samplePlan(), ImportOptions{
		Channel: "stable",
		Open:    sampleArtifacts().open,
		Publish: false,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(service.pointers) != 0 {
		t.Errorf("channel pointers = %v, want none", service.pointers)
	}
	if len(service.distributions) != 0 {
		t.Errorf("distributions = %v, want none", service.distributions)
	}
	if !strings.Contains(report.Distribution, "未发布") {
		t.Errorf("report.Distribution = %q", report.Distribution)
	}
	for _, produced := range report.Releases {
		if produced.Published {
			t.Errorf("%s was published despite -no-publish", produced.Component)
		}
	}
	if got := service.artifactCount(reportFor(t, report, ComponentProduct).ReleaseID); got != 3 {
		t.Errorf("product stored %d artifacts, want 3 even without publishing", got)
	}
}

// conflictingService 模拟服务端对 BOM 锚点的校验：产品换了就必须升产品版本，推进会被拒。
// 规则本身只在 release.PublishDistribution 一处实现，这里只验证 Import 会把它透出来。
type conflictingService struct {
	*fakeService
}

func (c *conflictingService) PublishDistribution(value model.Distribution, actor string) (model.Distribution, error) {
	return model.Distribution{}, fmt.Errorf("distribution %s/%s already exists for a different product release; bump the product version instead", value.Channel, value.ProductVersion)
}

func TestImportSurfacesDistributionConflict(t *testing.T) {
	service := &conflictingService{fakeService: newFakeService()}
	_, err := Import(service, samplePlan(), ImportOptions{Channel: "stable", Open: sampleArtifacts().open, Publish: true})
	if err == nil || !strings.Contains(err.Error(), "bump the product version") {
		t.Fatalf("Import error = %v, want the service's BOM rejection surfaced", err)
	}
}

func TestImportSurfacesMissingArtifactFile(t *testing.T) {
	plan := samplePlan()
	plan.Components[1].Artifacts[0].Path = "/dist/missing.exe"
	plan.Components[1].Artifacts[0].Filename = "missing.exe"
	_, err := Import(newFakeService(), plan, ImportOptions{Channel: "stable", Open: sampleArtifacts().open})
	if err == nil || !strings.Contains(err.Error(), "/dist/missing.exe") {
		t.Fatalf("Import error = %v, want it to name the missing artifact", err)
	}
}

func TestImportRequiresChannel(t *testing.T) {
	if _, err := Import(newFakeService(), samplePlan(), ImportOptions{}); err == nil {
		t.Fatal("Import without a channel should fail")
	}
}

func TestSameComponentsIgnoresOrder(t *testing.T) {
	left := []model.DistributionComponent{
		{Name: ComponentProduct, Version: "0.3.1", ReleaseID: "release-1"},
		{Name: ComponentCodex, Version: "0.160.0", ReleaseID: "release-2"},
	}
	right := []model.DistributionComponent{
		{Name: ComponentCodex, Version: "0.160.0", ReleaseID: "release-2"},
		{Name: ComponentProduct, Version: "0.3.1", ReleaseID: "release-1"},
	}
	if !sameComponents(left, right) {
		t.Fatal("sameComponents should ignore the order")
	}
	if sameComponents(left, []model.DistributionComponent{
		{Name: ComponentProduct, Version: "0.3.2", ReleaseID: "release-1"},
		{Name: ComponentCodex, Version: "0.160.0", ReleaseID: "release-2"},
	}) {
		t.Fatal("sameComponents should notice a different version")
	}
	if sameComponents(left, []model.DistributionComponent{
		{Name: ComponentProduct, Version: "0.3.1", ReleaseID: "release-1"},
	}) {
		t.Fatal("sameComponents should notice a missing component")
	}
}
