package release

import (
	"errors"
	"testing"

	"syntropy_update/internal/model"
)

func TestSetIgnoreValidation(t *testing.T) {
	fixture := newFixture(t)
	cases := []model.Ignore{
		{Component: "mbot", Version: "1.2.0"},
		{TargetID: "node-a", Version: "1.2.0"},
		{TargetID: "node-a", Component: "mbot", Version: "1.2"},
		{TargetID: "node-a", Component: "mbot"},
	}
	for index, ignore := range cases {
		if _, err := fixture.service.SetIgnore(ignore, "desktop"); err == nil {
			t.Fatalf("case %d: expected a validation error for %+v", index, ignore)
		}
	}
}

func TestSetIgnoreKeepsCreatedAtAndWritesAudit(t *testing.T) {
	fixture := newFixture(t)
	first, err := fixture.service.SetIgnore(model.Ignore{TargetID: "node-a", Component: "mbot", Version: "1.2.0"}, "desktop")
	if err != nil {
		t.Fatalf("set ignore: %v", err)
	}
	if first.CreatedAtMS == 0 || first.CreatedAtMS != first.UpdatedAtMS {
		t.Fatalf("ignore = %+v", first)
	}

	// 同一个 (目标, 组件) 只保留一个版本：换版本号要保留创建时间。
	second, err := fixture.service.SetIgnore(model.Ignore{TargetID: "node-a", Component: "mbot", Version: "1.3.0"}, "desktop")
	if err != nil {
		t.Fatalf("set ignore again: %v", err)
	}
	if second.Version != "1.3.0" || second.CreatedAtMS != first.CreatedAtMS {
		t.Fatalf("second ignore = %+v, want version 1.3.0 and the original createdAt", second)
	}
	ignores, err := fixture.service.ListIgnores("node-a")
	if err != nil {
		t.Fatalf("list ignores: %v", err)
	}
	if len(ignores) != 1 || ignores[0].Version != "1.3.0" {
		t.Fatalf("ignores = %+v, want exactly one record", ignores)
	}

	entries, err := fixture.store.ListAudit(10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	setCount := 0
	for _, entry := range entries {
		if entry.Action == "ignore.set" {
			setCount++
		}
	}
	if setCount != 2 {
		t.Fatalf("audit entries = %+v, want two ignore.set", entries)
	}
}

func TestClearIgnoreUnknownReturnsNotFound(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.service.ClearIgnore("node-a", "mbot", "desktop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clear unknown ignore = %v, want ErrNotFound", err)
	}
	if _, err := fixture.service.SetIgnore(model.Ignore{TargetID: "node-a", Component: "mbot", Version: "1.2.0"}, "desktop"); err != nil {
		t.Fatalf("set ignore: %v", err)
	}
	if err := fixture.service.ClearIgnore("node-a", "mbot", "desktop"); err != nil {
		t.Fatalf("clear ignore: %v", err)
	}
	if ignores, err := fixture.service.ListIgnores(""); err != nil || len(ignores) != 0 {
		t.Fatalf("ignores = %+v err=%v, want none", ignores, err)
	}
}

func TestListIgnoresFiltersByTarget(t *testing.T) {
	fixture := newFixture(t)
	for _, target := range []string{"node-b", "node-a"} {
		if _, err := fixture.service.SetIgnore(model.Ignore{TargetID: target, Component: "mbot", Version: "1.2.0"}, "desktop"); err != nil {
			t.Fatalf("set ignore for %s: %v", target, err)
		}
	}
	ignores, err := fixture.service.ListIgnores("node-b")
	if err != nil {
		t.Fatalf("list ignores: %v", err)
	}
	if len(ignores) != 1 || ignores[0].TargetID != "node-b" {
		t.Fatalf("ignores = %+v", ignores)
	}
	all, err := fixture.service.ListIgnores("")
	if err != nil || len(all) != 2 {
		t.Fatalf("all ignores = %+v err=%v, want 2", all, err)
	}
	if all[0].TargetID != "node-a" {
		t.Fatalf("ignores should be sorted by target, got %+v", all)
	}
}

func TestBuildPlanHonoursIgnore(t *testing.T) {
	fixture := newFixture(t)
	saved := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, saved.ID, "linux", "amd64", []byte("mbot"))

	if _, err := fixture.service.SetIgnore(model.Ignore{TargetID: "node-a", Component: "mbot", Version: "1.2.0"}, "desktop"); err != nil {
		t.Fatalf("set ignore: %v", err)
	}
	plan := planFor(t, fixture, "stable",
		model.TargetInventory{ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"}},
		model.TargetInventory{ID: "node-b", Kind: "node", Components: map[string]string{"mbot": "1.1.0"}},
	)

	item, ok := findItem(plan, "node-a", "mbot")
	if !ok {
		t.Fatal("an ignored version must still appear in the plan, otherwise the UI cannot show why")
	}
	if item.Action != model.ActionSkip || item.State != model.StateDeferred || item.To != "1.2.0" {
		t.Fatalf("ignored item = %+v", item)
	}
	if item.Reason != "已忽略此版本 1.2.0" {
		t.Fatalf("reason = %q", item.Reason)
	}
	if !item.Ignored {
		t.Fatal("an ignored item must be flagged as ignored, so the UI does not have to guess from the wording")
	}
	other, ok := findItem(plan, "node-b", "mbot")
	if !ok || other.Action != model.ActionUpgrade || other.State != model.StatePending {
		t.Fatalf("node-b item = %+v ok=%v, an ignore must not leak to other targets", other, ok)
	}
	if other.Ignored {
		t.Fatal("an item that is merely pending must not be flagged as ignored")
	}

	// 渠道上出现更新的版本：忽略只记一个版本，升级因此重新被提出。
	newer := fixture.publish(t, model.Release{Component: "mbot", Version: "1.3.0", Channel: "stable"})
	fixture.upload(t, newer.ID, "linux", "amd64", []byte("mbot-1.3"))
	plan = planFor(t, fixture, "stable", model.TargetInventory{ID: "node-a", Kind: "node", Components: map[string]string{"mbot": "1.1.0"}})
	item, ok = findItem(plan, "node-a", "mbot")
	if !ok || item.Action != model.ActionUpgrade || item.State != model.StatePending || item.To != "1.3.0" {
		t.Fatalf("item after a newer release = %+v ok=%v, want a fresh upgrade", item, ok)
	}
}
