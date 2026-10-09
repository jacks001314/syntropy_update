package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "update.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaultsTLSDisabled(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TLSDomains) != 0 {
		t.Fatalf("TLSDomains = %v, want empty (HTTPS is opt-in)", cfg.TLSDomains)
	}
	if cfg.TLSAddr != ":443" {
		t.Errorf("TLSAddr = %q, want :443", cfg.TLSAddr)
	}
	if got, want := cfg.TLSCacheDir, filepath.Join("data", "acme"); got != want {
		t.Errorf("TLSCacheDir = %q, want %q", got, want)
	}
}

func TestTLSCacheDirFollowsDataDir(t *testing.T) {
	path := writeConfig(t, `{"dataDir":"/var/lib/syntropy-update/data","tlsDomains":["app.syntropy-lab.com"]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.TLSCacheDir, filepath.Join("/var/lib/syntropy-update/data", "acme"); got != want {
		t.Errorf("TLSCacheDir = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(cfg.TLSDomains, []string{"app.syntropy-lab.com"}) {
		t.Errorf("TLSDomains = %v", cfg.TLSDomains)
	}
}

func TestNormalizeDomains(t *testing.T) {
	got := normalizeDomains([]string{" App.Syntropy-Lab.com ", "app.syntropy-lab.com", "", "  ", "cdn.example"})
	want := []string{"app.syntropy-lab.com", "cdn.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeDomains = %v, want %v", got, want)
	}
	if out := normalizeDomains(nil); len(out) != 0 {
		t.Fatalf("normalizeDomains(nil) = %v, want empty", out)
	}
}
