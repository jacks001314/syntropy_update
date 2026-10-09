// Package adminclient 是升级服务管理面（/v1/admin）的 HTTP 客户端。
//
// 存在的理由只有一条：灌库（读构建清单 → 建发布 → 传制品 → 推渠道指针 → 发 BOM）
// 这段逻辑只能有一份实现。CLI 的 import-bundle 在本地模式直接用 *release.Service，
// 在远端模式用本包的 Client，两者都满足 bundle.Service，于是 bundle.PlanAssets 与
// bundle.Import 一行不改。换一种写法——「再写一个 HTTP 版的灌库脚本」——同一张组件表、
// 同一套制品命名就有了第二份实现，两边漂移只是时间问题。
package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"syntropy_update/internal/bundle"
	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
)

// 编译期断言：管理面客户端与本地服务必须能互换，否则「本地灌库」与「远端灌库」
// 会悄悄变成两套不同语义。
var _ bundle.Service = (*Client)(nil)

// maxResponseBytes 给响应体兜个上限：管理面返回的都是元数据，正常不过几百 KB，
// 一个异常大的响应不该把 CLI 的内存吃光。
const maxResponseBytes = 8 << 20

// Options 是构造客户端时的可选项。
type Options struct {
	// Token 是管理面令牌（配置文件里的 adminTokens 之一）。
	Token string
	// Actor 是写进审计的操作者；为空时由各方法自己的 actor 参数决定。
	Actor string
	// HTTPClient 允许注入自定义客户端（测试或特殊网络环境）。
	HTTPClient *http.Client
}

// Client 访问升级服务的管理面 HTTP API。
type Client struct {
	baseURL string
	token   string
	actor   string
	http    *http.Client
}

// New 构造管理面客户端。baseURL 形如 https://app.syntropy-lab.com。
func New(baseURL string, options Options) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, errors.New("adminclient: base URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("adminclient: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("adminclient: base URL %q must be http or https", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("adminclient: base URL %q has no host", baseURL)
	}
	token := strings.TrimSpace(options.Token)
	if token == "" {
		return nil, errors.New("adminclient: admin token is required (-token, or adminTokens in the config file)")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	return &Client{
		baseURL: trimmed,
		token:   token,
		actor:   strings.TrimSpace(options.Actor),
		http:    client,
	}, nil
}

// BaseURL 返回归一后的服务地址（去掉尾部斜杠）。
func (c *Client) BaseURL() string { return c.baseURL }

// APIError 是管理面返回的错误；Code 是服务端错误码（invalid_request / not_found / ...）。
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("update service: HTTP %d", e.Status)
	}
	return fmt.Sprintf("update service (%s): %s", e.Code, e.Message)
}

// Unwrap 把服务端错误码映射回领域哨兵值，让调用方（bundle.Import）用 errors.Is 就能
// 分辨「输入不合法」与「查不到」，不必去认 HTTP 状态码。
func (e *APIError) Unwrap() error {
	switch e.Code {
	case "invalid_request":
		return release.ErrValidation
	case "not_found":
		return release.ErrNotFound
	default:
		return nil
	}
}

func decodeAPIError(status int, content []byte) error {
	payload := struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{}
	if err := json.Unmarshal(content, &payload); err != nil {
		return &APIError{Status: status, Code: "unknown", Message: strings.TrimSpace(string(content))}
	}
	return &APIError{Status: status, Code: payload.Error, Message: payload.Message}
}

// uploadBody 是给上传用的请求体壳：http.Transport 发完请求会 Close 请求体，而调用方
// （bundle.Import）在上传返回后还要自己关掉那个文件——本地后端不关 reader，远端后端却
// 替它关了，同一段调用代码就会在两条路上表现不一致（「file already closed」）。包一层
// 只读不关的壳，把「谁负责关文件」这件事还给调用方。
type uploadBody struct {
	reader io.Reader
}

func (b uploadBody) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b uploadBody) Close() error { return nil }

