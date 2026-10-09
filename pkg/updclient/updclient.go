// Package updclient 是升级服务的 Go 客户端：拉取签名清单、校验签名、下载并校验制品。
//
// 供 Master / Node / 其他 Go 侧消费者复用；桌面端使用 TypeScript 实现
// （desktop_app/apps/desktop/src/main/updates/update-service.ts）。
package updclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syntropy_update/internal/sign"
)

// Artifact 是清单中的一个制品。
type Artifact struct {
	Arch     string
	Filename string
	OS       string
	SHA256   string
	Size     int64
	URL      string
}

// Manifest 是校验通过后的组件清单。
type Manifest struct {
	Artifacts               []Artifact
	Channel                 string
	Component               string
	ExpiresAt               time.Time
	MinimumSupportedVersion string
	PublishedAt             time.Time
	ReleaseNotesURL         string
	Version                 string
}

// Pick 返回指定 (os, arch) 的制品；这个平台没有制品时，回落到与平台无关的那一件
// （templates / skills 这类内容制品，清单里 os/arch 都为空）。
func (m Manifest) Pick(platformOS, arch string) (Artifact, bool) {
	if platformOS != "" || arch != "" {
		for _, artifact := range m.Artifacts {
			if artifact.OS == platformOS && artifact.Arch == arch {
				return artifact, true
			}
		}
	}
	for _, artifact := range m.Artifacts {
		if artifact.OS == "" && artifact.Arch == "" {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// Client 是升级服务客户端。
type Client struct {
	BaseURL    string
	Channel    string
	HTTPClient *http.Client
	PublicKey  ed25519.PublicKey
	Token      string
}

type componentPayload struct {
	Artifacts []struct {
		Arch     string `json:"arch"`
		Filename string `json:"filename"`
		OS       string `json:"os"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size"`
		URL      string `json:"url"`
	} `json:"artifacts"`
	Channel                 string `json:"channel"`
	Component               string `json:"component"`
	ExpiresAt               string `json:"expiresAt"`
	MinimumSupportedVersion string `json:"minimumSupportedVersion"`
	PublishedAt             string `json:"publishedAt"`
	ReleaseNotesURL         string `json:"releaseNotesUrl"`
	Version                 string `json:"version"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) baseURL() (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("updclient: invalid base url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("updclient: base url must be http or https")
	}
	return parsed, nil
}

func (c *Client) get(ctx context.Context, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("updclient: request %s: %w", target, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("updclient: request %s: unexpected status %d", target, response.StatusCode)
	}
	return response, nil
}

// FetchComponentManifest 拉取并校验某组件的签名清单。
func (c *Client) FetchComponentManifest(ctx context.Context, component string) (Manifest, error) {
	if c.PublicKey == nil {
		return Manifest{}, errors.New("updclient: public key is required")
	}
	base, err := c.baseURL()
	if err != nil {
		return Manifest{}, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/components/" + url.PathEscape(component) + "/manifest"
	query := base.Query()
	if c.Channel != "" {
		query.Set("channel", c.Channel)
	}
	base.RawQuery = query.Encode()

	response, err := c.get(ctx, base.String())
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("updclient: read manifest: %w", err)
	}
	if len(raw) > 256*1024 {
		return Manifest{}, errors.New("updclient: manifest is too large")
	}
	var envelope sign.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Manifest{}, fmt.Errorf("updclient: decode envelope: %w", err)
	}
	if _, err := sign.VerifyEnvelope(c.PublicKey, envelope); err != nil {
		return Manifest{}, fmt.Errorf("updclient: verify manifest: %w", err)
	}
	var payload componentPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return Manifest{}, fmt.Errorf("updclient: decode payload: %w", err)
	}
	manifest := Manifest{
		Channel:                 payload.Channel,
		Component:               payload.Component,
		MinimumSupportedVersion: payload.MinimumSupportedVersion,
		ReleaseNotesURL:         payload.ReleaseNotesURL,
		Version:                 payload.Version,
	}
	if published, err := time.Parse(time.RFC3339, payload.PublishedAt); err == nil {
		manifest.PublishedAt = published
	}
	if expires, err := time.Parse(time.RFC3339, payload.ExpiresAt); err == nil {
		manifest.ExpiresAt = expires
		if expires.Before(time.Now()) {
			return Manifest{}, errors.New("updclient: manifest has expired")
		}
	}
	host := base.Hostname()
	for _, entry := range payload.Artifacts {
		artifactURL, err := url.Parse(entry.URL)
		if err != nil || artifactURL.Hostname() != host {
			return Manifest{}, fmt.Errorf("updclient: artifact url %q is not on %s", entry.URL, host)
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			Arch:     entry.Arch,
			Filename: entry.Filename,
			OS:       entry.OS,
			SHA256:   entry.SHA256,
			Size:     entry.Size,
			URL:      entry.URL,
		})
	}
	if len(manifest.Artifacts) == 0 {
		return Manifest{}, errors.New("updclient: manifest has no artifacts")
	}
	return manifest, nil
}

// Download 下载制品到 destination，并校验 sha256 与大小。
func (c *Client) Download(ctx context.Context, artifact Artifact, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("updclient: create destination dir: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return fmt.Errorf("updclient: create temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	response, err := c.get(ctx, artifact.URL)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hasher), response.Body)
	if err != nil {
		return fmt.Errorf("updclient: download %s: %w", artifact.URL, err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("updclient: fsync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("updclient: close: %w", err)
	}
	if artifact.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), artifact.SHA256) {
		return fmt.Errorf("updclient: sha256 mismatch for %s", artifact.URL)
	}
	if artifact.Size > 0 && written != artifact.Size {
		return fmt.Errorf("updclient: size mismatch for %s: got %d want %d", artifact.URL, written, artifact.Size)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("updclient: commit download: %w", err)
	}
	return nil
}
