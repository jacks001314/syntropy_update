package release

import (
	"errors"
	"testing"

	"syntropy_update/internal/model"
)

func TestComponentManifest(t *testing.T) {
	fixture := newFixture(t)
	mbot := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	fixture.upload(t, mbot.ID, "linux", "amd64", []byte("mbot-binary"))
	fixture.upload(t, mbot.ID, "windows", "amd64", []byte("mbot-binary-exe"))

	encoded, err := fixture.service.ComponentManifest("mbot", "stable")
	if err != nil {
		t.Fatalf("component manifest: %v", err)
	}
	_, payload := verifyManifest(t, fixture.public, encoded)
	if payload["component"] != "mbot" || payload["version"] != "1.2.0" || payload["channel"] != "stable" {
		t.Fatalf("payload = %+v", payload)
	}
	artifacts, ok := payload["artifacts"].([]any)
	if !ok || len(artifacts) != 2 {
		t.Fatalf("artifacts = %v", payload["artifacts"])
	}
	first := artifacts[0].(map[string]any)
	for _, key := range []string{"arch", "filename", "os", "sha256", "size", "url"} {
		if _, present := first[key]; !present {
			t.Fatalf("artifact missing %q: %+v", key, first)
		}
	}
}

func TestPublishDistribution(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.EnsureComponent(model.Component{Name: "codex", Kind: model.KindArchive, Versioning: model.VersioningIndependent}); err != nil {
		t.Fatalf("ensure component: %v", err)
	}
	mbot := fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})
	codex := fixture.publish(t, model.Release{Component: "codex", Version: "0.160.0", Channel: "stable"})
	fixture.upload(t, mbot.ID, "linux", "amd64", []byte("mbot"))
	fixture.upload(t, codex.ID, "linux", "amd64", []byte("codex"))

	distribution, err := fixture.service.PublishDistribution(model.Distribution{
		ProductVersion: "1.2.0",
		Channel:        "stable",
		Components: []model.DistributionComponent{
			{Name: "mbot", Version: "1.2.0"},
			{Name: "codex", Version: "0.160.0"},
		},
	}, "test")
	if err != nil {
		t.Fatalf("publish distribution: %v", err)
	}
	if distribution.Components[0].ReleaseID != mbot.ID || distribution.Components[1].ReleaseID != codex.ID {
		t.Fatalf("component release ids = %+v", distribution.Components)
	}

	encoded, err := fixture.service.SignedDistribution("stable", "1.2.0")
	if err != nil {
		t.Fatalf("signed distribution: %v", err)
	}
	_, payload := verifyManifest(t, fixture.public, encoded)
	if payload["product"] != ProductName || payload["productVersion"] != "1.2.0" {
		t.Fatalf("payload = %+v", payload)
	}
	components, ok := payload["components"].([]any)
	if !ok || len(components) != 2 {
		t.Fatalf("components = %v", payload["components"])
	}
	first := components[0].(map[string]any)
	if first["name"] != "mbot" || first["kind"] != model.KindBinary {
		t.Fatalf("first component = %+v", first)
	}
	second := components[1].(map[string]any)
	if second["name"] != "codex" || second["kind"] != model.KindArchive {
		t.Fatalf("second component = %+v", second)
	}

	latest, err := fixture.service.SignedDistribution("stable", "")
	if err != nil {
		t.Fatalf("latest distribution: %v", err)
	}
	if _, payload := verifyManifest(t, fixture.public, latest); payload["productVersion"] != "1.2.0" {
		t.Fatalf("latest payload = %+v", payload)
	}
}

func TestPublishDistributionValidation(t *testing.T) {
	fixture := newFixture(t)
	fixture.publish(t, model.Release{Component: "mbot", Version: "1.2.0", Channel: "stable"})

	cases := []struct {
		name         string
		distribution model.Distribution
	}{
		{"unknown component", model.Distribution{ProductVersion: "1.2.0", Channel: "stable", Components: []model.DistributionComponent{{Name: "nbot", Version: "1.2.0"}}}},
		{"unknown version", model.Distribution{ProductVersion: "1.2.0", Channel: "stable", Components: []model.DistributionComponent{{Name: "mbot", Version: "9.9.9"}}}},
		{"duplicate component", model.Distribution{ProductVersion: "1.2.0", Channel: "stable", Components: []model.DistributionComponent{{Name: "mbot", Version: "1.2.0"}, {Name: "mbot", Version: "1.2.0"}}}},
		{"bad product version", model.Distribution{ProductVersion: "1.2", Channel: "stable", Components: []model.DistributionComponent{{Name: "mbot", Version: "1.2.0"}}}},
		{"unknown channel", model.Distribution{ProductVersion: "1.2.0", Channel: "nightly", Components: []model.DistributionComponent{{Name: "mbot", Version: "1.2.0"}}}},
		{"no components", model.Distribution{ProductVersion: "1.2.0", Channel: "stable"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := fixture.service.PublishDistribution(testCase.distribution, "test"); !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
}

func TestLatestDistributionMissing(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.SignedDistribution("stable", ""); err == nil {
		t.Fatal("expected error for empty channel")
	}
}
