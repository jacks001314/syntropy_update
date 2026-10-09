// Package release 提供升级服务的领域操作：发布、制品入库与签名清单。
package release

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"syntropy_update/internal/blob"
	"syntropy_update/internal/config"
	"syntropy_update/internal/model"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

// DesktopComponent 是桌面应用本体的组件名。
const DesktopComponent = "desktop"

var versionPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

// ErrValidation 表示调用方输入不合法。
var ErrValidation = errors.New("release: validation failed")

// ErrNotFound 表示要查的记录不存在（发布、分发定义）。灌库这类「先查再写」的
// 调用方需要一个稳定的哨兵值来区分「没有」与「出错」，不要让它们去认存储层。
var ErrNotFound = store.ErrNotFound

// ValidVersion 判断字符串是否是 MAJOR.MINOR.PATCH 版本号。
func ValidVersion(value string) bool { return versionPattern.MatchString(value) }

// FindRelease 按 (组件, 渠道, 版本) 查发布记录；不存在时返回 ErrNotFound。
func (s *Service) FindRelease(component, channel, version string) (model.Release, error) {
	release, err := s.store.FindRelease(strings.TrimSpace(component), strings.TrimSpace(channel), strings.TrimSpace(version))
	if errors.Is(err, store.ErrNotFound) {
		return model.Release{}, ErrNotFound
	}
	return release, err
}

// Artifacts 列出某次发布已登记的制品。
func (s *Service) Artifacts(releaseID string) ([]model.Artifact, error) {
	return s.store.ListArtifacts(strings.TrimSpace(releaseID))
}

// Service 组合配置、存储、blob 与签名私钥。
type Service struct {
	cfg        config.Config
	store      *store.Store
	blobs      *blob.Store
	privateKey ed25519.PrivateKey
}

// New 构造领域服务。privateKey 为 nil 时清单接口不可用（仍可发布与入库）。
func New(cfg config.Config, store *store.Store, blobs *blob.Store, privateKey ed25519.PrivateKey) *Service {
	return &Service{cfg: cfg, store: store, blobs: blobs, privateKey: privateKey}
}

// Config 返回配置。
func (s *Service) Config() config.Config { return s.cfg }

// EnsureComponent 在组件不存在时创建默认定义。
func (s *Service) EnsureComponent(component model.Component) (model.Component, error) {
	if strings.TrimSpace(component.Name) == "" {
		return model.Component{}, fmt.Errorf("%w: component name is required", ErrValidation)
	}
	if component.Kind == "" {
		component.Kind = model.KindBinary
	}
	if component.Versioning == "" {
		component.Versioning = model.VersioningProduct
	}
	existing, err := s.store.GetComponent(component.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.Component{}, err
	}
	now := model.NowMS()
	component.CreatedAtMS = now
	component.UpdatedAtMS = now
	if err := s.store.PutComponent(component); err != nil {
		return model.Component{}, err
	}
	return component, nil
}

// CreateRelease 创建一次发布记录，但不移动渠道指针。
func (s *Service) CreateRelease(release model.Release, actor string) (model.Release, error) {
	release.Component = strings.TrimSpace(release.Component)
	if release.Component == "" {
		return model.Release{}, fmt.Errorf("%w: component is required", ErrValidation)
	}
	if !versionPattern.MatchString(release.Version) {
		return model.Release{}, fmt.Errorf("%w: version %q must be MAJOR.MINOR.PATCH", ErrValidation, release.Version)
	}
	if !s.cfg.IsChannel(release.Channel) {
		return model.Release{}, fmt.Errorf("%w: unknown channel %q", ErrValidation, release.Channel)
	}
	if _, err := s.EnsureComponent(model.Component{Name: release.Component}); err != nil {
		return model.Release{}, err
	}
	if _, err := s.store.FindRelease(release.Component, release.Channel, release.Version); err == nil {
		return model.Release{}, fmt.Errorf("%w: release %s/%s/%s already exists", ErrValidation, release.Component, release.Channel, release.Version)
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Release{}, err
	}
	if release.KillSwitch != nil {
		if _, err := parseTimestamp(release.KillSwitch.ExpiresAt); err != nil {
			return model.Release{}, fmt.Errorf("%w: killSwitch.expiresAt: %v", ErrValidation, err)
		}
		if !versionPattern.MatchString(release.KillSwitch.MinimumSafeVersion) {
			return model.Release{}, fmt.Errorf("%w: killSwitch.minimumSafeVersion must be MAJOR.MINOR.PATCH", ErrValidation)
		}
	}
	if release.MinimumSupportedVersion != "" && !versionPattern.MatchString(release.MinimumSupportedVersion) {
		return model.Release{}, fmt.Errorf("%w: minimumSupportedVersion must be MAJOR.MINOR.PATCH", ErrValidation)
	}
	now := model.NowMS()
	release.ID = model.NewID("release")
	release.CreatedAtMS = now
	release.PublishedAtMS = now
	if err := s.store.PutRelease(release); err != nil {
		return model.Release{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "release.create",
		Actor:   actor,
		Subject: release.Component + "/" + release.Channel + "/" + release.Version,
		Detail:  release.ID,
	}); err != nil {
		return model.Release{}, err
	}
	return release, nil
}

