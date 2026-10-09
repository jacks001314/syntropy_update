// Package bundle 把一次构建产出的「发布包」灌入升级服务。
//
// 输入是桌面构建（desktop_app/tools/build.mjs）产出的 release-manifest.json 与它所在的
// 资源目录，加上本次要发的桌面端安装器；输出是领域动作：为每个组件创建发布、上传各
// (os,arch) 制品、推进渠道指针，最后发布这份产品版本的 BOM。
//
// 分成两层是有意的：
//   - Plan 是纯函数，只把「构建产物」翻译成「要发什么」，可以脱离存储单独测试；
//   - Import 只依赖一个窄接口（*release.Service 天然满足），测试里换成假实现。
//
// 灌库是幂等的：同一份输入重跑只会跳过已存在的发布与制品；同样的
// (组件, 渠道, 版本) 却换了制品字节则直接报错——已发布的版本不可变，悄悄替换会让
// 客户端手里那份签名清单变成一个谎。
package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
)

// 组件名与桌面端（apps/desktop/src/main/updates/component-layout.ts）以及 mdeploy 的
// 制品命名保持一致：两边各起一个名字，结果就是「服务发了、客户端找不到」。
const (
	ComponentMBot      = "mbot"
	ComponentAppServer = "app-server"
	ComponentNBot      = "nbot"
	ComponentNodeRepl  = "node-repl"
	ComponentCodex     = "codex"
	ComponentTemplates = "templates"
	ComponentSkills    = "skills"
	ComponentDesktop   = "desktop"
)

// FileRef 是清单里的一份产物：相对资源根目录的路径 + 大小 + 摘要。
type FileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Target 是这次构建的目标平台（仓库命名：windows/darwin/linux + amd64/arm64）。
type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Components 是清单里逐个描述的宿主组件。
type Components struct {
	Master    FileRef `json:"master"`
	WebServer FileRef `json:"webServer"`
}

// CodexArtifact 是 Codex CLI 的归档制品；Codex 有自己的版本线，不随产品版本走。
type CodexArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Codex 记录随包的 Codex CLI 版本与各平台归档。
type Codex struct {
	Version   string          `json:"version"`
	Artifacts []CodexArtifact `json:"artifacts"`
}

// Manifest 是 release-manifest.json 里本包用到的部分（其余字段一律忽略）。
type Manifest struct {
	SchemaVersion     int        `json:"schemaVersion"`
	ProductVersion    string     `json:"productVersion"`
	Target            Target     `json:"target"`
	Components        Components `json:"components"`
	Codex             Codex      `json:"codex"`
	NodeArtifacts     []FileRef  `json:"nodeArtifacts"`
	NodeReplArtifacts []FileRef  `json:"nodeReplArtifacts"`
	MasterArtifacts   []FileRef  `json:"masterArtifacts"`
}

// ParseManifest 解析 release-manifest.json。schemaVersion 认不出来时直接拒绝：
// 按旧规则灌进去一份半懂不懂的发布，比当场失败危险得多。
func ParseManifest(encoded []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse release manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported release manifest schemaVersion %d", manifest.SchemaVersion)
	}
	return manifest, nil
}

// Artifact 是计划里的一份待上传制品。
type Artifact struct {
	// Path 是制品在本机的路径（资源根 + 清单里的相对路径）。
	Path string
	OS   string
	Arch string
	// Filename 是清单登记、客户端按它落盘的下载文件名。
	Filename string
	// SHA256 是清单声明的摘要；为空表示清单没给（桌面端安装器），执行时现算。
	SHA256 string
}

// ComponentPlan 是一个组件在这次灌库里的发布计划。
type ComponentPlan struct {
	Name       string
	Kind       string
	Versioning string
	Version    string
	Artifacts  []Artifact
}

