package release

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"syntropy_update/internal/model"
)

func planFor(t *testing.T, fixture *fixture, channel string, targets ...model.TargetInventory) model.Plan {
	t.Helper()
	plan, err := fixture.service.BuildPlan(model.Inventory{Channel: channel, Targets: targets})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return plan
}

func findItem(plan model.Plan, targetID, component string) (model.PlanItem, bool) {
	for _, item := range plan.Items {
		if item.TargetID == targetID && item.Component == component {
			return item, true
		}
	}
	return model.PlanItem{}, false
}

func TestBuildPlanDetectsDrift(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID:         "node-a",
		Kind:       "node",
		Components: map[string]string{"mbot": "1.1.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok {
		t.Fatalf("expected plan item, got %+v", plan.Items)
	}
	if item.Action != model.ActionUpgrade || item.State != model.StatePending || item.From != "1.1.0" || item.To != "1.2.0" {
		t.Fatalf("item = %+v", item)
	}
	if item.Approval != model.ApprovalConfirm {
		t.Fatalf("approval = %q, want confirm", item.Approval)
	}
	if item.ReleaseID != saved.ID {
		t.Fatalf("releaseId = %q", item.ReleaseID)
	}
}

func TestBuildPlanInstallUpgradeAndNoop(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))
	fixture.publishDistribution(t, "stable", "mbot")

	// 用 desktop 目标来测「全新 / 一致 / 更新」三种动作：它是整条基线都参与计划的目标
	// （其余目标只认自己上报的组件，见 TestBuildPlanScopesComponentsToTargetKind）。
	plan := planFor(t, fixture, "stable",
		model.TargetInventory{ID: "target-fresh", Kind: model.TargetKindDesktop, Components: map[string]string{}},
		model.TargetInventory{ID: "target-current", Kind: model.TargetKindDesktop, Components: map[string]string{"mbot": "1.2.0"}},
		model.TargetInventory{ID: "target-newer", Kind: model.TargetKindDesktop, Components: map[string]string{"mbot": "1.3.0"}},
	)
	if item, ok := findItem(plan, "target-fresh", "mbot"); !ok || item.Action != model.ActionInstall {
		t.Fatalf("fresh item = %+v ok=%v", item, ok)
	}
	if _, ok := findItem(plan, "target-current", "mbot"); ok {
		t.Fatal("up-to-date target should not appear in the plan")
	}
	if item, ok := findItem(plan, "target-newer", "mbot"); !ok || item.Action != model.ActionDowngrade {
		t.Fatalf("newer item = %+v ok=%v", item, ok)
	}
}

// TestBuildPlanScopesComponentsToTargetKind：工作节点库存回传后，一个只报了 nbot 的节点只能拿到
// nbot 的计划项。给它计划 mbot / app-server 只会往计划页里灌它根本不承载的条目。
func TestBuildPlanScopesComponentsToTargetKind(t *testing.T) {
	fixture := newFixture(t)
	for _, component := range []string{"mbot", "nbot"} {
		saved := fixture.publish(t, model.Release{Component: component, Version: "1.2.0", Channel: "stable"})
		fixture.upload(t, saved.ID, "linux", "amd64", []byte(component))
	}
	fixture.publishDistribution(t, "stable", "mbot", "nbot")

	plan := planFor(t, fixture, "stable",
		model.TargetInventory{ID: "desktop", Kind: model.TargetKindDesktop, Components: map[string]string{"desktop": "1.0.0"}},
		model.TargetInventory{ID: "node-1", Kind: model.TargetKindNode, Components: map[string]string{"nbot": "1.1.0"}},
	)

	// 节点：只计划它上报过的 nbot，mbot 不进它的计划。
	if item, ok := findItem(plan, "node-1", "nbot"); !ok || item.Action != model.ActionUpgrade || item.To != "1.2.0" {
		t.Fatalf("node nbot item = %+v ok=%v", item, ok)
	}
	if item, ok := findItem(plan, "node-1", "mbot"); ok {
		t.Fatalf("node should not be planned mbot, got %+v", item)
	}

	// 桌面端：整条基线都参与，mbot 与 nbot 都在（它要把制品拉到本机再决定谁生效）。
	for _, component := range []string{"mbot", "nbot"} {
		if _, ok := findItem(plan, "desktop", component); !ok {
			t.Fatalf("desktop should be planned %s", component)
		}
	}
}

// TestBuildPlanNodeWithoutReportedComponents：一个没报任何组件的工作节点不进计划（它的状态还没有
// 可核对的事实），而不是被当成「全新目标」把整条基线都灌进来。
func TestBuildPlanNodeWithoutReportedComponents(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	plan := planFor(t, fixture, "stable",
		model.TargetInventory{ID: "node-1", Kind: model.TargetKindNode, Components: map[string]string{}},
	)
	if len(plan.Items) != 0 {
		t.Fatalf("plan items = %+v, want none", plan.Items)
	}
}

