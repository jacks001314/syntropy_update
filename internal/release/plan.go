package release

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"syntropy_update/internal/model"
	"syntropy_update/internal/policy"
	"syntropy_update/internal/store"
)

// DefaultApprovalFor 返回组件在未配置策略时的默认审批口径：内容制品自动，二进制需确认。
func DefaultApprovalFor(kind string) string {
	if kind == model.KindContent {
		return model.ApprovalAuto
	}
	return model.ApprovalConfirm
}

// SetPolicy 写入 (component, channel) 的分发策略。
func (s *Service) SetPolicy(policyValue model.Policy, actor string) (model.Policy, error) {
	policyValue.Component = strings.TrimSpace(policyValue.Component)
	policyValue.Channel = strings.TrimSpace(policyValue.Channel)
	if policyValue.Component == "" {
		return model.Policy{}, fmt.Errorf("%w: component is required", ErrValidation)
	}
	if !s.cfg.IsChannel(policyValue.Channel) {
		return model.Policy{}, fmt.Errorf("%w: unknown channel %q", ErrValidation, policyValue.Channel)
	}
	if policyValue.Approval == "" {
		policyValue.Approval = model.ApprovalConfirm
	}
	switch policyValue.Approval {
	case model.ApprovalAuto, model.ApprovalConfirm, model.ApprovalManual:
	default:
		return model.Policy{}, fmt.Errorf("%w: approval %q must be auto|confirm|manual", ErrValidation, policyValue.Approval)
	}
	if policyValue.Percentage < 0 || policyValue.Percentage > 100 {
		return model.Policy{}, fmt.Errorf("%w: percentage must be 0..100", ErrValidation)
	}
	if _, err := policy.WithinWindow(policyValue.Window, time.Now()); err != nil {
		return model.Policy{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	policyValue.UpdatedAtMS = model.NowMS()
	if err := s.store.PutPolicy(policyValue); err != nil {
		return model.Policy{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "policy.set",
		Actor:   actor,
		Subject: policyValue.Component + "/" + policyValue.Channel,
		Detail:  fmt.Sprintf("approval=%s percentage=%d window=%s", policyValue.Approval, policyValue.Percentage, policyValue.Window),
	}); err != nil {
		return model.Policy{}, err
	}
	return policyValue, nil
}

// GetPolicy 读取策略；未配置时返回带默认值的策略。
func (s *Service) GetPolicy(component, channel string) (model.Policy, error) {
	kind := model.KindBinary
	if definition, err := s.store.GetComponent(component); err == nil && definition.Kind != "" {
		kind = definition.Kind
	}
	stored, err := s.store.GetPolicy(component, channel)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.Policy{}, err
	}
	return model.Policy{
		Component:  component,
		Channel:    channel,
		Approval:   DefaultApprovalFor(kind),
		Percentage: 100,
	}, nil
}

// ListPolicies 列出策略。
func (s *Service) ListPolicies(channel string) ([]model.Policy, error) {
	return s.store.ListPolicies(channel)
}

// RecordInventory 记录单个目标的最新库存。
func (s *Service) RecordInventory(target model.TargetInventory) error {
	target.ID = strings.TrimSpace(target.ID)
	if target.ID == "" {
		return fmt.Errorf("%w: target id is required", ErrValidation)
	}
	if target.Kind == "" {
		target.Kind = "node"
	}
	if target.Components == nil {
		target.Components = map[string]string{}
	}
	target.ReportedAtMS = model.NowMS()
	return s.store.PutInventory(target)
}

// ListInventory 列出所有目标的最新库存。
func (s *Service) ListInventory() ([]model.TargetInventory, error) {
	return s.store.ListInventory()
}

// ListPlans 列出升级计划。
func (s *Service) ListPlans(limit int) ([]model.Plan, error) {
	return s.store.ListPlans(limit)
}

// GetPlan 读取升级计划。
func (s *Service) GetPlan(id string) (model.Plan, error) {
	return s.store.GetPlan(id)
}

// BuildPlan 按渠道基线与策略计算升级计划，并记录本次库存。
func (s *Service) BuildPlan(inventory model.Inventory) (model.Plan, error) {
	channel := strings.TrimSpace(inventory.Channel)
	if channel == "" {
		channel = s.cfg.DefaultChannel
	}
	if !s.cfg.IsChannel(channel) {
		return model.Plan{}, fmt.Errorf("%w: unknown channel %q", ErrValidation, channel)
	}
	if len(inventory.Targets) == 0 {
		return model.Plan{}, fmt.Errorf("%w: inventory has no targets", ErrValidation)
	}

	// 渠道基线是「这条渠道上有什么」，不等于「每个目标都要装什么」。桌面端是整条 BOM 的落地端
	// （制品都要拉到本机，再由它决定换哪一份、给节点重下发），所以整条基线都参与它的计划；
	// 其余目标（主节点 / 工作节点）只认它自己上报的组件——给一台工作节点计划 mbot 或
	// app-server，只会往计划里灌它根本不承载的条目，而「这台机器上到底有什么」本来就只有它
	// 自己报的库存说得清。
	baseline := map[string]struct{}{}
	if distribution, err := s.LatestDistribution(channel); err == nil {
		for _, component := range distribution.Components {
			baseline[component.Name] = struct{}{}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Plan{}, err
	}
	baselineNames := make([]string, 0, len(baseline))
	for name := range baseline {
		baselineNames = append(baselineNames, name)
	}
	sort.Strings(baselineNames)

	targets := append([]model.TargetInventory(nil), inventory.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })

	now := time.Now().UTC()
	items := make([]model.PlanItem, 0)
	for _, target := range targets {
		if err := s.RecordInventory(target); err != nil {
			return model.Plan{}, err
		}
		// 忽略集合按目标取一次，别在组件循环里反复开事务。
		ignored, err := s.ignoredVersions(target.ID)
		if err != nil {
			return model.Plan{}, err
		}
		for _, name := range targetComponentNames(target, baselineNames) {
			item, include, err := s.planItem(target, name, channel, now, ignored[name])
			if err != nil {
				return model.Plan{}, err
			}
			if include {
				items = append(items, item)
			}
		}
	}

	plan := model.Plan{
		ID:          model.NewID("plan"),
		Channel:     channel,
		CreatedAtMS: model.NowMS(),
		Items:       items,
	}
	if err := s.store.PutPlan(plan); err != nil {
		return model.Plan{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "plan.build",
		Subject: plan.ID,
		Detail:  fmt.Sprintf("channel=%s items=%d", channel, len(items)),
		TraceID: plan.ID,
	}); err != nil {
		return model.Plan{}, err
	}
	return plan, nil
}

// targetComponentNames 决定一个目标要参与哪些组件的计划：桌面端拿整条渠道基线，其余目标只拿
// 它自己上报的组件。返回排序后的切片，保证同一份库存算出的计划是稳定的。
func targetComponentNames(target model.TargetInventory, baseline []string) []string {
	names := map[string]struct{}{}
	if target.Kind == model.TargetKindDesktop {
		for _, name := range baseline {
			names[name] = struct{}{}
		}
	}
	for name := range target.Components {
		names[name] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted
}

// KillSwitchActive 判断熔断指令是否还在有效期内。ExpiresAt 读不懂时按「没生效」处理：
// 一份解析不了的指令不该让整条渠道停摆，但它也不算一道有效的安全线。
func KillSwitchActive(killSwitch *model.KillSwitch, now time.Time) bool {
	if killSwitch == nil {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(killSwitch.ExpiresAt))
	if err != nil {
		return false
	}
	return expiresAt.After(now)
}

// activeSafetyFloor 返回熔断生效期间的最低安全版本；没生效或没写下限时为空。
func activeSafetyFloor(releaseValue model.Release, now time.Time) string {
	if !KillSwitchActive(releaseValue.KillSwitch, now) {
		return ""
	}
	return strings.TrimSpace(releaseValue.KillSwitch.MinimumSafeVersion)
}

// killedRelease 判断这一版发布是不是已经被熔断叫停（熔断生效中，且要发的这一版低于
// 最低安全版本）。判据落在「发布」上而不是「目标」上：一个低于安全线的版本对谁都不该
// 再发出去，否则熔断只是一纸空文。
func killedRelease(component string, releaseValue model.Release, now time.Time) (bool, string) {
	floor := activeSafetyFloor(releaseValue, now)
	if floor == "" {
		return false, ""
	}
	comparison, err := policy.CompareVersions(releaseValue.Version, floor)
	if err != nil || comparison >= 0 {
		return false, ""
	}
	return true, fmt.Sprintf("熔断生效：%s %s 低于最低安全版本 %s，暂停分发", component, releaseValue.Version, floor)
}

// forcedUpdate 判断目标当前跑的版本是不是已经低于安全下限（熔断下限或最低支持版本）。
// 命中的项必须立即执行：忽略集合、灰度与维护窗口管的是「怎么升」的节奏，不能让已知
// 有风险的版本在目标上多跑一天。
func forcedUpdate(releaseValue model.Release, installed string, now time.Time) (bool, string) {
	if installed == "" {
		return false, ""
	}
	if floor := activeSafetyFloor(releaseValue, now); floor != "" {
		if comparison, err := policy.CompareVersions(installed, floor); err == nil && comparison < 0 {
			return true, fmt.Sprintf("熔断生效：当前 %s 低于最低安全版本 %s，须立即升级", installed, floor)
		}
	}
	if floor := strings.TrimSpace(releaseValue.MinimumSupportedVersion); floor != "" {
		if comparison, err := policy.CompareVersions(installed, floor); err == nil && comparison < 0 {
			return true, fmt.Sprintf("当前 %s 低于最低支持版本 %s，须立即升级", installed, floor)
		}
	}
	return false, ""
}

// planItem 计算单个 (目标, 组件) 的计划项；include 为假表示无需出现在计划里。
// ignoredVersion 是该目标自己忽略的版本（空表示没忽略）。
func (s *Service) planItem(target model.TargetInventory, component, channel string, now time.Time, ignoredVersion string) (model.PlanItem, bool, error) {
	installed := strings.TrimSpace(target.Components[component])
	releaseValue, err := s.activeRelease(component, channel)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if installed == "" {
				return model.PlanItem{}, false, nil
			}
			return model.PlanItem{
				TargetID:   target.ID,
				TargetKind: target.Kind,
				Component:  component,
				From:       installed,
				Action:     model.ActionSkip,
				State:      model.StateBlocked,
				Approval:   model.ApprovalManual,
				Reason:     "渠道缺少可用发布",
			}, true, nil
		}
		return model.PlanItem{}, false, err
	}

	policyValue, err := s.GetPolicy(component, channel)
	if err != nil {
		return model.PlanItem{}, false, err
	}
	item := model.PlanItem{
		TargetID:   target.ID,
		TargetKind: target.Kind,
		Component:  component,
		From:       installed,
		To:         releaseValue.Version,
		Action:     model.ActionUpgrade,
		State:      model.StatePending,
		Approval:   policyValue.Approval,
		ReleaseID:  releaseValue.ID,
	}

	if installed == "" {
		item.Action = model.ActionInstall
	} else {
		comparison, err := policy.CompareVersions(installed, releaseValue.Version)
		if err != nil {
			item.Action = model.ActionUpgrade
			item.Reason = "已装版本无法解析，按升级处理"
		} else {
			switch {
			case comparison < 0:
				item.Action = model.ActionUpgrade
			case comparison > 0:
				item.Action = model.ActionDowngrade
			default:
				return model.PlanItem{}, false, nil
			}
		}
	}

	// 熔断与安全下限在这里执行——签名清单里的 killSwitch / minimumSupportedVersion 只有
	// 落到计划上才算真的生效，否则发布的就是一份没人执行的承诺。顺序放在忽略与灰度之前：
	// 安全下限优先于「怎么升」的节奏控制。
	if killed, reason := killedRelease(component, releaseValue, now); killed {
		item.Action = model.ActionSkip
		item.State = model.StateBlocked
		item.Reason = reason
		return item, true, nil
	}
	if forced, reason := forcedUpdate(releaseValue, installed, now); forced {
		item.State = model.StatePending
		item.Approval = model.ApprovalAuto
		item.Reason = reason
		return item, true, nil
	}

	// 目标自己忽略了这个版本：照样进计划，但标成「跳过」而不是悄悄消失——界面要能显示
	// 「该项已被忽略」以及怎么撤销。只比版本号，所以渠道上出现更新的版本时会自动重新提出。
	if ignoredVersion != "" && ignoredVersion == item.To {
		item.Action = model.ActionSkip
		item.State = model.StateDeferred
		item.Reason = fmt.Sprintf("已忽略此版本 %s", item.To)
		item.Ignored = true
		return item, true, nil
	}

	if !policy.MatchTarget(policyValue.Targets, target.ID) {
		item.Action = model.ActionSkip
		item.State = model.StateBlocked
		item.Reason = "不在分发目标范围"
		return item, true, nil
	}

	definition, err := s.store.GetComponent(component)
	if err == nil && definition.MinProductVersion != "" && target.ProductVersion != "" {
		if comparison, cmpErr := policy.CompareVersions(target.ProductVersion, definition.MinProductVersion); cmpErr == nil && comparison < 0 {
			item.State = model.StateBlocked
			item.Reason = fmt.Sprintf("产品版本 %s 低于组件要求 %s", target.ProductVersion, definition.MinProductVersion)
			return item, true, nil
		}
	}

	item.Bucket = policy.Bucket(target.ID, component, releaseValue.ID)
	if item.Bucket >= policyValue.Percentage {
		item.State = model.StateDeferred
		item.Reason = fmt.Sprintf("灰度未命中（桶 %d ≥ %d%%）", item.Bucket, policyValue.Percentage)
		return item, true, nil
	}

	within, err := policy.WithinWindow(policyValue.Window, now)
	if err != nil {
		return model.PlanItem{}, false, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if !within {
		item.State = model.StateDeferred
		item.Reason = fmt.Sprintf("不在维护窗口 %s（UTC）", policyValue.Window)
		return item, true, nil
	}

	return item, true, nil
}
