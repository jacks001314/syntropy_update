// Package model 定义升级服务的领域类型。
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Target kinds. 库存目标的类型：桌面端、主节点、工作节点。桌面端是整条渠道 BOM 的落地端
// （它要把每个组件的制品拉到本机，再决定谁生效、给节点重下发），主节点与工作节点则各承载
// 自己那一部分；计划引擎按这个区分「整条基线」与「只认上报的组件」。
const (
	TargetKindDesktop = "desktop"
	TargetKindMaster  = "master"
	TargetKindNode    = "node"
)

// Component kinds.
const (
	KindBinary    = "binary"
	KindArchive   = "archive"
	KindInstaller = "installer"
	KindContent   = "content"
)

// Versioning modes.
const (
	VersioningProduct     = "product"
	VersioningIndependent = "independent"
)

// Approval policies.
const (
	ApprovalAuto    = "auto"
	ApprovalConfirm = "confirm"
	ApprovalManual  = "manual"
)

// Channels.
const (
	ChannelAlpha  = "alpha"
	ChannelBeta   = "beta"
	ChannelStable = "stable"
)

// Component 是受管组件。
type Component struct {
	Name              string `json:"name"`
	DisplayName       string `json:"displayName"`
	Kind              string `json:"kind"`
	Versioning        string `json:"versioning"`
	MinProductVersion string `json:"minProductVersion,omitempty"`
	CreatedAtMS       int64  `json:"createdAtMs"`
	UpdatedAtMS       int64  `json:"updatedAtMs"`
}

// Artifact 是某次发布中面向 (os, arch) 的制品。
type Artifact struct {
	ID          string `json:"id"`
	ReleaseID   string `json:"releaseId"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	BlobSHA256  string `json:"blobSha256"`
	CreatedAtMS int64  `json:"createdAtMs"`
}

// KillSwitch 表示一份带有效期的熔断指令。
type KillSwitch struct {
	ExpiresAt          string `json:"expiresAt"`
	MinimumSafeVersion string `json:"minimumSafeVersion"`
}

// Release 是某组件在某渠道上的一次发布。
type Release struct {
	ID                      string      `json:"id"`
	Component               string      `json:"component"`
	Version                 string      `json:"version"`
	Channel                 string      `json:"channel"`
	Notes                   string      `json:"notes,omitempty"`
	NotesURL                string      `json:"notesUrl,omitempty"`
	MinimumSupportedVersion string      `json:"minimumSupportedVersion,omitempty"`
	KillSwitch              *KillSwitch `json:"killSwitch,omitempty"`
	PublishedAtMS           int64       `json:"publishedAtMs"`
	CreatedAtMS             int64       `json:"createdAtMs"`
	Yanked                  bool        `json:"yanked"`
}

// ChannelPointer 记录 (component, channel) 当前指向的发布。
type ChannelPointer struct {
	Component   string `json:"component"`
	Channel     string `json:"channel"`
	ReleaseID   string `json:"releaseId"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

// DistributionComponent 是分发定义中锁定的一个组件版本。
type DistributionComponent struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ReleaseID string `json:"releaseId"`
}

// Distribution 是某产品版本在某渠道上的组件清单（BOM）。
type Distribution struct {
	ProductVersion string                  `json:"productVersion"`
	Channel        string                  `json:"channel"`
	Components     []DistributionComponent `json:"components"`
	PublishedAtMS  int64                   `json:"publishedAtMs"`
	CreatedAtMS    int64                   `json:"createdAtMs"`
}

// PlanItem actions.
const (
	ActionInstall   = "install"
	ActionUpgrade   = "upgrade"
	ActionDowngrade = "downgrade"
	ActionSkip      = "skip"
)

// PlanItem states.
const (
	StatePending  = "pending"
	StateDeferred = "deferred"
	StateBlocked  = "blocked"
)

// Policy 是 (component, channel) 的分发策略。
type Policy struct {
	Component  string `json:"component"`
	Channel    string `json:"channel"`
	Approval   string `json:"approval"`
	Percentage int    `json:"percentage"`
	// Window 是维护窗口 "HH:MM-HH:MM"（UTC），为空表示不限。
	Window string `json:"window,omitempty"`
	// Targets 是按目标 ID 的 glob 白名单，为空表示全部目标。
	Targets     []string `json:"targets,omitempty"`
	UpdatedAtMS int64    `json:"updatedAtMs"`
}

