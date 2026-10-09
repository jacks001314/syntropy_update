// Package api 暴露升级服务的 HTTP 接口：客户端分发面与管理面。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
	"syntropy_update/internal/store"
	"syntropy_update/internal/webui"
)

// Server 组合领域服务与传输层依赖。
type Server struct {
	service *release.Service
	store   *store.Store
	blobs   *blob.Store
	tokens  auth.Tokens
	log     *logrus.Logger
}

// New 构造 HTTP 服务。
func New(service *release.Service, metadata *store.Store, blobs *blob.Store, tokens auth.Tokens, logger *logrus.Logger) *Server {
	if logger == nil {
		logger = logrus.New()
	}
	return &Server{service: service, store: metadata, blobs: blobs, tokens: tokens, log: logger}
}

// scope 决定一条接口挂在哪种鉴权下。
type scope int

const (
	scopePublic scope = iota
	// scopeClient 是读取类客户端接口：thin 安装器首启时本机还没有任何配置，拉清单与
	// 制品必须能不带令牌走通，所以未配置客户端令牌时放行。
	scopeClient
	// scopeDevice 是上报类客户端接口：提交计划、库存、忽略、升级结果都是「代表一台设备
	// 写数据」，没配令牌时按 requireDeviceTokens 决定，不再无条件放行。
	scopeDevice
	scopeAdmin
)

type route struct {
	pattern string
	scope   scope
	handler handlerFunc
}

// routes 是全部接口的唯一事实来源：注册路由、以及「管理页没有引用不存在的接口」这类
// 结构测试都从这里取，页面和接口不会各改各的。
func (s *Server) routes() []route {
	return []route{
		{"GET /healthz", scopePublic, s.handleHealth},
		{"GET /v1/pubkey", scopePublic, s.handlePublicKey},
		{"GET /v1/manifest", scopeClient, s.handleManifest},
		{"GET /v1/blobs/{sha256}", scopeClient, s.handleBlob},
		{"GET /v1/components/{component}/manifest", scopeClient, s.handleComponentManifest},
		{"GET /v1/distributions/latest", scopeClient, s.handleLatestDistribution},
		{"GET /v1/distributions/{channel}/{productVersion}", scopeClient, s.handleGetDistribution},
		{"POST /v1/plan", scopeDevice, s.handleBuildPlan},
		{"POST /v1/inventory/{id}", scopeDevice, s.handleReportInventory},
		{"POST /v1/targets/{id}/ignores", scopeDevice, s.handleSetIgnore},
		{"DELETE /v1/targets/{id}/ignores/{component}", scopeDevice, s.handleClearIgnore},
		{"POST /v1/targets/{id}/results", scopeDevice, s.handleReportResult},

		{"GET /v1/admin/components", scopeAdmin, s.handleListComponents},
		{"POST /v1/admin/components", scopeAdmin, s.handlePutComponent},
		{"GET /v1/admin/releases", scopeAdmin, s.handleListReleases},
		{"POST /v1/admin/releases", scopeAdmin, s.handleCreateRelease},
		{"GET /v1/admin/releases/{id}", scopeAdmin, s.handleGetRelease},
		{"PUT /v1/admin/releases/{id}/artifacts/{slot}/{os}/{arch}", scopeAdmin, s.handleUploadArtifact},
		// 按内容摘要取回制品：内容制品没有 os/arch，摘要是最稳的键（管理面专用）。
		{"GET /v1/admin/artifacts/{sha256}", scopeAdmin, s.handleDownloadArtifact},
		{"POST /v1/admin/releases/{id}/publish", scopeAdmin, s.handlePublishRelease},
		{"POST /v1/admin/releases/{id}/yank", scopeAdmin, s.handleYankRelease},
		{"GET /v1/admin/distributions", scopeAdmin, s.handleListDistributions},
		{"POST /v1/admin/distributions", scopeAdmin, s.handlePublishDistribution},
		{"GET /v1/admin/inventory", scopeAdmin, s.handleListInventory},
		{"GET /v1/admin/ignores", scopeAdmin, s.handleListIgnores},
		{"GET /v1/admin/results", scopeAdmin, s.handleListResults},
		{"GET /v1/admin/plans", scopeAdmin, s.handleListPlans},
		{"GET /v1/admin/plans/{id}", scopeAdmin, s.handleGetPlan},
		{"GET /v1/admin/policies", scopeAdmin, s.handleListPolicies},
		{"PUT /v1/admin/policies/{component}/{channel}", scopeAdmin, s.handleSetPolicy},
		{"GET /v1/admin/audit", scopeAdmin, s.handleAudit},
	}
}

