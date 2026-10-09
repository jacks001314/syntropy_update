package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
)

// Service 是灌库需要的领域操作；*release.Service 天然满足它。
type Service interface {
	EnsureComponent(model.Component) (model.Component, error)
	FindRelease(component, channel, version string) (model.Release, error)
	CreateRelease(model.Release, string) (model.Release, error)
	Artifacts(releaseID string) ([]model.Artifact, error)
	UploadArtifact(releaseID, slot, platformOS, arch, filename, expectedSHA256 string, reader io.Reader) (model.Artifact, error)
	PointChannel(component, channel, releaseID, actor string) error
	GetDistribution(channel, productVersion string) (model.Distribution, error)
	PublishDistribution(model.Distribution, string) (model.Distribution, error)
}

// ImportOptions 是一次灌库的执行参数。
type ImportOptions struct {
	Channel  string
	Actor    string
	Notes    string
	NotesURL string
	// Publish 为 false 时只创建发布、上传制品，不推进渠道指针、不发 BOM。
	Publish bool
	// Open 打开制品文件；默认 os.Open。注入是为了让测试不碰真实文件系统。
	Open func(string) (io.ReadCloser, error)
}

// ReleaseReport 汇总一个组件的灌库结果。
type ReleaseReport struct {
	Component string
	Version   string
	ReleaseID string
	Created   bool
	Published bool
	Skipped   []string
	Uploaded  []string
}

// Report 汇总一次灌库。
type Report struct {
	Channel        string
	ProductVersion string
	Releases       []ReleaseReport
	Distribution   string
	Notes          []string
}

// Import 执行一次灌库。
//
// 顺序是有讲究的：先创建发布，再传制品，最后才把渠道指针指过去——指针一旦指过去，
// 客户端立刻就能拉到这份清单，此时制品必须已经在库里了。BOM 放在全部组件都就绪之后再发。
func Import(service Service, plan Plan, options ImportOptions) (Report, error) {
	channel := strings.TrimSpace(options.Channel)
	if channel == "" {
		return Report{}, errors.New("bundle: channel is required")
	}
	actor := strings.TrimSpace(options.Actor)
	if actor == "" {
		actor = "import-bundle"
	}
	open := options.Open
	if open == nil {
		open = func(path string) (io.ReadCloser, error) { return os.Open(path) }
	}

	report := Report{
		Channel:        channel,
		ProductVersion: plan.ProductVersion,
		Notes:          append([]string{}, plan.Notes...),
	}
	distribution := model.Distribution{Channel: channel, ProductVersion: plan.ProductVersion}
	for _, component := range plan.Components {
		releaseReport, err := importComponent(service, component, channel, actor, options, open)
		if err != nil {
			return Report{}, err
		}
		report.Releases = append(report.Releases, releaseReport)
		distribution.Components = append(distribution.Components, model.DistributionComponent{
			Name:      component.Name,
			Version:   component.Version,
			ReleaseID: releaseReport.ReleaseID,
		})
	}

	if !options.Publish {
		report.Distribution = "未发布（-no-publish：只创建发布与上传制品）"
		return report, nil
	}
	existing, err := service.GetDistribution(channel, plan.ProductVersion)
	if err != nil && !errors.Is(err, release.ErrNotFound) {
		return Report{}, fmt.Errorf("read distribution %s/%s: %w", channel, plan.ProductVersion, err)
	}
	if err == nil && sameComponents(existing.Components, distribution.Components) {
		report.Distribution = fmt.Sprintf("已存在且一致（%s/%s），跳过", channel, plan.ProductVersion)
		return report, nil
	}
	// 已存在但组件不同：插件版本可以推进 BOM，产品换了会被 PublishDistribution 挡回去。
	// 「产品版本是 BOM 的锚点」这条规则只在服务端一处实现，这里不复制一份。
	advanced := err == nil
	saved, publishErr := service.PublishDistribution(distribution, actor)
	if publishErr != nil {
		return Report{}, fmt.Errorf("publish distribution: %w", publishErr)
	}
	if advanced {
		report.Distribution = fmt.Sprintf("已推进 %s/%s 的 BOM（%d 个组件）", saved.Channel, saved.ProductVersion, len(saved.Components))
		return report, nil
	}
	report.Distribution = fmt.Sprintf("已发布 %s/%s（%d 个组件）", saved.Channel, saved.ProductVersion, len(saved.Components))
	return report, nil
}

