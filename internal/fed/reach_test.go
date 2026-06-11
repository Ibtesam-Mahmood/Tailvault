package fed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

func TestProbe_MixedPassFail(t *testing.T) {
	members := []catalog.Member{
		mem("up-1", "active", "2026-06-11T09:00:00Z"),
		mem("down-1", "active", "2026-06-11T09:00:00Z"),
		mem("up-2", "active", "2026-06-11T09:00:00Z"),
	}
	probe := func(_ context.Context, m catalog.Member) error {
		if m.Name == "down-1" {
			return errors.New("node offline")
		}
		return nil
	}
	r := Probe(context.Background(), members, probe)

	if !eqStrs(r.Required, []string{"down-1", "up-1", "up-2"}) {
		t.Errorf("Required = %v", r.Required)
	}
	if !eqStrs(r.Answered, []string{"up-1", "up-2"}) {
		t.Errorf("Answered = %v, want [up-1 up-2]", r.Answered)
	}
	if !eqStrs(r.Unreachable, []string{"down-1"}) {
		t.Errorf("Unreachable = %v, want [down-1]", r.Unreachable)
	}
	if r.AllAnswered() {
		t.Error("AllAnswered: want false")
	}
	if !r.Partial() {
		t.Error("Partial: want true")
	}
}

func TestProbe_AllAnswered(t *testing.T) {
	members := []catalog.Member{
		mem("a", "active", "2026-06-11T09:00:00Z"),
		mem("b", "active", "2026-06-11T09:00:00Z"),
	}
	r := Probe(context.Background(), members, func(_ context.Context, _ catalog.Member) error { return nil })
	if !r.AllAnswered() || r.Partial() {
		t.Errorf("want all-answered; got Unreachable=%v", r.Unreachable)
	}
}

func TestProbe_ContextCancelled(t *testing.T) {
	members := []catalog.Member{
		mem("slow-1", "active", "2026-06-11T09:00:00Z"),
		mem("slow-2", "active", "2026-06-11T09:00:00Z"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before probing
	probe := func(ctx context.Context, _ catalog.Member) error {
		// Honor cancellation: block until ctx ends, then report failure.
		<-ctx.Done()
		return ctx.Err()
	}
	r := Probe(ctx, members, probe)
	// With ctx cancelled, no member can be confirmed → all Unreachable.
	if r.AllAnswered() {
		t.Errorf("want partial under cancellation; Unreachable=%v", r.Unreachable)
	}
	if len(r.Required) != 2 {
		t.Errorf("Required = %v, want 2 members", r.Required)
	}
}

func TestProbe_DeadlineBoundsHungMember(t *testing.T) {
	members := []catalog.Member{mem("hung", "active", "2026-06-11T09:00:00Z")}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// Prober ignores ctx and never returns within the deadline window.
	probe := func(_ context.Context, _ catalog.Member) error {
		time.Sleep(2 * time.Second)
		return nil
	}
	done := make(chan Reach, 1)
	go func() { done <- Probe(ctx, members, probe) }()
	select {
	case r := <-done:
		if !r.Partial() {
			t.Errorf("hung member should be Unreachable; got %v", r.Unreachable)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Probe did not return after its ctx deadline — hung member stalled it")
	}
}

func TestProbe_Empty(t *testing.T) {
	r := Probe(context.Background(), nil, func(_ context.Context, _ catalog.Member) error { return nil })
	if !r.AllAnswered() {
		t.Error("empty probe: AllAnswered should be true (vacuously)")
	}
	if len(r.Required) != 0 {
		t.Errorf("Required = %v, want empty", r.Required)
	}
}