// request 是所有调用的唯一出口：统一带令牌与审计操作者，统一把非 2xx 翻成 APIError。
// bundle.Service 的方法签名没有 context，所以这里固定用 Background。
func (c *Client) request(method, path string, body io.Reader, headers map[string]string, actor string) ([]byte, error) {
	var payload io.Reader
	if body != nil {
		payload = uploadBody{reader: body}
	}
	request, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+path, payload)
	if err != nil {
		return nil, fmt.Errorf("adminclient: build request %s %s: %w", method, path, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if value := c.actorOf(actor); value != "" {
		request.Header.Set("X-Actor", value)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("adminclient: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("adminclient: read response of %s %s: %w", method, path, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, decodeAPIError(response.StatusCode, content)
	}
	return content, nil
}

func (c *Client) requestJSON(method, path string, payload any, actor string) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("adminclient: encode body of %s %s: %w", method, path, err)
	}
	return c.request(method, path, bytes.NewReader(encoded), map[string]string{
		"Content-Type": "application/json",
	}, actor)
}

func (c *Client) actorOf(actor string) string {
	if value := strings.TrimSpace(actor); value != "" {
		return value
	}
	return c.actor
}

// EnsureComponent 在服务上登记（或更新）组件定义；管理面的组件写入是幂等的。
func (c *Client) EnsureComponent(component model.Component) (model.Component, error) {
	payload := map[string]string{
		"displayName": component.DisplayName,
		"kind":        component.Kind,
		"name":        component.Name,
		"versioning":  component.Versioning,
	}
	if value := strings.TrimSpace(component.MinProductVersion); value != "" {
		payload["minProductVersion"] = value
	}
	content, err := c.requestJSON(http.MethodPost, "/v1/admin/components", payload, "")
	if err != nil {
		return model.Component{}, err
	}
	var saved model.Component
	if err := json.Unmarshal(content, &saved); err != nil {
		return model.Component{}, fmt.Errorf("adminclient: decode component: %w", err)
	}
	return saved, nil
}

// FindRelease 按 (组件, 渠道, 版本) 查发布；查不到时返回 release.ErrNotFound。管理面只有
// 「按组件列发布」这一个读口，所以这里在客户端侧筛出唯一那个人要的版本。
func (c *Client) FindRelease(component, channel, version string) (model.Release, error) {
	path := "/v1/admin/releases?component=" + url.QueryEscape(component)
	content, err := c.request(http.MethodGet, path, nil, nil, "")
	if err != nil {
		return model.Release{}, err
	}
	payload := struct {
		Releases []model.Release `json:"releases"`
	}{}
	if err := json.Unmarshal(content, &payload); err != nil {
		return model.Release{}, fmt.Errorf("adminclient: decode releases: %w", err)
	}
	for _, candidate := range payload.Releases {
		if candidate.Channel == channel && candidate.Version == version {
			return candidate, nil
		}
	}
	return model.Release{}, fmt.Errorf(
		"%w: release %s %s/%s", release.ErrNotFound, component, channel, version,
	)
}

// CreateRelease 建一份不推进渠道指针的发布。publish 必须显式为 false：管理面默认 publish，
// 而灌库的次序是「先建发布与传制品，最后才推指针」——指针一推，客户端立刻就能拉到清单。
func (c *Client) CreateRelease(value model.Release, actor string) (model.Release, error) {
	payload := map[string]any{
		"channel":                 value.Channel,
		"component":               value.Component,
		"minimumSupportedVersion": value.MinimumSupportedVersion,
		"notes":                   value.Notes,
		"notesUrl":                value.NotesURL,
		"publish":                 false,
		"version":                 value.Version,
	}
	content, err := c.requestJSON(http.MethodPost, "/v1/admin/releases", payload, actor)
	if err != nil {
		return model.Release{}, err
	}
	var saved model.Release
	if err := json.Unmarshal(content, &saved); err != nil {
		return model.Release{}, fmt.Errorf("adminclient: decode release: %w", err)
	}
	return saved, nil
}

