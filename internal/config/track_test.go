package config

import "testing"

func TestValidateGlob(t *testing.T) {
	good := []string{"**/*.pdf", "drafts/**", "a/b.stl", "*.tmp"}
	for _, g := range good {
		if err := ValidateGlob(g); err != nil {
			t.Errorf("ValidateGlob(%q) = %v, want nil", g, err)
		}
	}
	bad := []string{"", "   ", "[bad", "[a-"}
	for _, g := range bad {
		if err := ValidateGlob(g); err == nil {
			t.Errorf("ValidateGlob(%q) = nil, want error", g)
		}
	}
}

func TestAddInclude(t *testing.T) {
	c := &Config{}
	if !c.AddInclude("**/*.pdf") {
		t.Fatal("first add should return added=true")
	}
	if len(c.Rules.Include) != 1 || c.Rules.Include[0] != "**/*.pdf" {
		t.Fatalf("include = %v, want [**/*.pdf]", c.Rules.Include)
	}
	// Idempotent: re-adding is a no-op and returns false.
	if c.AddInclude("**/*.pdf") {
		t.Error("re-add should return added=false")
	}
	if len(c.Rules.Include) != 1 {
		t.Errorf("include should not grow on duplicate: %v", c.Rules.Include)
	}
	// Append-only ordering.
	c.AddInclude("**/*.stl")
	if c.Rules.Include[0] != "**/*.pdf" || c.Rules.Include[1] != "**/*.stl" {
		t.Errorf("append-only order broken: %v", c.Rules.Include)
	}
}
