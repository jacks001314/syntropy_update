package release

import (
	"errors"
	"fmt"
	"strings"

	"syntropy_update/internal/model"
	"syntropy_update/internal/store"
)

// SetIgnore 记录「这个目标忽略这个组件的这个版本」。
//
// 忽略是每个目标的意愿（对应 Codex app 的 dismissed_version，落在目标自己的
// version.json 里），所以它不进 (组件, 渠道) 策略——否则一台机器不想升级会拖住整条渠道。
// 同一个 (目标, 组件) 只保留一个版本：渠道上出现更新的版本，忽略自然失效。
func (s *Service) SetIgnore(ignore model.Ignore, actor string) (model.Ignore, error) {
	ignore.TargetID = strings.TrimSpace(ignore.TargetID)
	ignore.Component = strings.TrimSpace(ignore.Component)
	ignore.Version = strings.TrimSpace(ignore.Version)
	if ignore.TargetID == "" {
		return model.Ignore{}, fmt.Errorf("%w: target id is required", ErrValidation)
	}
	if ignore.Component == "" {
		return model.Ignore{}, fmt.Errorf("%w: component is required", ErrValidation)
	}
	if !ValidVersion(ignore.Version) {
		return model.Ignore{}, fmt.Errorf("%w: version %q must be MAJOR.MINOR.PATCH", ErrValidation, ignore.Version)
	}

	previous, err := s.store.GetIgnore(ignore.TargetID, ignore.Component)
	switch {
	case err == nil:
		ignore.CreatedAtMS = previous.CreatedAtMS
	case errors.Is(err, store.ErrNotFound):
		ignore.CreatedAtMS = model.NowMS()
	default:
		return model.Ignore{}, err
	}
	ignore.Actor = strings.TrimSpace(actor)
	ignore.UpdatedAtMS = model.NowMS()
	if err := s.store.PutIgnore(ignore); err != nil {
		return model.Ignore{}, err
	}
	if err := s.store.AppendAudit(model.AuditEntry{
		Action:  "ignore.set",
		Actor:   ignore.Actor,
		Subject: ignore.TargetID + "/" + ignore.Component,
		Detail:  "version=" + ignore.Version,
	}); err != nil {
		return model.Ignore{}, err
	}
	return ignore, nil
}

// ClearIgnore 撤销忽略；记录不存在时返回 ErrNotFound。
func (s *Service) ClearIgnore(targetID, component, actor string) error {
	targetID = strings.TrimSpace(targetID)
	component = strings.TrimSpace(component)
	if targetID == "" || component == "" {
		return fmt.Errorf("%w: target id and component are required", ErrValidation)
	}
	if err := s.store.DeleteIgnore(targetID, component); err != nil {
		return err
	}
	return s.store.AppendAudit(model.AuditEntry{
		Action:  "ignore.clear",
		Actor:   strings.TrimSpace(actor),
		Subject: targetID + "/" + component,
	})
}

// ListIgnores 列出忽略记录；target 为空时返回全部。
func (s *Service) ListIgnores(target string) ([]model.Ignore, error) {
	return s.store.ListIgnores(strings.TrimSpace(target))
}

// ignoredVersions 返回某目标「组件 → 被忽略的版本」，供计划计算使用。
func (s *Service) ignoredVersions(targetID string) (map[string]string, error) {
	ignores, err := s.store.ListIgnores(targetID)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string, len(ignores))
	for _, entry := range ignores {
		versions[entry.Component] = entry.Version
	}
	return versions, nil
}