// Plan 是一次灌库的完整计划。
type Plan struct {
	ProductVersion string
	Components     []ComponentPlan
	// Notes 记录被跳过、去重或降级处理的项，由调用方如实打印，不静默吞掉。
	Notes []string
}

// PlanOptions 是规划时的可选输入。
type PlanOptions struct {
	// AssetDir 是资源根目录（清单里的相对路径以它为根）；为空时用 ManifestDir。
	AssetDir string
	// ManifestDir 是清单所在目录，AssetDir 为空时的兜底。
	ManifestDir string
	// ProductVersion 覆盖产品版本；为空则从清单的 productVersion 归一。
	ProductVersion string
	// ComponentVersions 是按组件的版本覆盖（独立版本线的组件用）。
	ComponentVersions map[string]string
	// Desktop 是本次发布的桌面端安装器。
	Desktop []Artifact
	// Content 是本次要发的内容制品（templates / skills）：与 (os,arch) 无关的目录归档。
	// 版本只能来自 ComponentVersions 或产品版本，制品文件名必须是 <组件>-<版本>.tar.gz。
	Content []ContentArtifact
}

// ContentArtifact 是一份待上传的内容制品；Path 相对资源根时为相对路径，绝对路径原样使用。
type ContentArtifact struct {
	Component string
	Path      string
	SHA256    string
}

// componentDef 是组件的固定属性：灌库与客户端必须对同名的东西有同一种理解。
type componentDef struct {
	name       string
	kind       string
	versioning string
}

// componentDefs 的先后顺序就是 BOM 里的组件顺序，固定下来便于比对与复现。
var componentDefs = []componentDef{
	{ComponentMBot, model.KindBinary, model.VersioningProduct},
	{ComponentAppServer, model.KindBinary, model.VersioningProduct},
	{ComponentNBot, model.KindBinary, model.VersioningProduct},
	{ComponentNodeRepl, model.KindBinary, model.VersioningIndependent},
	{ComponentCodex, model.KindArchive, model.VersioningIndependent},
	{ComponentTemplates, model.KindContent, model.VersioningIndependent},
	{ComponentSkills, model.KindContent, model.VersioningIndependent},
	{ComponentDesktop, model.KindInstaller, model.VersioningProduct},
}

// NormalizeProductVersion 把构建清单里的产品版本归一到 MAJOR.MINOR.PATCH。
//
// 桌面构建的清单里写的是带构建日期的版本（`0.3.1.2026-10-05`），而服务的产品版本、
// 渠道 BOM 与客户端比较都按三段式来；这里只做「取前三段」这一件事，并且要求前三段
// 本身合法，免得把一个畸形版本悄悄截断成看起来正常的东西。
func NormalizeProductVersion(value string) string {
	trimmed := strings.TrimSpace(value)
	if release.ValidVersion(trimmed) {
		return trimmed
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) > 3 {
		candidate := strings.Join(parts[:3], ".")
		if release.ValidVersion(candidate) {
			return candidate
		}
	}
	return ""
}

