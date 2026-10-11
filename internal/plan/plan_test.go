package plan

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// fixture replays testdata/bd-list.json, captured from `bd list --json`
// (plus a stage label, a foreign bead and a malformed ID).
func fixture(t *testing.T) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	out, err := os.ReadFile("testdata/bd-list.json")
	if err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, dir string, args ...string) ([]byte, error) {
		if dir != "/repo" {
			t.Errorf("bd ran in %q, want /repo", dir)
		}
		want := []string{"list", "--json", "--all", "--limit", "0"}
		if !reflect.DeepEqual(args, want) {
			t.Errorf("bd args = %q, want %q", args, want)
		}
		return out, nil
	}
}

func TestBDList(t *testing.T) {
	src := NewBD("/repo", "gr-boot")
	src.run = fixture(t)
	beads, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range beads {
		ids = append(ids, b.ID)
	}
	if want := []string{"gr-boot-1", "gr-boot-2", "gr-boot-8", "gr-boot-10"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %q, want %q (plan order, other plans dropped)", ids, want)
	}
	if got := beads[0]; got.Title != "Skeleton: config + status" || got.Status != "in_progress" {
		t.Errorf("gr-boot-1 = %+v", got)
	}
	if got := beads[1].Stage(); got != "implement" {
		t.Errorf("gr-boot-2 stage = %q, want implement", got)
	}
	if got := beads[2].Labels; !reflect.DeepEqual(got, []string{"factory:approved"}) {
		t.Errorf("gr-boot-8 labels = %q, want trimmed [factory:approved]", got)
	}
}

func TestBDListErrors(t *testing.T) {
	src := NewBD("/repo", "gr-boot")
	src.run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := src.List(context.Background()); err == nil {
		t.Error("want the bd error")
	}
	src.run = func(context.Context, string, ...string) ([]byte, error) { return []byte("not json"), nil }
	if _, err := src.List(context.Background()); err == nil {
		t.Error("want a parse error")
	}
}

func TestStage(t *testing.T) {
	tests := []struct {
		labels []string
		want   string
	}{
		{nil, ""},
		{[]string{"factory:approved"}, ""},
		{[]string{"factory:approved", "stage:review"}, "review"},
	}
	for _, tt := range tests {
		if got := (Bead{Labels: tt.labels}).Stage(); got != tt.want {
			t.Errorf("Stage(%q) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func TestFakeIsASource(t *testing.T) {
	var src Source = &Fake{Err: errors.New("down")}
	if _, err := src.List(context.Background()); err == nil {
		t.Error("want Fake.Err")
	}
}

func TestReady(t *testing.T) {
	dep := func(id string) []Dependency { return []Dependency{{DependsOn: id, Type: "blocks"}} }
	beads := []Bead{
		{ID: "p-1", Status: "closed"},
		{ID: "p-2", Status: "open", Dependencies: dep("p-1")},
		{ID: "p-3", Status: "open", Dependencies: dep("p-2")},
		{ID: "p-4", Status: "open", Dependencies: dep("x-9")},
		{ID: "p-5", Status: "in_progress"},
		{ID: "p-6", Status: "closed", Labels: []string{"stage:verify"}}, // closed in its unmerged PR
		{ID: "p-7", Status: "open", Dependencies: dep("p-6")},
	}
	for i, want := range []bool{false, true, false, false, false, false, false} {
		if got := Ready(beads[i], beads); got != want {
			t.Errorf("Ready(%s) = %v, want %v", beads[i].ID, got, want)
		}
	}
}

func TestBDSetStage(t *testing.T) {
	var calls []string
	b := &BD{Dir: "/r", Prefix: "p", run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return []byte(`[{"id":"p-1","status":"open","labels":["factory:approved","stage:spec"]}]`), nil
		}
		return nil, nil
	}}
	if err := b.SetStage(context.Background(), "p-1", "implement"); err != nil {
		t.Fatal(err)
	}
	want := "list --json --all --limit 0|label add p-1 stage:implement|label remove p-1 stage:spec"
	if got := strings.Join(calls, "|"); got != want {
		t.Errorf("calls %q\nwant  %q", got, want)
	}
}
