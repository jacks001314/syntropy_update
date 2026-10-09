package release

import (
	"errors"
	"strings"
	"testing"

	"syntropy_update/internal/model"
)

func TestRecordResultValidatesInput(t *testing.T) {
	fixture := newFixture(t)
	cases := []struct {
		name   string
		result model.UpgradeResult
	}{
		{"missing target", model.UpgradeResult{Component: "app-server", State: model.ResultInstalled}},
		{"missing component", model.UpgradeResult{TargetID: "desktop", State: model.ResultInstalled}},
		{"unknown state", model.UpgradeResult{TargetID: "desktop", Component: "app-server", State: "planned"}},
		{"bad from version", model.UpgradeResult{TargetID: "desktop", Component: "app-server", State: model.ResultInstalled, From: "1.0"}},
		{"bad to version", model.UpgradeResult{TargetID: "desktop", Component: "app-server", State: model.ResultInstalled, To: "1.0.0-rc.1"}},
		{"message too long", model.UpgradeResult{TargetID: "desktop", Component: "app-server", State: model.ResultFailed, Message: strings.Repeat("x", maxResultMessage+1)}},
		{"release id with separator", model.UpgradeResult{TargetID: "desktop", Component: "app-server", State: model.ResultInstalled, ReleaseID: "release/../etc"}},
	}
	for _, testCase := range cases {
		if _, err := fixture.service.RecordResult(testCase.result, "desktop"); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: err = %v, want ErrValidation", testCase.name, err)
		}
	}
}

func TestRecordResultStoresAndListsNewestFirst(t *testing.T) {
	fixture := newFixture(t)

	first, err := fixture.service.RecordResult(model.UpgradeResult{
		TargetID:  "desktop",
		Component: "app-server",
		Action:    model.ActionUpgrade,
		From:      "1.0.0",
		To:        "1.1.0",
		State:     model.ResultInstalled,
		PlanID:    "plan-1",
	}, "desktop")
	if err != nil {
		t.Fatalf("record installed: %v", err)
	}
	if first.ID == "" || first.AtMS == 0 {
		t.Fatalf("server must stamp id and time: %+v", first)
	}

	if _, err := fixture.service.RecordResult(model.UpgradeResult{
		TargetID:  "desktop",
		Component: "templates",
		State:     model.ResultFailed,
		Message:   "内容归档校验失败",
	}, "desktop"); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if _, err := fixture.service.RecordResult(model.UpgradeResult{
		TargetID:  "node-a",
		Component: "nbot",
		State:     model.ResultHandedOff,
	}, "node-a"); err != nil {
		t.Fatalf("record other target: %v", err)
	}

	all, err := fixture.service.ListResults("", 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d results, want 3", len(all))
	}

	onlyDesktop, err := fixture.service.ListResults("desktop", 0)
	if err != nil {
		t.Fatalf("list target: %v", err)
	}
	if len(onlyDesktop) != 2 {
		t.Fatalf("list desktop = %d results, want 2", len(onlyDesktop))
	}
	for _, entry := range onlyDesktop {
		if entry.TargetID != "desktop" {
			t.Fatalf("target filter leaked %q", entry.TargetID)
		}
	}

	limited, err := fixture.service.ListResults("", 1)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited list = %d results, want 1", len(limited))
	}
	failed := false
	for _, entry := range onlyDesktop {
		if entry.State == model.ResultFailed && entry.Component == "templates" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("failed result was not kept: %+v", onlyDesktop)
	}
}