// PlanAssets 把构建产物翻译成发布计划。纯函数，不碰文件系统也不碰存储。
func PlanAssets(manifest Manifest, options PlanOptions) (Plan, error) {
	root := strings.TrimSpace(options.AssetDir)
	if root == "" {
		root = strings.TrimSpace(options.ManifestDir)
	}
	if root == "" {
		return Plan{}, errors.New("bundle: asset directory is required")
	}
	productVersion := strings.TrimSpace(options.ProductVersion)
	if productVersion == "" {
		productVersion = NormalizeProductVersion(manifest.ProductVersion)
	}
	if !release.ValidVersion(productVersion) {
		return Plan{}, fmt.Errorf(
			"bundle: product version %q must be MAJOR.MINOR.PATCH (release manifest says %q; pass -version)",
			productVersion, manifest.ProductVersion,
		)
	}

	notes := make([]string, 0, 4)
	// 版本先算：内容制品的文件名里带着版本，校验顺序不能反过来。
	versions := make(map[string]string, len(componentDefs))
	definitions := make(map[string]componentDef, len(componentDefs))
	for _, definition := range componentDefs {
		version, err := componentVersion(definition, manifest, productVersion, options.ComponentVersions, &notes)
		if err != nil {
			return Plan{}, err
		}
		versions[definition.name] = version
		definitions[definition.name] = definition
	}
	slots := make(map[string]map[string]Artifact)
	// put 登记一份制品。校验口径由组件类型决定：平台组件必须给出已知的 (os,arch)，
	// 内容组件必须与平台无关；文件名一律要等于客户端认的那一个——两边各起一个名字，
	// 结果就是「服务发了、客户端找不到」。
	put := func(component string, artifact Artifact, origin string) error {
		definition, known := definitions[component]
		if !known {
			return fmt.Errorf("bundle: %s: not a managed component (%s)", component, origin)
		}
		if definition.kind == model.KindContent {
			if artifact.OS != "" || artifact.Arch != "" {
				return fmt.Errorf(
					"bundle: %s: content artifact %s must not carry an os/arch, got %s/%s",
					component, origin, artifact.OS, artifact.Arch,
				)
			}
		} else {
			if artifact.OS == "" || artifact.Arch == "" {
				return fmt.Errorf("bundle: %s: cannot tell the os/arch of %s", component, origin)
			}
			if !knownOS[artifact.OS] || !knownArch[artifact.Arch] {
				return fmt.Errorf(
					"bundle: %s: unknown os/arch %s/%s for %s (use windows/darwin/linux with amd64/arm64)",
					component, artifact.OS, artifact.Arch, origin,
				)
			}
		}
		if expected := expectedArtifactName(component, artifact.OS, artifact.Arch, versions[component]); expected != "" && artifact.Filename != expected {
			return fmt.Errorf(
				"bundle: %s %s artifact is named %q, the client expects %q",
				component, artifactKey(artifact.OS, artifact.Arch), artifact.Filename, expected,
			)
		}
		if slots[component] == nil {
			slots[component] = make(map[string]Artifact)
		}
		key := artifactKey(artifact.OS, artifact.Arch)
		if existing, taken := slots[component][key]; taken {
			reason := "清单里给了两份内容相同的制品"
			if !strings.EqualFold(existing.SHA256, artifact.SHA256) {
				reason = "清单里给了两份不同的制品"
			}
			notes = append(notes, fmt.Sprintf(
				"%s %s 重复：%s 与 %s（%s），保留先出现的一份", component, key, path.Base(artifact.Path), path.Base(existing.Path), reason,
			))
			return nil
		}
		slots[component][key] = artifact
		return nil
	}
	fromRef := func(file FileRef) Artifact {
		return Artifact{Path: join(root, file.Path), Filename: path.Base(file.Path), SHA256: file.SHA256}
	}

	// mbot 有两处来源：宿主 bin/ 下的 syntropy-master（随安装包分发）与 node/artifacts 下的
	// mbot-<os>-<arch>（Master 部署节点时按名字取）。先登记 mbot-<os>-<arch> 那一批——它是
	// 本地 Master 部署与节点重下发真正会去取的那一份；宿主那份只补空缺。
	for _, file := range manifest.MasterArtifacts {
		artifact, err := artifactFromName(ComponentMBot, file.Path)
		if err != nil {
			return Plan{}, err
		}
		artifact.Path = join(root, file.Path)
		artifact.SHA256 = file.SHA256
		if err := put(ComponentMBot, artifact, file.Path); err != nil {
			return Plan{}, err
		}
	}
	if master := manifest.Components.Master; master.Path != "" {
		artifact := fromRef(master)
		artifact.OS, artifact.Arch = manifest.Target.OS, manifest.Target.Arch
		// 随包的宿主二进制叫 syntropy-master，而客户端按 mbot-<os>-<arch> 取件、并按
		// 这个名字落盘；清单里的 filename 是给客户端用的落盘名，所以按约定名发布。
		if canonical := expectedArtifactName(ComponentMBot, artifact.OS, artifact.Arch, versions[ComponentMBot]); canonical != "" {
			artifact.Filename = canonical
		}
		if err := put(ComponentMBot, artifact, master.Path); err != nil {
			return Plan{}, err
		}
	}
	if webServer := manifest.Components.WebServer; webServer.Path != "" {
		artifact := fromRef(webServer)
		artifact.OS, artifact.Arch = manifest.Target.OS, manifest.Target.Arch
		if err := put(ComponentAppServer, artifact, webServer.Path); err != nil {
			return Plan{}, err
		}
	}
	for _, file := range manifest.NodeArtifacts {
		artifact, err := artifactFromName(ComponentNBot, file.Path)
		if err != nil {
			return Plan{}, err
		}
		artifact.Path = join(root, file.Path)
		artifact.SHA256 = file.SHA256
		if err := put(ComponentNBot, artifact, file.Path); err != nil {
			return Plan{}, err
		}
	}
	for _, file := range manifest.NodeReplArtifacts {
		artifact, err := artifactFromName(ComponentNodeRepl, file.Path)
		if err != nil {
			return Plan{}, err
		}
		artifact.Path = join(root, file.Path)
		artifact.SHA256 = file.SHA256
		if err := put(ComponentNodeRepl, artifact, file.Path); err != nil {
			return Plan{}, err
		}
	}
	for _, file := range manifest.Codex.Artifacts {
		artifact := Artifact{
			Arch:     file.Arch,
			Filename: path.Base(file.Path),
			OS:       file.OS,
			Path:     join(root, file.Path),
			SHA256:   file.SHA256,
		}
		if err := put(ComponentCodex, artifact, file.Path); err != nil {
			return Plan{}, err
		}
	}
	for _, artifact := range options.Desktop {
		if err := put(ComponentDesktop, artifact, artifact.Path); err != nil {
			return Plan{}, err
		}
	}
	// 内容制品（templates / skills）是与 (os,arch) 无关的目录归档，只能由调用方显式给出：
	// 随包清单里没有它们的位置，CI 用 -content 指明，文件名自带版本。
	for _, artifact := range options.Content {
		file := strings.TrimSpace(artifact.Path)
		if file == "" {
			return Plan{}, fmt.Errorf("bundle: content artifact for %s needs a path", artifact.Component)
		}
		if !filepath.IsAbs(file) {
			file = join(root, file)
		}
		entry := Artifact{Filename: filepath.Base(artifact.Path), Path: file, SHA256: artifact.SHA256}
		if err := put(strings.TrimSpace(artifact.Component), entry, artifact.Path); err != nil {
			return Plan{}, err
		}
	}

	plan := Plan{ProductVersion: productVersion}
	for _, definition := range componentDefs {
		found := slots[definition.name]
		if len(found) == 0 {
			notes = append(notes, fmt.Sprintf("清单里没有 %s 的制品，这一轮不发这个组件", definition.name))
			continue
		}
		component := ComponentPlan{
			Name:       definition.name,
			Kind:       definition.kind,
			Versioning: definition.versioning,
			Version:    versions[definition.name],
			Artifacts:  sortedArtifacts(found),
		}
		plan.Components = append(plan.Components, component)
	}
	if len(plan.Components) == 0 {
		return Plan{}, errors.New("bundle: release manifest produced no components to publish")
	}
	plan.Notes = notes
	return plan, nil
}

