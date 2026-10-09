package store

import (
	"errors"
	"path/filepath"
	"testing"

	"syntropy_update/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "update.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestComponentAndReleaseRoundTrip(t *testing.T) {
	store := openTestStore(t)
	component := model.Component{Name: "desktop", Kind: model.KindInstaller, Versioning: model.VersioningProduct}
	if err := store.PutComponent(component); err != nil {
		t.Fatalf("put component: %v", err)
	}
	loaded, err := store.GetComponent("desktop")
	if err != nil {
		t.Fatalf("get component: %v", err)
	}
	if loaded.Name != "desktop" || loaded.Kind != model.KindInstaller {
		t.Fatalf("component = %+v", loaded)
	}
	if _, err := store.GetComponent("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	release := model.Release{ID: "release-1", Component: "desktop", Version: "0.4.0", Channel: "beta"}
	if err := store.PutRelease(release); err != nil {
		t.Fatalf("put release: %v", err)
	}
	found, err := store.FindRelease("desktop", "beta", "0.4.0")
	if err != nil {
		t.Fatalf("find release: %v", err)
	}
	if found.ID != "release-1" {
		t.Fatalf("found = %+v", found)
	}
	if _, err := store.FindRelease("desktop", "beta", "9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestArtifactsAndChannelPointer(t *testing.T) {
	store := openTestStore(t)
	first := model.Artifact{ID: "artifact-1", ReleaseID: "release-1", OS: "windows", Arch: "amd64", SHA256: "aa"}
	second := model.Artifact{ID: "artifact-2", ReleaseID: "release-1", OS: "darwin", Arch: "arm64", SHA256: "bb"}
	other := model.Artifact{ID: "artifact-3", ReleaseID: "release-2", OS: "linux", Arch: "amd64", SHA256: "cc"}
	for _, artifact := range []model.Artifact{first, second, other} {
		if err := store.PutArtifact(artifact); err != nil {
			t.Fatalf("put artifact: %v", err)
		}
	}
	artifacts, err := store.ListArtifacts("release-1")
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(artifacts))
	}

	pointer := model.ChannelPointer{Component: "desktop", Channel: "beta", ReleaseID: "release-1"}
	if err := store.SetChannelPointer(pointer); err != nil {
		t.Fatalf("set pointer: %v", err)
	}
	loaded, err := store.GetChannelPointer("desktop", "beta")
	if err != nil {
		t.Fatalf("get pointer: %v", err)
	}
	if loaded.ReleaseID != "release-1" {
		t.Fatalf("pointer = %+v", loaded)
	}
}

func TestAuditOrdering(t *testing.T) {
	store := openTestStore(t)
	for _, entry := range []model.AuditEntry{
		{ID: "audit-1", Action: "first", AtMS: 1000},
		{ID: "audit-2", Action: "second", AtMS: 2000},
		{ID: "audit-3", Action: "third", AtMS: 3000},
	} {
		if err := store.AppendAudit(entry); err != nil {
			t.Fatalf("append audit: %v", err)
		}
	}
	entries, err := store.ListAudit(2)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Action != "third" || entries[1].Action != "second" {
		t.Fatalf("unexpected order: %+v", entries)
	}
}