// TargetInventory 是一个受管对象（桌面/本地 Master/远端 Node）的组件库存。
type TargetInventory struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind"`
	Labels         map[string]string `json:"labels,omitempty"`
	Components     map[string]string `json:"components"`
	ProductVersion string            `json:"productVersion,omitempty"`
	ReportedAtMS   int64             `json:"reportedAtMs"`
}

// Inventory 是一次计划计算提交的全量库存。
type Inventory struct {
	Channel      string            `json:"channel"`
	Targets      []TargetInventory `json:"targets"`
	ReceivedAtMS int64             `json:"receivedAtMs"`
}

// Ignore 记录某个目标对某组件某版本的「忽略」意愿（对应 Codex app 里
// `~/.codex/version.json` 的 dismissed_version，落在每个目标自己身上）。
// 只记一个版本：渠道上出现更新的版本时忽略自然失效，所以不需要过期时间。
type Ignore struct {
	TargetID    string `json:"targetId"`
	Component   string `json:"component"`
	Version     string `json:"version"`
	Actor       string `json:"actor,omitempty"`
	CreatedAtMS int64  `json:"createdAtMs"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

// PlanItem 是升级计划中的一步。
type PlanItem struct {
	TargetID   string `json:"targetId"`
	TargetKind string `json:"targetKind"`
	Component  string `json:"component"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Action     string `json:"action"`
	State      string `json:"state"`
	Approval   string `json:"approval"`
	Reason     string `json:"reason,omitempty"`
	// Ignored 表示这一项是目标自己忽略的版本（而不是被策略或灰度拦下的）。
	Ignored   bool   `json:"ignored,omitempty"`
	ReleaseID string `json:"releaseId,omitempty"`
	Bucket    int    `json:"bucket"`
}

// Plan 是一次计划计算的结果。
type Plan struct {
	ID          string     `json:"id"`
	Channel     string     `json:"channel"`
	CreatedAtMS int64      `json:"createdAtMs"`
	Items       []PlanItem `json:"items"`
}

// Upgrade result states（目标端回传的执行结果）。
const (
	// ResultInstalled 表示这一步真的换成了新版本（含内容制品解包与节点制品落盘）。
	ResultInstalled = "installed"
	// ResultHandedOff 表示桌面端已把控制权交给平台安装器，之后由安装器接管。
	ResultHandedOff = "handed-off"
	// ResultFailed 表示这一步失败，原因写在 message 里。
	ResultFailed = "failed"
	// ResultUnsupported 表示本机不支持安装这个组件，没有动手。
	ResultUnsupported = "unsupported"
)

// UpgradeResult 是目标端回传的一次组件升级执行结果：库存回答「现在是什么版本」，
// 结果回答「这次升级成没成、为什么」。桌面端安装完（或失败）后逐条上报，
// 运维由此判断某台机器是不是真的升上去了——也是将来自动回滚的判据。
type UpgradeResult struct {
	ID        string `json:"id"`
	TargetID  string `json:"targetId"`
	Component string `json:"component"`
	Action    string `json:"action,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	PlanID    string `json:"planId,omitempty"`
	ReleaseID string `json:"releaseId,omitempty"`
	AtMS      int64  `json:"atMs"`
}

// AuditEntry 记录一次管理动作。
type AuditEntry struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Actor   string `json:"actor,omitempty"`
	Subject string `json:"subject,omitempty"`
	Detail  string `json:"detail,omitempty"`
	TraceID string `json:"traceId,omitempty"`
	AtMS    int64  `json:"atMs"`
}

// NowMS 返回当前时间的毫秒时间戳。
func NowMS() int64 { return time.Now().UTC().UnixMilli() }

// NewID 生成带前缀的 ID，例如 release-1a2b3c4d。
func NewID(prefix string) string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand 失败时退化为时间戳，保证调用方仍能拿到足够唯一的 ID。
		return fmt.Sprintf("%s-%x", prefix, time.Now().UTC().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buffer)
}