// Handler 返回注册好路由的处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, entry := range s.routes() {
		switch entry.scope {
		case scopeAdmin:
			mux.HandleFunc(entry.pattern, s.admin(entry.handler))
		case scopeClient:
			mux.HandleFunc(entry.pattern, s.client(entry.handler))
		case scopeDevice:
			mux.HandleFunc(entry.pattern, s.device(entry.handler))
		default:
			mux.HandleFunc(entry.pattern, s.public(entry.handler))
		}
	}
	// 运维管理页：静态单页本身不要令牌（否则连登录框都拿不到），它调用的
	// /v1/admin/* 仍然逐条校验 Bearer Token。
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusFound))
	// 管理页用相对路径引 app.css / app.js，所以必须把 /admin 前缀剥掉再交给 FileServer：
	// 否则 /admin/app.css 在资源 FS 里找不到，会被 SPA 回落成 index.html，
	// 浏览器拿到的就是一份没有样式、没有脚本的静态壳。
	mux.Handle("GET /admin/", http.StripPrefix("/admin", webui.Handler()))
	return s.withLogging(mux)
}

type handlerFunc func(http.ResponseWriter, *http.Request) error

func (s *Server) public(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.dispatch(w, r, next)
	}
}

func (s *Server) admin(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokens.AllowAdmin(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid admin token is required")
			return
		}
		s.dispatch(w, r, next)
	}
}

func (s *Server) client(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokens.AllowClient(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid client token is required")
			return
		}
		s.dispatch(w, r, next)
	}
}

// device 是上报类客户端接口的鉴权：比读取类严，未配置客户端令牌时由 requireDeviceTokens 决定。
func (s *Server) device(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokens.AllowDevice(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid client token is required")
			return
		}
		s.dispatch(w, r, next)
	}
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, next handlerFunc) {
	if err := next(w, r); err != nil {
		status := http.StatusInternalServerError
		code := "internal_error"
		switch {
		case errors.Is(err, release.ErrValidation):
			status, code = http.StatusBadRequest, "invalid_request"
		case errors.Is(err, store.ErrNotFound), errors.Is(err, blob.ErrInvalidDigest):
			status, code = http.StatusNotFound, "not_found"
		}
		if status >= 500 {
			s.log.WithError(err).WithField("path", r.URL.Path).Error("request failed")
		}
		writeError(w, status, code, err.Error())
	}
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.log.WithFields(logrus.Fields{
			"method": r.Method,
			"path":   r.URL.Path,
			"status": recorder.status,
		}).Info("http request")
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeRawJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// handleHealth 是无鉴权的存活探针；顺带告诉运维/管理页管理面与客户端鉴权有没有开，
// 免得打开管理页只看到一个「未授权」而不知道是没配令牌还是令牌写错了。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"adminEnabled":       s.tokens.AdminEnabled(),
		"clientTokensSet":    s.tokens.ClientTokensConfigured(),
		"deviceAuthRequired": s.tokens.DeviceRequired(),
		"readAuthRequired":   s.tokens.ReadRequired(),
	})
	return nil
}