func TestBuildPlanRespectsPercentage(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))
	if _, err := fixture.service.SetPolicy(model.Policy{Component: "mbot", Channel: "stable", Approval: model.ApprovalAuto, Percentage: 0}, "test"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok || item.State != model.StateDeferred {
		t.Fatalf("item = %+v ok=%v, want deferred", item, ok)
	}
}

func TestBuildPlanRespectsWindowAndTargetsAndCompatibility(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.EnsureComponent(model.Component{Name: "mbot", Kind: model.KindBinary, MinProductVersion: "1.0.0"}); err != nil {
		t.Fatalf("ensure component: %v", err)
	}
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	// 选一个必然不覆盖当前 UTC 时刻的窗口。
	hour := (time.Now().UTC().Hour() + 1) % 24
	window := fmt.Sprintf("%02d:00-%02d:00", hour, (hour+1)%24)
	if _, err := fixture.service.SetPolicy(model.Policy{Component: "mbot", Channel: "stable", Approval: model.ApprovalAuto, Percentage: 100, Window: window}, "test"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	plan := planFor(t, fixture, "stable",
		model.TargetInventory{ID: "node-a", Kind: "node", ProductVersion: "1.2.0", Components: map[string]string{"mbot": "1.1.0"}},
		model.TargetInventory{ID: "node-old", Kind: "node", ProductVersion: "0.9.0", Components: map[string]string{"mbot": "1.1.0"}},
	)
	if item, _ := findItem(plan, "node-a", "mbot"); item.State != model.StateDeferred {
		t.Fatalf("window item = %+v, want deferred", item)
	}
	if item, _ := findItem(plan, "node-old", "mbot"); item.State != model.StateBlocked {
		t.Fatalf("compat item = %+v, want blocked", item)
	}

	// 目标白名单把 node-a 排除掉。
	if _, err := fixture.service.SetPolicy(model.Policy{Component: "mbot", Channel: "stable", Approval: model.ApprovalAuto, Percentage: 100, Targets: []string{"node-other"}}, "test"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	plan = planFor(t, fixture, "stable", model.TargetInventory{ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"}})
	if item, _ := findItem(plan, "node-a", "mbot"); item.State != model.StateBlocked || item.Action != model.ActionSkip {
		t.Fatalf("target filter item = %+v, want blocked skip", item)
	}
}

func TestBuildPlanPersistsInventoryAndPlan(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))
	plan := planFor(t, fixture, "stable", model.TargetInventory{ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"}})

	stored, err := fixture.service.GetPlan(plan.ID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if len(stored.Items) != len(plan.Items) {
		t.Fatalf("stored plan = %+v", stored)
	}
	inventory, err := fixture.service.ListInventory()
	if err != nil {
		t.Fatalf("list inventory: %v", err)
	}
	if len(inventory) != 1 || inventory[0].ID != "node-a" {
		t.Fatalf("inventory = %+v", inventory)
	}
	plans, err := fixture.service.ListPlans(10)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestSetPolicyValidation(t *testing.T) {
	fixture := newFixture(t)
	cases := []model.Policy{
		{Component: "mbot", Channel: "nightly", Approval: model.ApprovalAuto},
		{Component: "mbot", Channel: "stable", Approval: "sometimes"},
		{Component: "mbot", Channel: "stable", Approval: model.ApprovalAuto, Percentage: 101},
		{Component: "mbot", Channel: "stable", Approval: model.ApprovalAuto, Window: "nope"},
		{Component: "", Channel: "stable", Approval: model.ApprovalAuto},
	}
	for index, policyValue := range cases {
		if _, err := fixture.service.SetPolicy(policyValue, "test"); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", index, policyValue)
		}
	}
}

func TestDefaultApprovalDependsOnKind(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.EnsureComponent(model.Component{Name: "skills-bundle", Kind: model.KindContent, Versioning: model.VersioningIndependent}); err != nil {
		t.Fatalf("ensure component: %v", err)
	}
	contentPolicy, err := fixture.service.GetPolicy("skills-bundle", "stable")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if contentPolicy.Approval != model.ApprovalAuto {
		t.Fatalf("content approval = %q, want auto", contentPolicy.Approval)
	}
	binaryPolicy, err := fixture.service.GetPolicy("mbot", "stable")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if binaryPolicy.Approval != model.ApprovalConfirm {
		t.Fatalf("binary approval = %q, want confirm", binaryPolicy.Approval)
	}
}

// futureRFC3339 生成一个还没过期的熔断时间戳，口径与服务端校验一致。
func futureRFC3339() string {
	return time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
}

// TestBuildPlanKillSwitchHaltsKilledRelease：熔断生效期间，低于最低安全版本的发布不许再
// 发出去——灰度、审批、忽略都拦在这条线后面。
func TestBuildPlanKillSwitchHaltsKilledRelease(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{
		Component: "mbot", Version: "1.2.0", Channel: "stable",
		KillSwitch: &model.KillSwitch{ExpiresAt: futureRFC3339(), MinimumSafeVersion: "1.3.0"},
	})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok {
		t.Fatalf("expected plan item, got %+v", plan.Items)
	}
	if item.State != model.StateBlocked || item.Action != model.ActionSkip {
		t.Fatalf("item = %+v, want blocked/skip", item)
	}
	if !strings.Contains(item.Reason, "熔断") {
		t.Fatalf("reason = %q, want a kill-switch explanation", item.Reason)
	}
}

// TestBuildPlanKillSwitchForcesUnsafeTarget：熔断生效且目标当前版本低于下限时，必须立刻
// 升级：目标自己的忽略与 0% 灰度都不再拦。
func TestBuildPlanKillSwitchForcesUnsafeTarget(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{
		Component: "mbot", Version: "1.3.0", Channel: "stable",
		KillSwitch: &model.KillSwitch{ExpiresAt: futureRFC3339(), MinimumSafeVersion: "1.2.0"},
	})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))
	if _, err := fixture.service.SetPolicy(model.Policy{
		Component: "mbot", Channel: "stable", Approval: model.ApprovalConfirm, Percentage: 0,
	}, "test"); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if _, err := fixture.service.SetIgnore(model.Ignore{
		TargetID: "node-a", Component: "mbot", Version: "1.3.0",
	}, "test"); err != nil {
		t.Fatalf("set ignore: %v", err)
	}

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok {
		t.Fatalf("expected plan item, got %+v", plan.Items)
	}
	if item.State != model.StatePending || item.Action != model.ActionUpgrade {
		t.Fatalf("item = %+v, want pending/upgrade", item)
	}
	if item.Approval != model.ApprovalAuto {
		t.Fatalf("approval = %q, want auto (a safety fix must not wait for a human)", item.Approval)
	}
	if !strings.Contains(item.Reason, "熔断") {
		t.Fatalf("reason = %q, want a kill-switch explanation", item.Reason)
	}
}

// TestBuildPlanExpiredKillSwitchDoesNotBlock：过期熔断不再拦，走正常灰度与审批。
func TestBuildPlanExpiredKillSwitchDoesNotBlock(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{
		Component: "mbot", Version: "1.2.0", Channel: "stable",
		KillSwitch: &model.KillSwitch{
			ExpiresAt:          time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			MinimumSafeVersion: "1.3.0",
		},
	})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok || item.State != model.StatePending || item.Action != model.ActionUpgrade {
		t.Fatalf("item = %+v ok=%v, want a normal pending upgrade", item, ok)
	}
	if item.Approval != model.ApprovalConfirm {
		t.Fatalf("approval = %q, want confirm", item.Approval)
	}
}