func componentVersion(
	definition componentDef,
	manifest Manifest,
	productVersion string,
	overrides map[string]string,
	notes *[]string,
) (string, error) {
	if override := strings.TrimSpace(overrides[definition.name]); override != "" {
		if !release.ValidVersion(override) {
			return "", fmt.Errorf("bundle: %s version %q must be MAJOR.MINOR.PATCH", definition.name, override)
		}
		return override, nil
	}
	if definition.kind == model.KindContent {
		// 内容组件没有随包版本来源：默认跟产品版本走，CI 想单独发版就用 -component-version 覆盖。
		*notes = append(*notes, fmt.Sprintf(
			"%s 没有独立版本来源，按产品版本 %s 发布（用 -component-version %s=<版本> 覆盖）",
			definition.name, productVersion, definition.name,
		))
		return productVersion, nil
	}
	if definition.name == ComponentCodex {
		if codex := strings.TrimSpace(manifest.Codex.Version); codex != "" {
			if !release.ValidVersion(codex) {
				return "", fmt.Errorf("bundle: release manifest records codex version %q, want MAJOR.MINOR.PATCH (override with -component-version codex=<version>)", codex)
			}
			return codex, nil
		}
		*notes = append(*notes, "清单没记录 codex 版本，按产品版本发布")
	}
	return productVersion, nil
}

