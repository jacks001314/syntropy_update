// Command syntropy-update 是 Syntropy 升级服务的入口。
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/acme/autocert"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"syntropy_update/internal/api"
	"syntropy_update/internal/auth"
	"syntropy_update/internal/blob"
	"syntropy_update/internal/bundle"
	"syntropy_update/internal/config"
	"syntropy_update/internal/model"
	"syntropy_update/internal/release"
	"syntropy_update/internal/sign"
	"syntropy_update/internal/store"
)

const defaultConfigPath = "conf/update.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	command := os.Args[1]
	var err error
	switch command {
	case "serve":
		err = runServe(os.Args[2:])
	case "keygen":
		err = runKeygen(os.Args[2:])
	case "pubkey":
		err = runPubkey(os.Args[2:])
	case "publish":
		err = runPublish(os.Args[2:])
	case "import-bundle":
		err = runImportBundle(os.Args[2:])
	case "distribution":
		err = runDistribution(os.Args[2:])
	case "manifest":
		err = runManifest(os.Args[2:])
	case "plan":
		err = runPlan(os.Args[2:])
	case "inventory":
		err = runInventory(os.Args[2:])
	case "results":
		err = runResults(os.Args[2:])
	case "policy":
		err = runPolicy(os.Args[2:])
	case "ignore":
		err = runIgnore(os.Args[2:])
	case "list":
		err = runList(os.Args[2:])
	case "audit":
		err = runAudit(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `syntropy-update <command> [flags]

commands:
  serve     启动升级服务
  keygen    生成 Ed25519 签名密钥
  pubkey    打印签名公钥 PEM
  publish   创建发布并上传制品
  import-bundle  按桌面构建清单一次性灌入发布、制品与 BOM（CI 用）
  distribution  发布/查看分发定义（BOM）
  manifest  打印签名后的更新清单
  plan      提交库存 JSON，计算并保存升级计划
  inventory 查看/上报目标库存
  results   查看/上报组件升级结果
  policy    查看/设置分发策略
  ignore    查看/设置/撤销「忽略此版本」（目标级）
  list      列出组件、发布与渠道指针
  audit     查看审计记录

run "syntropy-update <command> -h" for command flags.
`)
}

func newLogger(cfg config.Config, component string) (*logrus.Logger, error) {
	logger := logrus.New()
	logger.SetLevel(parseLogLevel(cfg.LogLevel))
	logger.SetOutput(os.Stdout)
	if cfg.LogDir != "" {
		if err := os.MkdirAll(cfg.LogDir, 0o750); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
		fileWriter := &lumberjack.Logger{
			Filename:   filepath.Join(cfg.LogDir, component+".service.jsonl"),
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
			Compress:   true,
		}
		logger.SetOutput(io.MultiWriter(os.Stdout, fileWriter))
		logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
	}
	return logger, nil
}

func parseLogLevel(value string) logrus.Level {
	level, err := logrus.ParseLevel(strings.ToLower(strings.TrimSpace(value)))
	if err != nil {
		return logrus.InfoLevel
	}
	return level
}

// deps 是一次命令执行所需的本地依赖。
type deps struct {
	cfg     config.Config
	store   *store.Store
	blobs   *blob.Store
	service *release.Service
}

func loadDeps(configPath string, requireKey bool) (*deps, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	metadata, err := store.Open(cfg.DBFile)
	if err != nil {
		return nil, err
	}
	blobs, err := blob.New(cfg.BlobDir)
	if err != nil {
		_ = metadata.Close()
		return nil, err
	}
	var privateKey ed25519.PrivateKey
	if _, statErr := os.Stat(cfg.SigningKeyFile); statErr == nil {
		privateKey, err = sign.LoadPrivateKeyPEM(cfg.SigningKeyFile)
		if err != nil {
			_ = metadata.Close()
			return nil, err
		}
	} else if requireKey {
		_ = metadata.Close()
		return nil, fmt.Errorf("signing key %s not found; run \"syntropy-update keygen\" first", cfg.SigningKeyFile)
	}
	return &deps{cfg: cfg, store: metadata, blobs: blobs, service: release.New(cfg, metadata, blobs, privateKey)}, nil
}

