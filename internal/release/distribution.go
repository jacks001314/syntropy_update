package release

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"syntropy_update/internal/model"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

// ProductName 是分发定义（BOM）的产品名。
const ProductName = "syntropy"

type distributionComponentPayload struct {
	Artifacts []componentArtifact `json:"artifacts"`
	Kind      string              `json:"kind"`
	Name      string              `json:"name"`
	Version   string              `json:"version"`
}

type distributionPayload struct {
	Channel        string                         `json:"channel"`
	Components     []distributionComponentPayload `json:"components"`
	ExpiresAt      string                         `json:"expiresAt"`
	Product        string                         `json:"product"`
	ProductVersion string                         `json:"productVersion"`
	PublishedAt    string                         `json:"publishedAt"`
}

// PublishDistribution 创建某产品版本在某渠道上的组件清单（BOM）。
func (s *Service) PublishDistribution(distribution model.Distribution, actor string) (model.Distribution, error) {
	distribution.ProductVersion = strings.TrimSpace(distribution.ProductVersion)
	if !versionPattern.MatchString(distribution.ProductVersion) {
		return model.Distribution{}, fmt.Errorf("%w: productVersion %q must be MAJOR.MINOR.PATCH", ErrValidation, distribution.ProductVersion)
	}
	if !s.cfg.IsChannel(distribution.Channel) {
		return model.Distribution{}, fmt.Errorf("%w: unknown channel %q", ErrValidation, distribution.Channel)
	}
	if len(distribution.Components) == 0 {
		return model.Distribution{}, fmt.Errorf("%w: at least one component is required", ErrValidation)
	}
	seen := make(map[string]struct{}, len(distribution.Components))
	for index, component := range distribution.Components {
		name := strings.TrimSpace(component.Name)
		if name == "" {
			return model.Distribution{}, fmt.Errorf("%w: component name is required", ErrValidation)
		}
		if _, duplicated := seen[name]; duplicated {
			return model.Distribution{}, fmt.Errorf("%w: duplicated component %q", ErrValidation, name)
		}
		seen[name] = struct{}{}
		if !versionPattern.MatchString(component.Version) {
			return model.Distribution{}, fmt.Errorf("%w: component %s version %q must be MAJOR.MINOR.PATCH", ErrValidation, name, component.Version)
		}
		releaseValue, err := s.store.FindRelease(name, distribution.Channel, component.Version)
		if errors.Is(err, store.ErrNotFound) {
			return model.Distribution{}, fmt.Errorf("%w: %s@%s is not published on channel %s", ErrValidation, name, component.Version, distribution.Channel)
		}
		if err != nil {
			return model.Distribution{}, err
		}
		distribution.Components[index] = model.DistributionComponent{Name: name, Version: component.Version, ReleaseID: releaseValue.ID}
	}
	// 产品版本是 BOM 的锚点：产品自己换了就必须升产品版本，插件换了则可以随时推进。
	existing, existingErr := s.store.GetDistribution(distribution.Channel, distribution.ProductVersion)
	action := "distribution.publish"
	now := model.NowMS()
	switch {
	case existingErr == nil && existing.ProductVersion != "":
		if !sameProductRelease(existing.Components, distribution.Components) {
			return model.Distribution{}, fmt.Errorf(
				"%w: distribution %s/%s already exists for a different product release; bump the product version instead",
				ErrValidation, distribution.Channel, distribution.ProductVersion,
			)
		}
		action = "distribution.update"
		// 产品没动、只是插件推进：创建时间不变，发布时间刷新。
		distribution.CreatedAtMS = existing.CreatedAtMS
	case existingErr != nil && !errors.Is(existingErr, store.ErrNotFound):
		return model.Distribution{}, existingErr
	default:
		distribution.CreatedAtMS = now
	}
	distribution.PublishedAtMS = now
	if err := s.store.PutDistribution(distribution); err != nil {
		return model.Distribution{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  action,
		Actor:   actor,
		Subject: distribution.Channel + "/" + distribution.ProductVersion,
	}); err != nil {
		return model.Distribution{}, err
	}
	return distribution, nil
}