// PublishRelease 创建一次发布并把渠道指针指向它。
func (s *Service) PublishRelease(release model.Release, actor string) (model.Release, error) {
	created, err := s.CreateRelease(release, actor)
	if err != nil {
		return model.Release{}, err
	}
	if err := s.pointChannel(created.Component, created.Channel, created.ID, actor); err != nil {
		return model.Release{}, err
	}
	return created, nil
}

// Release 回滚渠道指针到指定发布。
func (s *Service) PointChannel(component, channel, releaseID, actor string) error {
	release, err := s.store.GetRelease(releaseID)
	if err != nil {
		return err
	}
	if release.Component != component || release.Channel != channel {
		return fmt.Errorf("%w: release %s is not %s/%s", ErrValidation, releaseID, component, channel)
	}
	return s.pointChannel(component, channel, releaseID, actor)
}

func (s *Service) pointChannel(component, channel, releaseID, actor string) error {
	pointer := model.ChannelPointer{
		Component:   component,
		Channel:     channel,
		ReleaseID:   releaseID,
		UpdatedAtMS: model.NowMS(),
	}
	if err := s.store.SetChannelPointer(pointer); err != nil {
		return err
	}
	return s.store.AppendAudit(model.AuditEntry{
		Action:  "channel.point",
		Actor:   actor,
		Subject: component + "/" + channel,
		Detail:  releaseID,
	})
}

// YankRelease 标记发布为撤回；若渠道正指向它则同时摘除指针。
func (s *Service) YankRelease(releaseID, actor string) (model.Release, error) {
	release, err := s.store.GetRelease(releaseID)
	if err != nil {
		return model.Release{}, err
	}
	release.Yanked = true
	if err := s.store.PutRelease(release); err != nil {
		return model.Release{}, err
	}
	pointer, err := s.store.GetChannelPointer(release.Component, release.Channel)
	if err == nil && pointer.ReleaseID == releaseID {
		_ = s.store.SetChannelPointer(model.ChannelPointer{
			Component:   release.Component,
			Channel:     release.Channel,
			ReleaseID:   "",
			UpdatedAtMS: model.NowMS(),
		})
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "release.yank",
		Actor:   actor,
		Subject: releaseID,
	}); err != nil {
		return model.Release{}, err
	}
	return release, nil
}

// ContentArtifactFileName 是内容制品的文件名约定：<组件>-<版本>.tar.gz，与 (os,arch) 无关。
func ContentArtifactFileName(component, version string) string {
	return fmt.Sprintf("%s-%s.tar.gz", component, version)
}

// componentIsContent 判断组件是不是内容制品；组件定义查不到时按 false 处理。
func (s *Service) componentIsContent(component string) bool {
	definition, err := s.store.GetComponent(component)
	return err == nil && definition.Kind == model.KindContent
}

// UploadArtifact 把制品流写入 blob 仓库并登记元数据。expectedSHA256 非空时校验一致性。
//
// 平台组件必须给出 os 与 arch；内容组件（templates / skills）必须两个都为空，且文件名要等于
// <组件>-<版本>.tar.gz——客户端就按这个名字认件，服务端放行别的名字，等于发了一份装了也没人认
// 的制品。
func (s *Service) UploadArtifact(releaseID, platformOS, arch, filename string, expectedSHA256 string, reader io.Reader) (model.Artifact, error) {
	release, err := s.store.GetRelease(releaseID)
	if err != nil {
		return model.Artifact{}, err
	}
	platformOS = strings.TrimSpace(platformOS)
	arch = strings.TrimSpace(arch)
	filename = strings.TrimSpace(filename)
	if (platformOS == "") != (arch == "") {
		return model.Artifact{}, fmt.Errorf("%w: os and arch must be given together", ErrValidation)
	}
	content := platformOS == ""
	isContent := s.componentIsContent(release.Component)
	if content && !isContent {
		return model.Artifact{}, fmt.Errorf(
			"%w: %s is not a content component, so its artifacts need an os and an arch",
			ErrValidation, release.Component,
		)
	}
	if !content && isContent {
		return model.Artifact{}, fmt.Errorf(
			"%w: %s is a content component, so its artifacts must not carry an os or an arch",
			ErrValidation, release.Component,
		)
	}
	if content {
		expected := ContentArtifactFileName(release.Component, release.Version)
		if filename == "" {
			filename = expected
		}
		if filename != expected {
			return model.Artifact{}, fmt.Errorf("%w: content artifact must be named %s, got %s", ErrValidation, expected, filename)
		}
	}
	result, err := s.blobs.Put(reader)
	if err != nil {
		return model.Artifact{}, err
	}
	if expectedSHA256 != "" && !strings.EqualFold(expectedSHA256, result.SHA256) {
		return model.Artifact{}, fmt.Errorf("%w: sha256 mismatch: got %s want %s", ErrValidation, result.SHA256, expectedSHA256)
	}
	if !content && filename == "" {
		filename = release.Component + "-" + platformOS + "-" + arch
	}
	artifact := model.Artifact{
		ID:          model.NewID("artifact"),
		ReleaseID:   releaseID,
		OS:          platformOS,
		Arch:        arch,
		Filename:    filename,
		Size:        result.Size,
		SHA256:      result.SHA256,
		BlobSHA256:  result.SHA256,
		CreatedAtMS: model.NowMS(),
	}
	if err := s.store.PutArtifact(artifact); err != nil {
		return model.Artifact{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "artifact.upload",
		Subject: artifactSubject(releaseID, platformOS, arch),
		Detail:  artifact.SHA256,
	}); err != nil {
		return model.Artifact{}, err
	}
	return artifact, nil
}

