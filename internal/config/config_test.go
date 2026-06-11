package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadProposalSample(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "sample.toml"))
	if err != nil {
		t.Fatalf("Load(sample): %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Storage.Location != "home-pi" {
		t.Errorf("Location = %q, want home-pi", cfg.Storage.Location)
	}
	if cfg.Storage.Subpath != "root-pnp" {
		t.Errorf("Subpath = %q, want root-pnp", cfg.Storage.Subpath)
	}
	if cfg.Rules.MinSize != "5MB" {
		t.Errorf("MinSize = %q, want 5MB", cfg.Rules.MinSize)
	}
	if len(cfg.Rules.Include) != 4 {
		t.Errorf("Include = %v, want 4 globs", cfg.Rules.Include)
	}
	if !cfg.Rules.AutoDelete {
		t.Error("AutoDelete = false, want true")
	}
	if cfg.Rules.History {
		t.Error("History = true, want false")
	}
	if len(cfg.Rules.Overrides) != 1 {
		t.Fatalf("Overrides = %v, want 1", cfg.Rules.Overrides)
	}
	o := cfg.Rules.Overrides[0]
	if o.Match != "masters/**" || !o.History || !o.Preserve {
		t.Errorf("override = %+v, want {masters/**, history+preserve}", o)
	}
}

func TestDefaultsAutoDeleteWhenOmitted(t *testing.T) {
	// Default() seeds auto_delete=true; a TOML omitting it must keep that.
	cfg := Default()
	if !cfg.Rules.AutoDelete {
		t.Error("Default() AutoDelete = false, want true")
	}
	if cfg.Version != 1 || cfg.Rules.MinSize != "5MB" {
		t.Errorf("Default() = %+v, want version 1 + 5MB", cfg)
	}
	// A file that explicitly sets auto_delete = false must override the default.
	cfg2, err := loadString(t, "version=1\n[storage]\nlocation=\"x\"\n[rules]\nauto_delete=false\n")
	if err != nil {
		t.Fatalf("load explicit false: %v", err)
	}
	if cfg2.Rules.AutoDelete {
		t.Error("explicit auto_delete=false was not honored")
	}
}

func TestValidateErrors(t *testing.T) {
	if _, err := Load(filepath.Join("testdata", "badversion.toml")); err == nil {
		t.Error("version=2 should fail validation")
	}
	if _, err := Load(filepath.Join("testdata", "nolocation.toml")); err == nil {
		t.Error("missing location should fail validation")
	}
}

func TestRoundTripStable(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "sample.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "tailvault.toml")
	if err := Write(out, cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	cfg2, err := Load(out)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reflect.DeepEqual(cfg, cfg2) {
		t.Errorf("round-trip mismatch:\n %+v\n %+v", cfg, cfg2)
	}
	// Overrides order preserved.
	if cfg2.Rules.Overrides[0].Match != "masters/**" {
		t.Errorf("override order not preserved: %v", cfg2.Rules.Overrides)
	}
}

// loadString writes content to a temp file and loads it.
func loadString(t *testing.T, content string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}