func (d *deps) Close() { _ = d.store.Close() }

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	autoKeygen := flags.Bool("auto-keygen", true, "generate a signing key when missing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *autoKeygen {
		if _, err := os.Stat(*configPath); err != nil && errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config %s not found", *configPath)
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(cfg.SigningKeyFile); errors.Is(err, os.ErrNotExist) {
			if err := generateKey(cfg.SigningKeyFile, false); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "warning: generated new signing key at %s; publish its public key (syntropy-update pubkey)\n", cfg.SigningKeyFile)
		}
	}
	deps, err := loadDeps(*configPath, true)
	if err != nil {
		return err
	}
	defer deps.Close()
	logger, err := newLogger(deps.cfg, "update")
	if err != nil {
		return err
	}
	tokens := auth.New(deps.cfg.AdminTokens, deps.cfg.ClientTokens, deps.cfg.RequireReadTokens, deps.cfg.RequireDeviceTokens)
	if !tokens.AdminEnabled() {
		logger.Warn("no admin tokens configured; /v1/admin/* is disabled — set adminTokens in the config " +
			"(deploy/install.sh generates one) and restart")
	}
	if !tokens.ClientTokensConfigured() {
		if tokens.DeviceRequired() {
			logger.Warn("no client tokens configured while requireDeviceTokens is on: /v1/plan, inventory, " +
				"ignores and results reject every request — set clientTokens in the config " +
				"(deploy/install.sh generates one) and hand it to the targets")
		} else {
			logger.Warn("no client tokens configured and requireDeviceTokens is off: /v1/plan, inventory, " +
				"ignores and results accept anonymous writes — set clientTokens, or requireDeviceTokens to " +
				"reject anonymous writes outright")
		}
	}
	if tokens.ReadRequired() && !tokens.ClientTokensConfigured() {
		logger.Warn("requireReadTokens is on but clientTokens is empty: /v1/manifest, blobs and " +
			"distributions reject every request, including a thin installer's first bootstrap")
	}
	handler := api.New(deps.service, deps.store, deps.blobs, tokens, logger).Handler()
	server := &http.Server{
		Addr:              deps.cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// 第二个监听器专供公网 HTTPS：80 端口通常进不来，autocert 会走 TLS-ALPN-01，
	// 这条路只要 443 能进就能签发。证书缓存必须落在持久目录（service 开着 PrivateTmp），
	// 否则重启会丢证书并反复签发，很容易撞上 Let's Encrypt 的限流。
	var tlsServer *http.Server
	if len(deps.cfg.TLSDomains) > 0 {
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(deps.cfg.TLSCacheDir),
			HostPolicy: autocert.HostWhitelist(deps.cfg.TLSDomains...),
			Email:      deps.cfg.TLSEmail,
		}
		tlsConfig := manager.TLSConfig()
		tlsConfig.MinVersion = tls.VersionTLS12
		tlsServer = &http.Server{
			Addr:              deps.cfg.TLSAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         tlsConfig,
		}
		go func() {
			if err := tlsServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.WithError(err).Error("tls listener stopped; public https is unavailable")
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if tlsServer != nil {
			_ = tlsServer.Shutdown(shutdownCtx)
		}
	}()
	fields := logrus.Fields{
		"addr":          deps.cfg.Addr,
		"publicBaseURL": deps.cfg.PublicBaseURL,
		"adminUI":       strings.TrimRight(deps.cfg.PublicBaseURL, "/") + "/admin/",
		"adminUILocal":  localAdminURL(deps.cfg.Addr),
		"adminEnabled":  tokens.AdminEnabled(),
	}
	if tlsServer != nil {
		fields["tlsAddr"] = deps.cfg.TLSAddr
		fields["tlsDomains"] = deps.cfg.TLSDomains
		fields["tlsCacheDir"] = deps.cfg.TLSCacheDir
	}
	logger.WithFields(fields).Info("update service listening")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// localAdminURL 把监听地址里的通配地址换成回环地址，运维可以直接点开日志里的管理页链接。
func localAdminURL(addr string) string {
	host := addr
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	} else if strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "[::]:")
	}
	return "http://" + host + "/admin/"
}

func generateKey(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("signing key %s already exists (use -force to overwrite)", path)
	}
	keys, err := sign.GenerateKeys()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := sign.WritePrivateKeyFile(path, keys.Private); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote signing key %s\n", path)
	return nil
}

func runKeygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	out := flags.String("out", "", "output path (defaults to config signingKeyFile)")
	force := flags.Bool("force", false, "overwrite existing key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path := *out
	if path == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		path = cfg.SigningKeyFile
	}
	return generateKey(path, *force)
}

func runPubkey(args []string) error {
	flags := flag.NewFlagSet("pubkey", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	privateKey, err := sign.LoadPrivateKeyPEM(cfg.SigningKeyFile)
	if err != nil {
		return err
	}
	pemBytes, err := sign.MarshalPublicKeyPEM(privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(pemBytes)
	return err
}

type artifactFlag struct {
	specs []artifactSpec
}

type artifactSpec struct {
	os   string
	arch string
	path string
}

func (a *artifactFlag) String() string {
	parts := make([]string, 0, len(a.specs))
	for _, spec := range a.specs {
		parts = append(parts, spec.os+"/"+spec.arch+"="+spec.path)
	}
	return strings.Join(parts, ",")
}

func (a *artifactFlag) Set(value string) error {
	equals := strings.Index(value, "=")
	if equals <= 0 {
		return fmt.Errorf("artifact %q must be os/arch=path", value)
	}
	left, path := value[:equals], value[equals+1:]
	slash := strings.Index(left, "/")
	if slash <= 0 {
		return fmt.Errorf("artifact %q must be os/arch=path", value)
	}
	spec := artifactSpec{os: left[:slash], arch: left[slash+1:], path: path}
	if spec.os == "" || spec.arch == "" || spec.path == "" {
		return fmt.Errorf("artifact %q must be os/arch=path", value)
	}
	a.specs = append(a.specs, spec)
	return nil
}

func runPublish(args []string) error {
	flags := flag.NewFlagSet("publish", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	component := flags.String("component", release.DesktopComponent, "component name")
	kind := flags.String("kind", model.KindBinary, "component kind (binary|archive|installer|content)")
	versioning := flags.String("versioning", model.VersioningProduct, "versioning mode (product|independent)")
	version := flags.String("version", "", "version MAJOR.MINOR.PATCH")
	channel := flags.String("channel", "", "channel (alpha|beta|stable)")
	notes := flags.String("notes", "", "release notes")
	notesURL := flags.String("notes-url", "", "release notes URL")
	minSupported := flags.String("min-supported", "", "minimum supported version")
	killSwitchExpires := flags.String("kill-switch-expires", "", "kill switch expiry RFC3339")
	killSwitchMin := flags.String("kill-switch-min", "", "kill switch minimum safe version")
	noPublish := flags.Bool("no-publish", false, "create the release without moving the channel pointer")
	var artifacts artifactFlag
	flags.Var(&artifacts, "artifact", "artifact as os/arch=path (repeatable)")
	content := flags.String("content", "", "platform-independent content archive (kind=content components only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("-version is required")
	}
	if len(artifacts.specs) == 0 && strings.TrimSpace(*content) == "" {
		return errors.New("at least one -artifact or -content is required")
	}
	if (*killSwitchExpires == "") != (*killSwitchMin == "") {
		return errors.New("-kill-switch-expires and -kill-switch-min must be provided together")
	}
	deps, err := loadDeps(*configPath, true)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	if _, err := deps.service.EnsureComponent(model.Component{
		Name:        *component,
		DisplayName: *component,
		Kind:        *kind,
		Versioning:  *versioning,
	}); err != nil {
		return err
	}
	releaseValue := model.Release{
		Component:               *component,
		Version:                 *version,
		Channel:                 *channel,
		Notes:                   *notes,
		NotesURL:                *notesURL,
		MinimumSupportedVersion: *minSupported,
	}
	if *killSwitchExpires != "" {
		releaseValue.KillSwitch = &model.KillSwitch{ExpiresAt: *killSwitchExpires, MinimumSafeVersion: *killSwitchMin}
	}
	var saved model.Release
	if *noPublish {
		saved, err = deps.service.CreateRelease(releaseValue, "cli")
	} else {
		saved, err = deps.service.PublishRelease(releaseValue, "cli")
	}
	if err != nil {
		return err
	}
	// 内容制品没有 (os,arch)，与平台制品分成两种来源，但走同一段上传代码。
	type upload struct{ label, os, arch, path string }
	uploads := make([]upload, 0, len(artifacts.specs)+1)
	for _, spec := range artifacts.specs {
		uploads = append(uploads, upload{label: spec.os + "/" + spec.arch, os: spec.os, arch: spec.arch, path: spec.path})
	}
	if path := strings.TrimSpace(*content); path != "" {
		uploads = append(uploads, upload{label: "content", path: path})
	}
	for _, spec := range uploads {
		file, err := os.Open(spec.path)
		if err != nil {
			return err
		}
		artifact, uploadErr := deps.service.UploadArtifact(saved.ID, spec.os, spec.arch, filepath.Base(spec.path), "", file)
		closeErr := file.Close()
		if uploadErr != nil {
			return uploadErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Printf("uploaded %s %s (sha256=%s size=%d)\n", spec.label, artifact.Filename, artifact.SHA256, artifact.Size)
	}
	fmt.Printf("release %s %s/%s published=%v\n", saved.ID, saved.Component, saved.Version, !*noPublish)
	return nil
}

// runImportBundle 把一次桌面构建的产物整体灌入服务：读 release-manifest.json，
// 为该产品版本下的每个组件创建发布、上传各 (os,arch) 制品、推进渠道指针，最后发布 BOM。
//
// CI 只需要三件事：清单路径、产物根目录（默认取清单所在目录）、本次要发的桌面端安装器。
// 幂等：同一份输入重跑只跳过；同一版本换了字节则报错（已发布的版本不可变）。
func runImportBundle(args []string) error {
	flags := flag.NewFlagSet("import-bundle", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	manifestPath := flags.String("manifest", "", "release-manifest.json produced by the desktop build (required)")
	assetDir := flags.String("dir", "", "resource root the manifest paths are relative to (defaults to the manifest directory)")
	channel := flags.String("channel", "", "channel (defaults to config defaultChannel)")
	productVersion := flags.String("version", "", "product version MAJOR.MINOR.PATCH (defaults to the manifest productVersion)")
	notes := flags.String("notes", "", "release notes")
	notesURL := flags.String("notes-url", "", "release notes URL")
	actor := flags.String("actor", "ci", "audit actor")
	noPublish := flags.Bool("no-publish", false, "create releases and upload artifacts without moving the channel pointer or publishing the BOM")
	dryRun := flags.Bool("dry-run", false, "print the plan and exit without touching the service")
	var desktop artifactFlag
	flags.Var(&desktop, "desktop", "desktop installer as os/arch=path (repeatable)")
	var componentVersions componentFlag
	flags.Var(&componentVersions, "component-version", "component version override as name=version (repeatable)")
	var contents componentFlag
	flags.Var(&contents, "content", "content artifact as component=path (repeatable, templates/skills)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" {
		return errors.New("-manifest is required")
	}
	encoded, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read release manifest: %w", err)
	}
	manifest, err := bundle.ParseManifest(encoded)
	if err != nil {
		return err
	}
	overrides := make(map[string]string, len(componentVersions.specs))
	for _, spec := range componentVersions.specs {
		overrides[spec.name] = spec.version
	}
	installers := make([]bundle.Artifact, 0, len(desktop.specs))
	for _, spec := range desktop.specs {
		installers = append(installers, bundle.Artifact{
			Arch:     spec.arch,
			Filename: filepath.Base(spec.path),
			OS:       spec.os,
			Path:     spec.path,
		})
	}
	contentArtifacts := make([]bundle.ContentArtifact, 0, len(contents.specs))
	for _, spec := range contents.specs {
		contentArtifacts = append(contentArtifacts, bundle.ContentArtifact{Component: spec.name, Path: spec.version})
	}
	plan, err := bundle.PlanAssets(manifest, bundle.PlanOptions{
		AssetDir:          *assetDir,
		ComponentVersions: overrides,
		Content:           contentArtifacts,
		Desktop:           installers,
		ManifestDir:       filepath.Dir(*manifestPath),
		ProductVersion:    *productVersion,
	})
	if err != nil {
		return err
	}
	if *dryRun {
		printBundlePlan(plan)
		return nil
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	report, err := bundle.Import(deps.service, plan, bundle.ImportOptions{
		Channel:  *channel,
		Actor:    *actor,
		Notes:    *notes,
		NotesURL: *notesURL,
		Publish:  !*noPublish,
	})
	if err != nil {
		return err
	}
	printBundleReport(report)
	return nil
}

func printBundlePlan(plan bundle.Plan) {
	fmt.Printf("plan: product version %s, %d components\n", plan.ProductVersion, len(plan.Components))
	for _, component := range plan.Components {
		fmt.Printf("  %-12s %-10s %s\n", component.Name, component.Version, component.Kind)
		for _, artifact := range component.Artifacts {
			os, arch := artifact.OS, artifact.Arch
			if os == "" && arch == "" {
				os, arch = "content", "-"
			}
			fmt.Printf("      %-8s %-6s %s\n", os, arch, artifact.Path)
		}
	}
	for _, note := range plan.Notes {
		fmt.Printf("  note: %s\n", note)
	}
}

func printBundleReport(report bundle.Report) {
	fmt.Printf("imported channel %s: %s\n", report.Channel, report.Distribution)
	for _, item := range report.Releases {
		state := "reused release"
		if item.Created {
			state = "created release"
		}
		if item.Published {
			// 复用已有发布时指针本来就在那儿，说「moved」会让 CI 日志骗人。
			if item.Created {
				state += ", channel pointer moved"
			} else {
				state += ", channel pointer confirmed"
			}
		}
		fmt.Printf("  %-12s %-10s %s (%s)\n", item.Component, item.Version, item.ReleaseID, state)
		for _, line := range item.Uploaded {
			fmt.Printf("      uploaded %s\n", line)
		}
		for _, line := range item.Skipped {
			fmt.Printf("      skipped  %s\n", line)
		}
	}
	for _, note := range report.Notes {
		fmt.Printf("  note: %s\n", note)
	}
}

func runManifest(args []string) error {
	flags := flag.NewFlagSet("manifest", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	component := flags.String("component", release.DesktopComponent, "component name")
	channel := flags.String("channel", "", "channel (defaults to config defaultChannel)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, true)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	// 桌面自更新清单与集群组件清单是两种载荷；按组件名分派。
	var encoded []byte
	if *component == release.DesktopComponent {
		encoded, err = deps.service.Manifest(*component, *channel)
	} else {
		encoded, err = deps.service.ComponentManifest(*component, *channel)
	}
	if err != nil {
		return err
	}
	var pretty map[string]any
	if err := json.Unmarshal(encoded, &pretty); err == nil {
		formatted, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(formatted))
		return nil
	}
	fmt.Println(string(encoded))
	return nil
}

// printJSON 以缩进形式打印 JSON。
func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// decodeJSONInput 从文件或标准输入解码 JSON 对象，拒绝未知字段。
func decodeJSONInput(path string, target any) error {
	var reader io.Reader
	if path == "" || path == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("parse json input: %w", err)
	}
	return nil
}

// runPlan 提交库存并计算升级计划。
func runPlan(args []string) error {
	flags := flag.NewFlagSet("plan", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	inventoryPath := flags.String("inventory", "-", "inventory JSON file, or - for stdin")
	channel := flags.String("channel", "", "override inventory channel")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var inventory model.Inventory
	if err := decodeJSONInput(*inventoryPath, &inventory); err != nil {
		return err
	}
	if *channel != "" {
		inventory.Channel = *channel
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	plan, err := deps.service.BuildPlan(inventory)
	if err != nil {
		return err
	}
	return printJSON(plan)
}

// runInventory 处理库存相关子命令。
func runInventory(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntropy-update inventory <list|report> [flags]")
	}
	switch args[0] {
	case "list":
		return runInventoryList(args[1:])
	case "report":
		return runInventoryReport(args[1:])
	default:
		return fmt.Errorf("unknown inventory subcommand %q", args[0])
	}
}

func runInventoryList(args []string) error {
	flags := flag.NewFlagSet("inventory list", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	jsonOutput := flags.Bool("json", false, "print raw JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	targets, err := deps.service.ListInventory()
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(targets)
	}
	for _, target := range targets {
		names := make([]string, 0, len(target.Components))
		for name := range target.Components {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, name+"="+target.Components[name])
		}
		fmt.Printf("%-20s %-8s product=%-10s %s\n", target.ID, target.Kind, target.ProductVersion, strings.Join(parts, " "))
	}
	return nil
}

func runInventoryReport(args []string) error {
	flags := flag.NewFlagSet("inventory report", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	targetID := flags.String("target", "", "target id (required)")
	kind := flags.String("kind", "node", "target kind: desktop|master|node")
	productVersion := flags.String("product-version", "", "product version")
	var components componentFlag
	flags.Var(&components, "component", "component as name=version (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *targetID == "" {
		return errors.New("-target is required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	target := model.TargetInventory{
		ID:             *targetID,
		Kind:           *kind,
		ProductVersion: *productVersion,
		Components:     map[string]string{},
	}
	for _, spec := range components.specs {
		target.Components[spec.name] = spec.version
	}
	if err := deps.service.RecordInventory(target); err != nil {
		return err
	}
	fmt.Printf("recorded inventory for %s with %d components\n", target.ID, len(target.Components))
	return nil
}

// runResults 处理「升级结果」相关子命令。结果由目标端在每次组件升级后回传，
// 与库存互补：库存是「现在跑哪一版」，结果是「这一次升级成没成」。
func runResults(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntropy-update results <list|report> [flags]")
	}
	switch args[0] {
	case "list":
		return runResultsList(args[1:])
	case "report":
		return runResultsReport(args[1:])
	default:
		return fmt.Errorf("unknown results subcommand %q", args[0])
	}
}

func runResultsList(args []string) error {
	flags := flag.NewFlagSet("results list", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	target := flags.String("target", "", "target id; empty lists every target")
	limit := flags.Int("limit", 200, "max results to return; <=0 for unlimited")
	jsonOutput := flags.Bool("json", false, "print raw JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	results, err := deps.service.ListResults(*target, *limit)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(map[string]any{"results": results})
	}
	fmt.Println("results:")
	for _, entry := range results {
		fmt.Printf("  %-20s %-16s %-11s %-10s %-10s %s\n",
			entry.TargetID, entry.Component, entry.State, entry.From, entry.To,
			time.UnixMilli(entry.AtMS).UTC().Format(time.RFC3339))
	}
	return nil
}

func runResultsReport(args []string) error {
	flags := flag.NewFlagSet("results report", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	target := flags.String("target", "", "target id (required)")
	component := flags.String("component", "", "component name (required)")
	state := flags.String("state", "", "installed|handed-off|failed|unsupported (required)")
	from := flags.String("from", "", "version before the upgrade")
	to := flags.String("to", "", "version after the upgrade")
	action := flags.String("action", "", "plan action, e.g. install|update")
	message := flags.String("message", "", "short human-readable outcome")
	planID := flags.String("plan", "", "plan id this result belongs to")
	releaseID := flags.String("release", "", "release id this result belongs to")
	actor := flags.String("actor", "cli", "audit actor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *component == "" || *state == "" {
		return errors.New("-target, -component and -state are required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	result, err := deps.service.RecordResult(model.UpgradeResult{
		TargetID:  *target,
		Component: *component,
		State:     *state,
		Action:    *action,
		From:      *from,
		To:        *to,
		Message:   *message,
		PlanID:    *planID,
		ReleaseID: *releaseID,
	}, *actor)
	if err != nil {
		return err
	}
	fmt.Printf("recorded %s for %s: %s %s %s->%s\n", result.ID, result.TargetID, result.Component, result.State, result.From, result.To)
	return nil
}

// runPolicy 处理策略相关子命令。
// runIgnore 管理目标级的「忽略此版本」集合。忽略属于目标自己，不属于 (组件, 渠道) 策略。
func runIgnore(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntropy-update ignore <list|set|clear> [flags]")
	}
	switch args[0] {
	case "list":
		return runIgnoreList(args[1:])
	case "set":
		return runIgnoreSet(args[1:])
	case "clear":
		return runIgnoreClear(args[1:])
	default:
		return fmt.Errorf("unknown ignore subcommand %q", args[0])
	}
}

func runIgnoreList(args []string) error {
	flags := flag.NewFlagSet("ignore list", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	target := flags.String("target", "", "target id; empty lists every target")
	jsonOutput := flags.Bool("json", false, "print raw JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	ignores, err := deps.service.ListIgnores(*target)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(map[string]any{"ignores": ignores})
	}
	fmt.Println("ignores:")
	for _, entry := range ignores {
		fmt.Printf("  %-24s %-16s %-10s %-10s %s\n", entry.TargetID, entry.Component, entry.Version, entry.Actor,
			time.UnixMilli(entry.UpdatedAtMS).UTC().Format(time.RFC3339))
	}
	return nil
}

func runIgnoreSet(args []string) error {
	flags := flag.NewFlagSet("ignore set", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	target := flags.String("target", "", "target id (required)")
	component := flags.String("component", "", "component name (required)")
	version := flags.String("version", "", "version to ignore, MAJOR.MINOR.PATCH (required)")
	actor := flags.String("actor", "cli", "audit actor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *component == "" || *version == "" {
		return errors.New("-target, -component and -version are required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	ignore, err := deps.service.SetIgnore(model.Ignore{
		Component: *component,
		TargetID:  *target,
		Version:   *version,
	}, *actor)
	if err != nil {
		return err
	}
	fmt.Printf("ignored %s@%s for %s\n", ignore.Component, ignore.Version, ignore.TargetID)
	return nil
}

func runIgnoreClear(args []string) error {
	flags := flag.NewFlagSet("ignore clear", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	target := flags.String("target", "", "target id (required)")
	component := flags.String("component", "", "component name (required)")
	actor := flags.String("actor", "cli", "audit actor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *component == "" {
		return errors.New("-target and -component are required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	if err := deps.service.ClearIgnore(*target, *component, *actor); err != nil {
		return err
	}
	fmt.Printf("cleared %s for %s\n", *component, *target)
	return nil
}

func runPolicy(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntropy-update policy <show|set> [flags]")
	}
	switch args[0] {
	case "show":
		return runPolicyShow(args[1:])
	case "set":
		return runPolicySet(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand %q", args[0])
	}
}

func runPolicyShow(args []string) error {
	flags := flag.NewFlagSet("policy show", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	component := flags.String("component", "", "component name; empty lists all")
	channel := flags.String("channel", "", "channel filter (required with -component)")
	jsonOutput := flags.Bool("json", false, "print raw JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *component != "" {
		if *channel == "" {
			return errors.New("-channel is required with -component")
		}
		policyValue, err := deps.service.GetPolicy(*component, *channel)
		if err != nil {
			return err
		}
		return printJSON(policyValue)
	}
	policies, err := deps.service.ListPolicies(*channel)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(policies)
	}
	for _, policyValue := range policies {
		fmt.Printf("%-16s %-8s approval=%-8s percentage=%-3d window=%-11s targets=%s\n",
			policyValue.Component, policyValue.Channel, policyValue.Approval,
			policyValue.Percentage, policyValue.Window, strings.Join(policyValue.Targets, ","))
	}
	return nil
}

func runPolicySet(args []string) error {
	flags := flag.NewFlagSet("policy set", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	component := flags.String("component", "", "component name (required)")
	channel := flags.String("channel", "", "channel (defaults to config defaultChannel)")
	approval := flags.String("approval", "confirm", "approval: auto|confirm|manual")
	percentage := flags.Int("percentage", 100, "rollout percentage 0..100")
	window := flags.String("window", "", "maintenance window HH:MM-HH:MM (UTC)")
	var targets stringListFlag
	flags.Var(&targets, "target", "target id glob (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *component == "" {
		return errors.New("-component is required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	saved, err := deps.service.SetPolicy(model.Policy{
		Component:  *component,
		Channel:    *channel,
		Approval:   *approval,
		Percentage: *percentage,
		Window:     *window,
		Targets:    targets.values,
	}, "cli")
	if err != nil {
		return err
	}
	fmt.Printf("policy %s/%s approval=%s percentage=%d\n", saved.Component, saved.Channel, saved.Approval, saved.Percentage)
	return nil
}

// stringListFlag 收集可重复的字符串参数。
type stringListFlag struct {
	values []string
}

func (f *stringListFlag) String() string { return strings.Join(f.values, ",") }

func (f *stringListFlag) Set(value string) error {
	f.values = append(f.values, strings.TrimSpace(value))
	return nil
}

func runList(args []string) error {
	flags := flag.NewFlagSet("list", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	component := flags.String("component", "", "filter by component")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	components, err := deps.store.ListComponents()
	if err != nil {
		return err
	}
	fmt.Println("components:")
	for _, item := range components {
		fmt.Printf("  %-16s %-10s %s\n", item.Name, item.Kind, item.Versioning)
	}
	releases, err := deps.store.ListReleases(*component)
	if err != nil {
		return err
	}
	fmt.Println("releases:")
	for _, item := range releases {
		state := "active"
		if item.Yanked {
			state = "yanked"
		}
		fmt.Printf("  %-16s %-8s %-8s %s %s\n", item.Component, item.Version, item.Channel, item.ID, state)
	}
	pointers, err := deps.store.ListChannelPointers(*component)
	if err != nil {
		return err
	}
	fmt.Println("channels:")
	for _, pointer := range pointers {
		fmt.Printf("  %-16s %-8s -> %s\n", pointer.Component, pointer.Channel, pointer.ReleaseID)
	}
	return nil
}

func runAudit(args []string) error {
	flags := flag.NewFlagSet("audit", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	limit := flags.Int("limit", 50, "max entries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	entries, err := deps.store.ListAudit(*limit)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Printf("%s %-20s %-24s %s\n", time.UnixMilli(entry.AtMS).UTC().Format(time.RFC3339), entry.Action, entry.Subject, entry.Detail)
	}
	return nil
}

type componentFlag struct {
	specs []componentSpec
}

type componentSpec struct {
	name    string
	version string
}

func (c *componentFlag) String() string {
	parts := make([]string, 0, len(c.specs))
	for _, spec := range c.specs {
		parts = append(parts, spec.name+"="+spec.version)
	}
	return strings.Join(parts, ",")
}

func (c *componentFlag) Set(value string) error {
	equals := strings.Index(value, "=")
	if equals <= 0 || equals == len(value)-1 {
		return fmt.Errorf("%q must be name=value", value)
	}
	c.specs = append(c.specs, componentSpec{name: strings.TrimSpace(value[:equals]), version: strings.TrimSpace(value[equals+1:])})
	return nil
}

func runDistribution(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: syntropy-update distribution <publish|show> [flags]")
	}
	switch args[0] {
	case "publish":
		return runDistributionPublish(args[1:])
	case "show":
		return runDistributionShow(args[1:])
	default:
		return fmt.Errorf("unknown distribution subcommand %q", args[0])
	}
}

func runDistributionPublish(args []string) error {
	flags := flag.NewFlagSet("distribution publish", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	channel := flags.String("channel", "", "channel (alpha|beta|stable)")
	productVersion := flags.String("product-version", "", "product version MAJOR.MINOR.PATCH")
	var components componentFlag
	flags.Var(&components, "component", "component as name=version (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *productVersion == "" {
		return errors.New("-product-version is required")
	}
	if len(components.specs) == 0 {
		return errors.New("at least one -component is required")
	}
	deps, err := loadDeps(*configPath, false)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	distribution := model.Distribution{Channel: *channel, ProductVersion: *productVersion}
	for _, spec := range components.specs {
		distribution.Components = append(distribution.Components, model.DistributionComponent{Name: spec.name, Version: spec.version})
	}
	saved, err := deps.service.PublishDistribution(distribution, "cli")
	if err != nil {
		return err
	}
	fmt.Printf("distribution %s/%s with %d components\n", saved.Channel, saved.ProductVersion, len(saved.Components))
	return nil
}

func runDistributionShow(args []string) error {
	flags := flag.NewFlagSet("distribution show", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "config file path")
	channel := flags.String("channel", "", "channel (defaults to config defaultChannel)")
	productVersion := flags.String("product-version", "", "product version (defaults to channel latest)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	deps, err := loadDeps(*configPath, true)
	if err != nil {
		return err
	}
	defer deps.Close()
	if *channel == "" {
		*channel = deps.cfg.DefaultChannel
	}
	encoded, err := deps.service.SignedDistribution(*channel, *productVersion)
	if err != nil {
		return err
	}
	var pretty map[string]any
	if err := json.Unmarshal(encoded, &pretty); err == nil {
		formatted, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(formatted))
		return nil
	}
	fmt.Println(string(encoded))
	return nil
}