// artifactSubject 是审计里的制品标识；平台无关制品用 content 代替空的 os/arch。
func artifactSubject(releaseID, platformOS, arch string) string {
	if platformOS == "" && arch == "" {
		return releaseID + "/content"
	}
	return releaseID + "/" + platformOS + "/" + arch
}

// desktopArtifact 是桌面自更新清单协议里的制品条目（字段名与顺序无关，值必须一致）。
type desktopArtifact struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

type desktopKillSwitch struct {
	ExpiresAt          string `json:"expiresAt"`
	MinimumSafeVersion string `json:"minimumSafeVersion"`
}

// desktopPayload 严格对齐 update-service.ts 的 UpdateManifestPayloadSchema（.strict()）。
type desktopPayload struct {
	Artifacts               []desktopArtifact  `json:"artifacts"`
	Channel                 string             `json:"channel"`
	ExpiresAt               string             `json:"expiresAt"`
	KillSwitch              *desktopKillSwitch `json:"killSwitch"`
	MinimumSupportedVersion string             `json:"minimumSupportedVersion"`
	PublishedAt             string             `json:"publishedAt"`
	ReleaseNotesURL         string             `json:"releaseNotesUrl"`
	Version                 string             `json:"version"`
}

// desktopPlatform 把仓库的 os 命名映射到桌面客户端的 platform 枚举。
func desktopPlatform(platformOS string) (string, bool) {
	switch platformOS {
	case "windows":
		return "win32", true
	case "darwin":
		return "darwin", true
	case "linux":
		return "linux", true
	default:
		return "", false
	}
}

// desktopArch 把仓库的 arch 命名映射到桌面客户端的 arch 枚举。
func desktopArch(arch string) (string, bool) {
	switch arch {
	case "amd64":
		return "x64", true
	case "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

// Manifest 构建并签名桌面自更新清单。component 为空时使用 desktop。
func (s *Service) Manifest(component, channel string) ([]byte, error) {
	if s.privateKey == nil {
		return nil, errors.New("release: signing key is not loaded")
	}
	if component == "" {
		component = DesktopComponent
	}
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	release, err := s.activeRelease(component, channel)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.store.ListArtifacts(release.ID)
	if err != nil {
		return nil, err
	}
	entries := make([]desktopArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		platform, ok := desktopPlatform(artifact.OS)
		if !ok {
			continue
		}
		clientArch, ok := desktopArch(artifact.Arch)
		if !ok {
			continue
		}
		entries = append(entries, desktopArtifact{
			Platform: platform,
			Arch:     clientArch,
			SHA256:   artifact.SHA256,
			Size:     artifact.Size,
			URL:      s.cfg.PublicBaseURL + "/v1/blobs/" + artifact.BlobSHA256,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: release %s has no desktop artifacts for darwin/linux/win32", ErrValidation, release.ID)
	}
	payload := desktopPayload{
		Artifacts:               entries,
		Channel:                 release.Channel,
		ExpiresAt:               formatTimestamp(time.Now().UTC().Add(time.Duration(s.cfg.ManifestTTLHours) * time.Hour)),
		KillSwitch:              nil,
		MinimumSupportedVersion: release.MinimumSupportedVersion,
		PublishedAt:             formatTimestamp(time.UnixMilli(release.PublishedAtMS).UTC()),
		ReleaseNotesURL:         s.notesURL(release),
		Version:                 release.Version,
	}
	if payload.MinimumSupportedVersion == "" {
		payload.MinimumSupportedVersion = "0.0.0"
	}
	if release.KillSwitch != nil {
		payload.KillSwitch = &desktopKillSwitch{
			ExpiresAt:          release.KillSwitch.ExpiresAt,
			MinimumSafeVersion: release.KillSwitch.MinimumSafeVersion,
		}
	}
	return sign.SignEnvelope(s.privateKey, payload)
}

func (s *Service) notesURL(release model.Release) string {
	if strings.TrimSpace(release.NotesURL) != "" {
		return release.NotesURL
	}
	return s.cfg.PublicBaseURL + "/releases/" + release.Version
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("want RFC3339 timestamp: %w", err)
	}
	return parsed, nil
}

// PublicKeyPEM 返回签名公钥 PEM，未加载私钥时报错。
func (s *Service) PublicKeyPEM() ([]byte, error) {
	if s.privateKey == nil {
		return nil, errors.New("release: signing key is not loaded")
	}
	return sign.MarshalPublicKeyPEM(s.privateKey.Public().(ed25519.PublicKey))
}