func importComponent(
	service Service,
	component ComponentPlan,
	channel, actor string,
	options ImportOptions,
	open func(string) (io.ReadCloser, error),
) (ReleaseReport, error) {
	report := ReleaseReport{Component: component.Name, Version: component.Version}
	if _, err := service.EnsureComponent(model.Component{
		Name:        component.Name,
		DisplayName: component.Name,
		Kind:        component.Kind,
		Versioning:  component.Versioning,
	}); err != nil {
		return ReleaseReport{}, fmt.Errorf("ensure component %s: %w", component.Name, err)
	}

	existing, err := service.FindRelease(component.Name, channel, component.Version)
	switch {
	case err == nil:
		report.ReleaseID = existing.ID
		report.Skipped = append(report.Skipped, fmt.Sprintf("发布 %s@%s 已存在，复用", component.Name, component.Version))
	case errors.Is(err, release.ErrNotFound):
		created, createErr := service.CreateRelease(model.Release{
			Channel:   channel,
			Component: component.Name,
			Notes:     options.Notes,
			NotesURL:  options.NotesURL,
			Version:   component.Version,
		}, actor)
		if createErr != nil {
			return ReleaseReport{}, fmt.Errorf("create release %s@%s: %w", component.Name, component.Version, createErr)
		}
		report.ReleaseID = created.ID
		report.Created = true
	default:
		return ReleaseReport{}, fmt.Errorf("look up release %s@%s: %w", component.Name, component.Version, err)
	}

	registered := make(map[string]model.Artifact)
	if !report.Created {
		listed, listErr := service.Artifacts(report.ReleaseID)
		if listErr != nil {
			return ReleaseReport{}, fmt.Errorf("list artifacts of %s: %w", report.ReleaseID, listErr)
		}
		for _, artifact := range listed {
			registered[artifactKey(artifact.Slot, artifact.OS, artifact.Arch)] = artifact
		}
	}

	for _, artifact := range component.Artifacts {
		key := artifactKey(artifact.Slot, artifact.OS, artifact.Arch)
		digest := artifact.SHA256
		if digest == "" {
			// 清单没给摘要（桌面端安装器就是这种）：现算一份，既用于比对已登记的制品，
			// 也交给上传校验字节。多读一遍文件，换的是「同一版本不会悄悄换字节」。
			computed, hashErr := digestFile(open, artifact.Path)
			if hashErr != nil {
				return ReleaseReport{}, fmt.Errorf("hash artifact %s: %w", artifact.Path, hashErr)
			}
			digest = computed
		}
		if current, ok := registered[key]; ok {
			if strings.EqualFold(current.SHA256, digest) {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s %s 已登记同一份制品", component.Name, key))
				continue
			}
			return ReleaseReport{}, fmt.Errorf(
				"bundle: %s %s 已发布为 %s，本次是 %s：已发布的版本不可变，请升版本号",
				component.Name, key, shortSHA(current.SHA256), shortSHA(digest),
			)
		}
		file, openErr := open(artifact.Path)
		if openErr != nil {
			return ReleaseReport{}, fmt.Errorf("open artifact %s: %w", artifact.Path, openErr)
		}
		_, uploadErr := service.UploadArtifact(report.ReleaseID, artifact.Slot, artifact.OS, artifact.Arch, artifact.Filename, digest, file)
		closeErr := file.Close()
		if uploadErr != nil {
			return ReleaseReport{}, fmt.Errorf("upload %s %s: %w", component.Name, key, uploadErr)
		}
		if closeErr != nil {
			return ReleaseReport{}, fmt.Errorf("close artifact %s: %w", artifact.Path, closeErr)
		}
		report.Uploaded = append(report.Uploaded, fmt.Sprintf("%s %s %s", key, artifact.Filename, shortSHA(digest)))
	}

	if options.Publish {
		if err := service.PointChannel(component.Name, channel, report.ReleaseID, actor); err != nil {
			return ReleaseReport{}, fmt.Errorf("point channel %s/%s: %w", component.Name, channel, err)
		}
		report.Published = true
	}
	return report, nil
}

func digestFile(open func(string) (io.ReadCloser, error), path string) (string, error) {
	file, err := open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func shortSHA(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// sameComponents 比较两份组件清单（顺序无关）：灌库重跑必须能认出「这就是同一份 BOM」。
func sameComponents(left, right []model.DistributionComponent) bool {
	if len(left) != len(right) {
		return false
	}
	index := make(map[string]model.DistributionComponent, len(left))
	for _, component := range left {
		index[component.Name] = component
	}
	for _, component := range right {
		current, ok := index[component.Name]
		if !ok || current.Version != component.Version || current.ReleaseID != component.ReleaseID {
			return false
		}
	}
	return true
}