// handleDownloadArtifact 按内容摘要下发制品，供管理面取件核对（桌面端走 /v1/blobs/）。
func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) error {
	digest := r.PathValue("sha256")
	file, info, err := s.blobs.Open(digest)
	if err != nil {
		if errors.Is(err, blob.ErrInvalidDigest) {
			return err
		}
		return fmt.Errorf("%w: blob %s", store.ErrNotFound, digest)
	}
	defer func() { _ = file.Close() }()
	filename := sanitizeFilename(r.URL.Query().Get("filename"))
	if filename == "" {
		filename = digest
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filename, info.ModTime(), file)
	return nil
}

// sanitizeFilename 只保留安全字符，避免把用户输入拼进 Content-Disposition 造成头注入。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	for _, symbol := range name {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= 'A' && symbol <= 'Z',
			symbol >= '0' && symbol <= '9', symbol == '.', symbol == '-', symbol == '_':
			builder.WriteRune(symbol)
		default:
			builder.WriteRune('_')
		}
	}
	return strings.Trim(builder.String(), ".")
}

func (s *Server) handlePublicKey(w http.ResponseWriter, _ *http.Request) error {
	pemBytes, err := s.service.PublicKeyPEM()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pemBytes)
	return nil
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) error {
	component := r.URL.Query().Get("component")
	channel := r.URL.Query().Get("channel")
	encoded, err := s.service.Manifest(component, channel)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeRawJSON(w, http.StatusOK, encoded)
	return nil
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) error {
	digest := r.PathValue("sha256")
	file, info, err := s.blobs.Open(digest)
	if err != nil {
		if errors.Is(err, blob.ErrInvalidDigest) {
			return err
		}
		return fmt.Errorf("%w: blob %s", store.ErrNotFound, digest)
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, digest, info.ModTime(), file)
	return nil
}