// GetDistribution 读取指定产品版本的分发定义。
func (s *Service) GetDistribution(channel, productVersion string) (model.Distribution, error) {
	if !s.cfg.IsChannel(channel) {
		return model.Distribution{}, fmt.Errorf("%w: unknown channel %q", ErrValidation, channel)
	}
	return s.store.GetDistribution(channel, productVersion)
}

// LatestDistribution 返回渠道上最近创建的分发定义。
func (s *Service) LatestDistribution(channel string) (model.Distribution, error) {
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	distributions, err := s.store.ListDistributions(channel)
	if err != nil {
		return model.Distribution{}, err
	}
	var latest model.Distribution
	for _, distribution := range distributions {
		if distribution.CreatedAtMS > latest.CreatedAtMS {
			latest = distribution
		}
	}
	if latest.ProductVersion == "" {
		return model.Distribution{}, fmt.Errorf("%w: no distribution on channel %s", store.ErrNotFound, channel)
	}
	return latest, nil
}

// SignedDistribution 返回签名后的分发定义；productVersion 为空时取渠道最新。
func (s *Service) SignedDistribution(channel, productVersion string) ([]byte, error) {
	if s.privateKey == nil {
		return nil, errors.New("release: signing key is not loaded")
	}
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	var (
		distribution model.Distribution
		err          error
	)
	if productVersion == "" {
		distribution, err = s.LatestDistribution(channel)
	} else {
		distribution, err = s.GetDistribution(channel, productVersion)
	}
	if err != nil {
		return nil, err
	}
	components := make([]distributionComponentPayload, 0, len(distribution.Components))
	for _, component := range distribution.Components {
		artifacts, err := s.componentArtifacts(component.ReleaseID)
		if err != nil {
			return nil, err
		}
		kind := model.KindBinary
		if definition, err := s.store.GetComponent(component.Name); err == nil && definition.Kind != "" {
			kind = definition.Kind
		}
		components = append(components, distributionComponentPayload{
			Artifacts: artifacts,
			Kind:      kind,
			Name:      component.Name,
			Version:   component.Version,
		})
	}
	payload := distributionPayload{
		Channel:        distribution.Channel,
		Components:     components,
		ExpiresAt:      formatTimestamp(time.Now().UTC().Add(time.Duration(s.cfg.ManifestTTLHours) * time.Hour)),
		Product:        ProductName,
		ProductVersion: distribution.ProductVersion,
		PublishedAt:    formatTimestamp(time.UnixMilli(distribution.PublishedAtMS).UTC()),
	}
	return sign.SignEnvelope(s.privateKey, payload)
}

// sameProductRelease 判断两份 BOM 里的产品是不是同一份发布。
//
// 产品版本（`productVersion`）是 BOM 的锚点，产品自己只有一个版本号：产品换了，锚点就换了，
// 只能发新的产品版本。插件（codex / node-runtime / templates / skills）各有版本号，
// 可以随时推进 BOM 而不动产品版本——「插件可单独升级」在服务端的落点就是这里。
func sameProductRelease(existing, next []model.DistributionComponent) bool {
	current, ok := findDistributionComponent(existing, model.ComponentProduct)
	if !ok {
		return false
	}
	candidate, ok := findDistributionComponent(next, model.ComponentProduct)
	if !ok {
		return false
	}
	return current.ReleaseID == candidate.ReleaseID && current.Version == candidate.Version
}

// findDistributionComponent 按组件名取一条 BOM 条目。
func findDistributionComponent(components []model.DistributionComponent, name string) (model.DistributionComponent, bool) {
	for _, component := range components {
		if component.Name == name {
			return component, true
		}
	}
	return model.DistributionComponent{}, false
}