// sortedArtifacts 按 os/arch 排序，让同一份输入永远产出同样的顺序（便于比对与复现）。
func sortedArtifacts(found map[string]Artifact) []Artifact {
	keys := make([]string, 0, len(found))
	for key := range found {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	artifacts := make([]Artifact, 0, len(keys))
	for _, key := range keys {
		artifacts = append(artifacts, found[key])
	}
	return artifacts
}

// artifactFromName 从制品文件名反解出 (os, arch)：命名是 `<组件>-<os>-<arch>[-.exe]`，
// 组件名本身可能带连字符（node-repl），所以一律从右边数第二、一段。
func artifactFromName(component, filePath string) (Artifact, error) {
	name := path.Base(filePath)
	trimmed := strings.TrimSuffix(name, ".exe")
	parts := strings.Split(trimmed, "-")
	if len(parts) < 3 {
		return Artifact{}, fmt.Errorf("bundle: %s artifact name %q is not <component>-<os>-<arch>", component, name)
	}
	os, arch := parts[len(parts)-2], parts[len(parts)-1]
	if !knownOS[os] || !knownArch[arch] {
		return Artifact{}, fmt.Errorf("bundle: %s artifact name %q has an unknown os/arch", component, name)
	}
	return Artifact{Arch: arch, Filename: name, OS: os}, nil
}

// expectedArtifactName 复刻客户端（component-layout.ts）的命名约定；未知组件返回空串，
// 由调用方决定要不要当真。内容组件的文件名里带着版本，所以版本必须一起传进来。
func expectedArtifactName(component, os, arch, version string) string {
	if contentComponent(component) {
		return fmt.Sprintf("%s-%s.tar.gz", component, version)
	}
	suffix := ""
	if os == "windows" {
		suffix = ".exe"
	}
	switch component {
	case ComponentAppServer:
		return "syntropy-app-server" + suffix
	case ComponentCodex:
		return fmt.Sprintf("codex-%s-%s.tgz", os, arch)
	case ComponentMBot, ComponentNBot, ComponentNodeRepl:
		return fmt.Sprintf("%s-%s-%s%s", component, os, arch, suffix)
	default:
		return ""
	}
}

// contentComponent 判断组件是不是内容制品：与 (os,arch) 无关的目录归档。
func contentComponent(component string) bool {
	return component == ComponentTemplates || component == ComponentSkills
}

// artifactKey 是同一组件下制品的去重键；内容制品只有一个，用 content 当键。
func artifactKey(platformOS, arch string) string {
	if platformOS == "" && arch == "" {
		return "content"
	}
	return platformOS + "/" + arch
}

var knownOS = map[string]bool{"windows": true, "darwin": true, "linux": true}
var knownArch = map[string]bool{"amd64": true, "arm64": true}

// join 拼接资源根与清单里的相对路径；清单里的路径一律用 '/' 分隔。
func join(root, relative string) string {
	if relative == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}
