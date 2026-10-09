package release

import (
	"fmt"
	"strings"

	"syntropy_update/internal/model"
)

// resultStates 是目标端可以回传的执行结果状态：与桌面端 install-executor 的步骤状态一一
// 对应（planned 是「还没装」的预演态，不允许回传）。
var resultStates = map[string]struct{}{
	model.ResultInstalled:   {},
	model.ResultHandedOff:   {},
	model.ResultFailed:      {},
	model.ResultUnsupported: {},
}

// maxResultMessage 与桌面端的截断口径一致：结果说明只留一条可读原因，不留整段日志。
const maxResultMessage = 512

// RecordResult 记录一次组件升级的执行结果。
//
// 结果与库存是两件事：库存回答「现在跑的是哪一版」，结果回答「这一次升级成没成、失败在
// 哪一步」。目标可以只报结果（比如离线补报），所以这里不要求先有库存记录，也不改写库存。
func (s *Service) RecordResult(result model.UpgradeResult, actor string) (model.UpgradeResult, error) {
	result.TargetID = strings.TrimSpace(result.TargetID)
	result.Component = strings.TrimSpace(result.Component)
	result.State = strings.TrimSpace(result.State)
	result.Message = strings.TrimSpace(result.Message)
	result.Action = strings.TrimSpace(result.Action)
	result.From = strings.TrimSpace(result.From)
	result.To = strings.TrimSpace(result.To)
	result.PlanID = strings.TrimSpace(result.PlanID)
	result.ReleaseID = strings.TrimSpace(result.ReleaseID)
	if result.TargetID == "" {
		return model.UpgradeResult{}, fmt.Errorf("%w: target id is required", ErrValidation)
	}
	if result.Component == "" {
		return model.UpgradeResult{}, fmt.Errorf("%w: component is required", ErrValidation)
	}
	if _, ok := resultStates[result.State]; !ok {
		return model.UpgradeResult{}, fmt.Errorf("%w: state %q must be installed|handed-off|failed|unsupported", ErrValidation, result.State)
	}
	if len(result.Message) > maxResultMessage {
		return model.UpgradeResult{}, fmt.Errorf("%w: message must be at most %d characters", ErrValidation, maxResultMessage)
	}
	for name, value := range map[string]string{
		"from": result.From,
		"to":   result.To,
	} {
		if value != "" && !ValidVersion(value) {
			return model.UpgradeResult{}, fmt.Errorf("%w: %s %q must be MAJOR.MINOR.PATCH", ErrValidation, name, value)
		}
	}
	if result.ReleaseID != "" && strings.ContainsAny(result.ReleaseID, "/ \t\n") {
		return model.UpgradeResult{}, fmt.Errorf("%w: release id %q is not a plain identifier", ErrValidation, result.ReleaseID)
	}

	result.ID = model.NewID("result")
	result.AtMS = model.NowMS()
	if err := s.store.PutResult(result); err != nil {
		return model.UpgradeResult{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "result.report",
		Actor:   strings.TrimSpace(actor),
		Subject: result.TargetID + "/" + result.Component,
		Detail:  result.State + " " + result.From + "->" + result.To,
		TraceID: result.PlanID,
	}); err != nil {
		return model.UpgradeResult{}, err
	}
	return result, nil
}

// ListResults 列出升级结果；target 为空时返回全部，limit <= 0 表示不限。
func (s *Service) ListResults(target string, limit int) ([]model.UpgradeResult, error) {
	return s.store.ListResults(strings.TrimSpace(target), limit)
}