// Artifacts 列出某次发布已登记的制品。
func (c *Client) Artifacts(releaseID string) ([]model.Artifact, error) {
	path := "/v1/admin/releases/" + url.PathEscape(releaseID)
	content, err := c.request(http.MethodGet, path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	payload := struct {
		Artifacts []model.Artifact `json:"artifacts"`
	}{}
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, fmt.Errorf("adminclient: decode artifacts: %w", err)
	}
	return payload.Artifacts, nil
}

// UploadArtifact 把制品流上传到指定槽位。文件名与摘要走头字段，与服务端
// handleUploadArtifact 的约定一致；平台无关的内容制品用 any 段占位。
func (c *Client) UploadArtifact(releaseID, slot, platformOS, arch, filename, expectedSHA256 string, reader io.Reader) (model.Artifact, error) {
	path := fmt.Sprintf(
		"/v1/admin/releases/%s/artifacts/%s/%s/%s",
		url.PathEscape(releaseID), url.PathEscape(slot), platformSegment(platformOS), platformSegment(arch),
	)
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if value := strings.TrimSpace(filename); value != "" {
		headers["X-Filename"] = value
	}
	if value := strings.TrimSpace(expectedSHA256); value != "" {
		headers["X-Sha256"] = value
	}
	content, err := c.request(http.MethodPut, path, reader, headers, "")
	if err != nil {
		return model.Artifact{}, err
	}
	var artifact model.Artifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		return model.Artifact{}, fmt.Errorf("adminclient: decode artifact: %w", err)
	}
	return artifact, nil
}

// platformSegment 与 api.platformSegment 同一口径：空值走 any 占位段。
func platformSegment(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return url.PathEscape(trimmed)
	}
	return "any"
}

// PointChannel 把 (组件, 渠道) 的指针指向这份发布——客户端从这一刻起就能拉到它。
func (c *Client) PointChannel(component, channel, releaseID, actor string) error {
	path := "/v1/admin/releases/" + url.PathEscape(releaseID) + "/publish"
	_, err := c.request(http.MethodPost, path, nil, nil, actor)
	return err
}

// GetDistribution 读某产品版本在某渠道上的 BOM；没有时返回 release.ErrNotFound。
func (c *Client) GetDistribution(channel, productVersion string) (model.Distribution, error) {
	path := "/v1/admin/distributions?channel=" + url.QueryEscape(channel)
	content, err := c.request(http.MethodGet, path, nil, nil, "")
	if err != nil {
		return model.Distribution{}, err
	}
	payload := struct {
		Distributions []model.Distribution `json:"distributions"`
	}{}
	if err := json.Unmarshal(content, &payload); err != nil {
		return model.Distribution{}, fmt.Errorf("adminclient: decode distributions: %w", err)
	}
	for _, candidate := range payload.Distributions {
		if candidate.ProductVersion == productVersion {
			return candidate, nil
		}
	}
	return model.Distribution{}, fmt.Errorf(
		"%w: distribution %s/%s", release.ErrNotFound, channel, productVersion,
	)
}

// PublishDistribution 发布 BOM。管理面只收 (组件名, 版本)，服务端自己解析发布 ID——
// 所以这里不带 ReleaseID，避免客户端与账本各说一套。
func (c *Client) PublishDistribution(distribution model.Distribution, actor string) (model.Distribution, error) {
	type componentPayload struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	components := make([]componentPayload, 0, len(distribution.Components))
	for _, component := range distribution.Components {
		components = append(components, componentPayload{Name: component.Name, Version: component.Version})
	}
	payload := struct {
		Channel        string             `json:"channel"`
		Components     []componentPayload `json:"components"`
		ProductVersion string             `json:"productVersion"`
	}{
		Channel:        distribution.Channel,
		Components:     components,
		ProductVersion: distribution.ProductVersion,
	}
	content, err := c.requestJSON(http.MethodPost, "/v1/admin/distributions", payload, actor)
	if err != nil {
		return model.Distribution{}, err
	}
	var saved model.Distribution
	if err := json.Unmarshal(content, &saved); err != nil {
		return model.Distribution{}, fmt.Errorf("adminclient: decode distribution: %w", err)
	}
	return saved, nil
}
