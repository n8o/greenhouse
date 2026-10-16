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

// The rejection reason is the latest word on the PR, a review or a comment
// (`gh pr close -c "why"` leaves a comment).
func TestParsePRReason(t *testing.T) {
	const review = `{"body":"wrong layer","submittedAt":"2026-10-12T10:00:00Z"}`
	const comment = `{"body":"use the adapter","createdAt":"2026-10-12T11:00:00Z"}`
	tests := []struct{ name, reviews, comments, want string }{
		{"review only", review, "", "wrong layer"},
		{"comment only", "", comment, "use the adapter"},
		{"both, the comment is later", review, comment, "use the adapter"},
		{"both, the review is later", `{"body":"wrong layer","submittedAt":"2026-10-12T12:00:00Z"}`, comment, "wrong layer"},
		{"empty bodies skipped", review + `,{"body":"  ","submittedAt":"2026-10-12T13:00:00Z"}`, `{"body":"","createdAt":"2026-10-12T14:00:00Z"}`, "wrong layer"},
		{"neither", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := `{"number":7,"state":"CLOSED","isDraft":false,"headRefName":"feature/x-1-implement","labels":[],` +
				`"reviews":[` + tt.reviews + `],"comments":[` + tt.comments + `]}`
			pr, err := parsePR([]byte(out))
			if err != nil {
				t.Fatal(err)
			}
			if pr.Reason != tt.want || pr.State != "CLOSED" || pr.HeadRef != "feature/x-1-implement" {
				t.Fatalf("got %+v, want reason %q", pr, tt.want)
			}
		})
	}
}

func TestPRByBranch(t *testing.T) {
	var args []string
	g := &ghCLI{run: func(_ context.Context, a ...string) ([]byte, error) {
		args = a
		return []byte(`[{"number":3,"state":"CLOSED","isDraft":true,"headRefName":"feature/x-1-learn","files":[]},
			{"number":9,"state":"OPEN","isDraft":true,"headRefName":"feature/x-1-learn",
			 "files":[{"path":".nugit/lessons/a.md","additions":12,"deletions":0}]}]`), nil
	}}
	pr, err := g.PRByBranch(context.Background(), "feature/x-1-learn")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 9 || pr.State != "OPEN" || !pr.IsDraft || !slices.Equal(pr.Files, []string{".nugit/lessons/a.md"}) {
		t.Fatalf("want the newest PR 9 with its files, got %+v", pr)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "pr list --head feature/x-1-learn --state all") {
		t.Fatalf("gh %s", got)
	}
	for _, out := range []string{"", "[]"} {
		g.run = func(context.Context, ...string) ([]byte, error) { return []byte(out), nil }
		if pr, err := g.PRByBranch(context.Background(), "b"); err != nil || pr.Number != 0 {
			t.Fatalf("output %q: %+v %v, want no PR", out, pr, err)
		}
	}
}
