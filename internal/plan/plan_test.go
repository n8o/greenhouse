package plan

import (
	"context"
	"errors"
	"os"
	"reflect"
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