// TestBuildPlanMinimumSupportedVersionForcesUpdate：最低支持版本是一道长期下限，命中就
// 不再等灰度与维护窗口。
func TestBuildPlanMinimumSupportedVersionForcesUpdate(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{
		Component: "mbot", Version: "1.2.0", Channel: "stable", MinimumSupportedVersion: "1.1.0",
	})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))
	if _, err := fixture.service.SetPolicy(model.Policy{
		Component: "mbot", Channel: "stable", Approval: model.ApprovalConfirm, Percentage: 0,
	}, "test"); err != nil {
		t.Fatalf("set policy: %v", err)
	}

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.0.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok {
		t.Fatalf("expected plan item, got %+v", plan.Items)
	}
	if item.State != model.StatePending || item.Approval != model.ApprovalAuto {
		t.Fatalf("item = %+v, want pending with auto approval", item)
	}
	if !strings.Contains(item.Reason, "最低支持版本") {
		t.Fatalf("reason = %q", item.Reason)
	}
}

// TestBuildPlanAboveSafetyFloorKeepsNormalGates：目标已经在线以上时，熔断不额外加戏。
func TestBuildPlanAboveSafetyFloorKeepsNormalGates(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{
		Component: "mbot", Version: "1.4.0", Channel: "stable",
		KillSwitch: &model.KillSwitch{ExpiresAt: futureRFC3339(), MinimumSafeVersion: "1.2.0"},
	})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	plan := planFor(t, fixture, "stable", model.TargetInventory{
		ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.3.0"},
	})
	item, ok := findItem(plan, "node-a", "mbot")
	if !ok || item.State != model.StatePending || item.Approval != model.ApprovalConfirm {
		t.Fatalf("item = %+v ok=%v, want an ordinary upgrade", item, ok)
	}
	if item.Reason != "" {
		t.Fatalf("reason = %q, want empty", item.Reason)
	}
}
