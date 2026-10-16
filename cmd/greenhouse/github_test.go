package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// gh prints nothing, not [], when `label list --search` matches no label. That
// must read as "no label yet" and create it, not fail every tick.
func TestEnsureLabel_EmptySearchCreatesTheLabel(t *testing.T) {
	for _, out := range []string{"", "\n", "[]"} {
		var calls []string
		g := &ghCLI{run: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args[:2], " "))
			if args[1] == "list" {
				return []byte(out), nil
			}
			return nil, nil
		}}
		if err := g.EnsureLabel(context.Background()); err != nil {
			t.Fatalf("list output %q: %v", out, err)
		}
		if !slices.Equal(calls, []string{"label list", "label create"}) {
			t.Fatalf("list output %q: calls %v, want list then create", out, calls)
		}
	}
}

func TestEnsureLabel_ExistingLabelIsLeftAlone(t *testing.T) {
	var calls int
	g := &ghCLI{run: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		return []byte(`[{"name":"greenhouse"}]`), nil
	}}
	if err := g.EnsureLabel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("%d gh calls, want 1 (no create)", calls)
	}
}
