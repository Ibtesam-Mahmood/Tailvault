package gc

import (
	"reflect"
	"testing"
)

// set builds a KeepSet from bare shas.
func set(shas ...string) KeepSet {
	k := KeepSet{}
	for _, s := range shas {
		k.Add(s)
	}
	return k
}

func TestStripObjectKey(t *testing.T) {
	if got := StripObjectKey("objects/abc123"); got != "abc123" {
		t.Errorf("StripObjectKey(objects/abc123) = %q, want abc123", got)
	}
	if got := StripObjectKey("abc123"); got != "abc123" {
		t.Errorf("StripObjectKey(abc123) = %q, want abc123 (already bare)", got)
	}
}

func TestPlanSweep(t *testing.T) {
	tests := []struct {
		name         string
		stored       []string
		keep         KeepSet
		preserve     KeepSet
		wantEligible []string
		wantKept     int
		wantPreserve int
	}{
		{
			name:         "delete prunes unreferenced blob",
			stored:       []string{"objects/A", "objects/B"},
			keep:         set("A"), // B dropped from every lock
			preserve:     set(),
			wantEligible: []string{"B"},
			wantKept:     1,
		},
		{
			name:         "preserve survives even when unreferenced",
			stored:       []string{"objects/A", "objects/P"},
			keep:         set("A"),
			preserve:     set("P"), // P deleted from locks but preserve-protected
			wantEligible: nil,
			wantKept:     1,
			wantPreserve: 1,
		},
		{
			name:         "cross-branch survival: sha kept by another branch's lock",
			stored:       []string{"objects/SHARED", "objects/GONE"},
			keep:         set("SHARED"), // removed on branch A, still in branch B's lock -> union keeps it
			preserve:     set(),
			wantEligible: []string{"GONE"},
			wantKept:     1,
		},
		{
			name:         "history versions all kept",
			stored:       []string{"objects/v3", "objects/v2", "objects/v1"},
			keep:         set("v3", "v2", "v1"), // versions[] unioned into keep-set
			preserve:     set(),
			wantEligible: nil,
			wantKept:     3,
		},
		{
			name:         "keep takes precedence over preserve (no double count)",
			stored:       []string{"objects/X"},
			keep:         set("X"),
			preserve:     set("X"),
			wantEligible: nil,
			wantKept:     1,
			wantPreserve: 0,
		},
		{
			name:         "bare-sha keys (no prefix) handled",
			stored:       []string{"A", "B"},
			keep:         set("A"),
			preserve:     set(),
			wantEligible: []string{"B"},
			wantKept:     1,
		},
		{
			name:         "empty store",
			stored:       nil,
			keep:         set("A"),
			preserve:     set(),
			wantEligible: nil,
		},
		{
			name:         "all eligible when keep+preserve empty",
			stored:       []string{"objects/A", "objects/B"},
			keep:         set(),
			preserve:     set(),
			wantEligible: []string{"A", "B"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanSweep(tt.stored, tt.keep, tt.preserve)
			if !reflect.DeepEqual(got.Eligible, tt.wantEligible) {
				t.Errorf("Eligible = %v, want %v", got.Eligible, tt.wantEligible)
			}
			if got.Kept != tt.wantKept {
				t.Errorf("Kept = %d, want %d", got.Kept, tt.wantKept)
			}
			if got.Preserved != tt.wantPreserve {
				t.Errorf("Preserved = %d, want %d", got.Preserved, tt.wantPreserve)
			}
		})
	}
}