func (s *Server) handleComponentManifest(w http.ResponseWriter, r *http.Request) error {
	encoded, err := s.service.ComponentManifest(r.PathValue("component"), r.URL.Query().Get("channel"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeRawJSON(w, http.StatusOK, encoded)
	return nil
}

func (s *Server) handleLatestDistribution(w http.ResponseWriter, r *http.Request) error {
	encoded, err := s.service.SignedDistribution(r.URL.Query().Get("channel"), "")
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeRawJSON(w, http.StatusOK, encoded)
	return nil
}

func (s *Server) handleGetDistribution(w http.ResponseWriter, r *http.Request) error {
	encoded, err := s.service.SignedDistribution(r.PathValue("channel"), r.PathValue("productVersion"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeRawJSON(w, http.StatusOK, encoded)
	return nil
}

func (s *Server) handleListDistributions(w http.ResponseWriter, r *http.Request) error {
	distributions, err := s.store.ListDistributions(r.URL.Query().Get("channel"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"distributions": distributions})
	return nil
}

type publishDistributionRequest struct {
	Channel        string `json:"channel"`
	ProductVersion string `json:"productVersion"`
	Components     []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"components"`
}

func (s *Server) handlePublishDistribution(w http.ResponseWriter, r *http.Request) error {
	var request publishDistributionRequest
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	distribution := model.Distribution{Channel: request.Channel, ProductVersion: request.ProductVersion}
	for _, component := range request.Components {
		distribution.Components = append(distribution.Components, model.DistributionComponent{
			Name:    component.Name,
			Version: component.Version,
		})
	}
	saved, err := s.service.PublishDistribution(distribution, actorOf(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, saved)
	return nil
}

func (s *Server) handleListComponents(w http.ResponseWriter, _ *http.Request) error {
	components, err := s.store.ListComponents()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": components})
	return nil
}

func (s *Server) handlePutComponent(w http.ResponseWriter, r *http.Request) error {
	var component model.Component
	if err := decodeJSON(r, &component); err != nil {
		return err
	}
	saved, err := s.service.EnsureComponent(component)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, saved)
	return nil
}

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) error {
	releases, err := s.store.ListReleases(r.URL.Query().Get("component"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases})
	return nil
}

type createReleaseRequest struct {
	Component               string            `json:"component"`
	Version                 string            `json:"version"`
	Channel                 string            `json:"channel"`
	Notes                   string            `json:"notes"`
	NotesURL                string            `json:"notesUrl"`
	MinimumSupportedVersion string            `json:"minimumSupportedVersion"`
	KillSwitch              *model.KillSwitch `json:"killSwitch"`
	Publish                 *bool             `json:"publish"`
}

func (s *Server) handleCreateRelease(w http.ResponseWriter, r *http.Request) error {
	var request createReleaseRequest
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	releaseValue := model.Release{
		Component:               request.Component,
		Version:                 request.Version,
		Channel:                 request.Channel,
		Notes:                   request.Notes,
		NotesURL:                request.NotesURL,
		MinimumSupportedVersion: request.MinimumSupportedVersion,
		KillSwitch:              request.KillSwitch,
	}
	actor := actorOf(r)
	publish := request.Publish == nil || *request.Publish
	var (
		saved model.Release
		err   error
	)
	if publish {
		saved, err = s.service.PublishRelease(releaseValue, actor)
	} else {
		saved, err = s.service.CreateRelease(releaseValue, actor)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, saved)
	return nil
}

func (s *Server) handleGetRelease(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	releaseValue, err := s.store.GetRelease(id)
	if err != nil {
		return err
	}
	artifacts, err := s.store.ListArtifacts(id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": releaseValue, "artifacts": artifacts})
	return nil
}

func (s *Server) handleUploadArtifact(w http.ResponseWriter, r *http.Request) error {
	releaseID := r.PathValue("id")
	// 内容制品与 (os,arch) 无关，而路径里不能出现空段，所以用 any 当哨兵：
	// artifacts/<slot>/any/any 落到服务端就是「这份制品不挑平台」。
	slot := strings.TrimSpace(r.PathValue("slot"))
	platformOS := platformSegment(r.PathValue("os"))
	arch := platformSegment(r.PathValue("arch"))
	filename := r.Header.Get("X-Filename")
	expectedSHA := strings.TrimSpace(r.Header.Get("X-Sha256"))
	artifact, err := s.service.UploadArtifact(releaseID, slot, platformOS, arch, filename, expectedSHA, r.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, artifact)
	return nil
}

// platformSegment 把平台路径段归一化：any（或空）表示与平台无关，其余原样返回。
func platformSegment(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "any") {
		return ""
	}
	return value
}
func (s *Server) handlePublishRelease(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	releaseValue, err := s.store.GetRelease(id)
	if err != nil {
		return err
	}
	if err := s.service.PointChannel(releaseValue.Component, releaseValue.Channel, id, actorOf(r)); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"releaseId": id, "status": "published"})
	return nil
}

func (s *Server) handleYankRelease(w http.ResponseWriter, r *http.Request) error {
	releaseValue, err := s.service.YankRelease(r.PathValue("id"), actorOf(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, releaseValue)
	return nil
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) error {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%w: invalid limit %q", release.ErrValidation, raw)
		}
		limit = parsed
	}
	entries, err := s.store.ListAudit(limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	return nil
}

// handleBuildPlan 接收全量库存，按渠道基线与策略计算升级计划。
func (s *Server) handleBuildPlan(w http.ResponseWriter, r *http.Request) error {
	var inventory model.Inventory
	if err := decodeJSON(r, &inventory); err != nil {
		return err
	}
	plan, err := s.service.BuildPlan(inventory)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, plan)
	return nil
}

// handleReportInventory 记录单个目标的组件库存。
func (s *Server) handleReportInventory(w http.ResponseWriter, r *http.Request) error {
	var target model.TargetInventory
	if err := decodeJSON(r, &target); err != nil {
		return err
	}
	target.ID = r.PathValue("id")
	if err := s.service.RecordInventory(target); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"targetId": target.ID, "status": "recorded"})
	return nil
}

func (s *Server) handleListInventory(w http.ResponseWriter, _ *http.Request) error {
	targets, err := s.service.ListInventory()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
	return nil
}

// setIgnoreRequest 是「忽略此版本」的请求体；目标 id 走路径参数。
type setIgnoreRequest struct {
	Component string `json:"component"`
	Version   string `json:"version"`
}

// handleSetIgnore 记录「这个目标忽略这个组件的这个版本」。
func (s *Server) handleSetIgnore(w http.ResponseWriter, r *http.Request) error {
	var request setIgnoreRequest
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	ignore, err := s.service.SetIgnore(model.Ignore{
		TargetID:  r.PathValue("id"),
		Component: request.Component,
		Version:   request.Version,
	}, actorOf(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ignore)
	return nil
}

// reportResultRequest 是目标端回传的执行结果；目标 id 走路径参数，id 与时间戳由服务端补。
type reportResultRequest struct {
	Action    string `json:"action"`
	Component string `json:"component"`
	From      string `json:"from"`
	Message   string `json:"message"`
	PlanID    string `json:"planId"`
	ReleaseID string `json:"releaseId"`
	State     string `json:"state"`
	To        string `json:"to"`
}

// handleReportResult 记录目标端回传的一次组件升级结果。客户端作用域：结果只描述目标自己
// 这台机器上发生了什么，写错也污染不了别人的记录。
func (s *Server) handleReportResult(w http.ResponseWriter, r *http.Request) error {
	var request reportResultRequest
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	result, err := s.service.RecordResult(model.UpgradeResult{
		TargetID:  r.PathValue("id"),
		Component: request.Component,
		Action:    request.Action,
		From:      request.From,
		To:        request.To,
		State:     request.State,
		Message:   request.Message,
		PlanID:    request.PlanID,
		ReleaseID: request.ReleaseID,
	}, actorOf(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, result)
	return nil
}

// handleListResults 列出升级结果（管理面只读）；target 为空表示全部，limit 默认 200。
func (s *Server) handleListResults(w http.ResponseWriter, r *http.Request) error {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%w: invalid limit %q", release.ErrValidation, raw)
		}
		limit = parsed
	}
	results, err := s.service.ListResults(r.URL.Query().Get("target"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
	return nil
}

// handleClearIgnore 撤销「忽略此版本」，让该版本重新回到升级计划里。
func (s *Server) handleClearIgnore(w http.ResponseWriter, r *http.Request) error {
	if err := s.service.ClearIgnore(r.PathValue("id"), r.PathValue("component"), actorOf(r)); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
	return nil
}

// handleListIgnores 列出忽略集合；带 ?target= 时只列该目标。
func (s *Server) handleListIgnores(w http.ResponseWriter, r *http.Request) error {
	ignores, err := s.service.ListIgnores(r.URL.Query().Get("target"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ignores": ignores})
	return nil
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) error {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%w: invalid limit %q", release.ErrValidation, raw)
		}
		limit = parsed
	}
	plans, err := s.service.ListPlans(limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
	return nil
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) error {
	plan, err := s.service.GetPlan(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plan)
	return nil
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) error {
	policies, err := s.service.ListPolicies(r.URL.Query().Get("channel"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
	return nil
}

// handleSetPolicy 写入 (component, channel) 的分发策略，路径参数优先于请求体。
func (s *Server) handleSetPolicy(w http.ResponseWriter, r *http.Request) error {
	var policyValue model.Policy
	if err := decodeJSON(r, &policyValue); err != nil {
		return err
	}
	policyValue.Component = r.PathValue("component")
	policyValue.Channel = r.PathValue("channel")
	saved, err := s.service.SetPolicy(policyValue, actorOf(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, saved)
	return nil
}

func actorOf(r *http.Request) string {
	if actor := strings.TrimSpace(r.Header.Get("X-Actor")); actor != "" {
		return actor
	}
	return "api"
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid json body: %v", release.ErrValidation, err)
	}
	return nil
}
