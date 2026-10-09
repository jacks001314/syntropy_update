package release

import (
	"errors"
	"fmt"
	"time"

	"syntropy_update/internal/model"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

// componentArtifact 是组件清单与 BOM 里的制品条目。
type componentArtifact struct {
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
	OS       string `json:"os"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	// Slot 是制品在发布里的槽位：产品组件靠它区分 desktop/mbot/nbot/node-repl/
	// app-server，其余组件等于组件名本身。客户端按 (slot, os, arch) 选件。
	Slot string `json:"slot"`
	URL  string `json:"url"`
}

// componentPayload 是集群组件（mbot/nbot/codex/...）清单的签名载荷。
type componentPayload struct {
	Artifacts               []componentArtifact `json:"artifacts"`
	Channel                 string              `json:"channel"`
	Component               string              `json:"component"`
	ExpiresAt               string              `json:"expiresAt"`
	KillSwitch              *desktopKillSwitch  `json:"killSwitch"`
	MinimumSupportedVersion string              `json:"minimumSupportedVersion"`
	PublishedAt             string              `json:"publishedAt"`
	ReleaseNotesURL         string              `json:"releaseNotesUrl"`
	Version                 string              `json:"version"`
}

// activeRelease 返回 (component, channel) 当前指向的有效发布。
func (s *Service) activeRelease(component, channel string) (model.Release, error) {
	pointer, err := s.store.GetChannelPointer(component, channel)
	if err != nil {
		return model.Release{}, fmt.Errorf("%w: no active release for %s/%s", store.ErrNotFound, component, channel)
	}
	if pointer.ReleaseID == "" {
		return model.Release{}, fmt.Errorf("%w: channel %s/%s has no active release", store.ErrNotFound, component, channel)
	}
	releaseValue, err := s.store.GetRelease(pointer.ReleaseID)
	if err != nil {
		return model.Release{}, err
	}
	if releaseValue.Yanked {
		return model.Release{}, fmt.Errorf("%w: release %s is yanked", store.ErrNotFound, releaseValue.ID)
	}
	return releaseValue, nil
}

func (s *Service) componentArtifacts(releaseID string) ([]componentArtifact, error) {
	artifacts, err := s.store.ListArtifacts(releaseID)
	if err != nil {
		return nil, err
	}
	entries := make([]componentArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		entries = append(entries, componentArtifact{
			Arch:     artifact.Arch,
			Filename: artifact.Filename,
			OS:       artifact.OS,
			SHA256:   artifact.SHA256,
			Size:     artifact.Size,
			Slot:     artifact.Slot,
			URL:      s.cfg.PublicBaseURL + "/v1/blobs/" + artifact.BlobSHA256,
		})
	}
	return entries, nil
}

// ComponentManifest 构建并签名某集群组件的更新清单。
func (s *Service) ComponentManifest(component, channel string) ([]byte, error) {
	if s.privateKey == nil {
		return nil, errors.New("release: signing key is not loaded")
	}
	if component == "" {
		return nil, fmt.Errorf("%w: component is required", ErrValidation)
	}
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	releaseValue, err := s.activeRelease(component, channel)
	if err != nil {
		return nil, err
	}
	entries, err := s.componentArtifacts(releaseValue.ID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: release %s has no artifacts", ErrValidation, releaseValue.ID)
	}
	payload := componentPayload{
		Artifacts:               entries,
		Channel:                 releaseValue.Channel,
		Component:               releaseValue.Component,
		ExpiresAt:               formatTimestamp(time.Now().UTC().Add(time.Duration(s.cfg.ManifestTTLHours) * time.Hour)),
		KillSwitch:              nil,
		MinimumSupportedVersion: releaseValue.MinimumSupportedVersion,
		PublishedAt:             formatTimestamp(time.UnixMilli(releaseValue.PublishedAtMS).UTC()),
		ReleaseNotesURL:         s.notesURL(releaseValue),
		Version:                 releaseValue.Version,
	}
	if payload.MinimumSupportedVersion == "" {
		payload.MinimumSupportedVersion = "0.0.0"
	}
	if releaseValue.KillSwitch != nil {
		payload.KillSwitch = &desktopKillSwitch{
			ExpiresAt:          releaseValue.KillSwitch.ExpiresAt,
			MinimumSafeVersion: releaseValue.KillSwitch.MinimumSafeVersion,
		}
	}
	return sign.SignEnvelope(s.privateKey, payload)
}
