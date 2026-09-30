package gate

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// scripted returns a CLI whose commands answer from outs, keyed by
// "name arg...", and records each call.
func scripted(outs map[string][]byte, errs map[string]error, calls *[]string) *CLI {
	return &CLI{Repo: "/repo", run: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		key := name + " " + strings.Join(args, " ")
		*calls = append(*calls, dir+"$ "+key)
		return outs[key], errs[key]
	}}
}

const (
	usageCmd = "agent-deck usage --json"
	listCmd  = "agent-deck list --json"
	prCmd    = "gh pr list --label greenhouse --state open --json number --limit 1000"
)

func TestCLIUsage(t *testing.T) {
	tests := []struct {
		file string
		want *Reading
	}{
		{"usage.json", &Reading{
			FiveHour:  &Window{UsedPercentage: 42.5, ResetsAt: time.Unix(1790810000, 0)},
			SevenDay:  &Window{UsedPercentage: 31, ResetsAt: time.Unix(1791300000, 0)},
			UpdatedAt: time.Unix(1790800000, 0),
		}},
		{"usage-empty.json", nil},
		{"usage-stale.json", &Reading{
			FiveHour:  &Window{UsedPercentage: 12, ResetsAt: time.Unix(1790810000, 0)},
			UpdatedAt: time.Unix(1790790000, 0),
			Stale:     true,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			var calls []string
			c := scripted(map[string][]byte{usageCmd: testdata(t, tt.file)}, nil, &calls)
			got, err := c.Usage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !sameReading(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if !slices.Equal(calls, []string{"$ " + usageCmd}) {
				t.Errorf("calls %q", calls)
			}
		})
	}
}

func sameReading(a, b *Reading) bool {
	if a == nil || b == nil {
		return a == b
	}
	sameWin := func(x, y *Window) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.UsedPercentage == y.UsedPercentage && x.ResetsAt.Equal(y.ResetsAt)
	}
	return sameWin(a.FiveHour, b.FiveHour) && sameWin(a.SevenDay, b.SevenDay) &&
		a.UpdatedAt.Equal(b.UpdatedAt) && a.Stale == b.Stale
}

// The testdata readings drive the gate end to end.
func TestQuotaFromTestdata(t *testing.T) {
	var calls []string
	for file, reactive := range map[string]bool{"usage.json": false, "usage-empty.json": true, "usage-stale.json": true} {
		c := scripted(map[string][]byte{usageCmd: testdata(t, file)}, nil, &calls)
		r, err := c.Usage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if d := Quota(r, Park{}, caps, now); !d.Allow || d.Reactive != reactive {
			t.Errorf("%s: got %+v, want allow with reactive=%v", file, d, reactive)
		}
	}
}

func TestCLIImplementSessions(t *testing.T) {
	var calls []string
	c := scripted(map[string][]byte{listCmd: testdata(t, "list.json")}, nil, &calls)
	n, err := c.ImplementSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// running, waiting, starting and the one in a greenhouse subgroup.
	if n != 4 {
		t.Errorf("counted %d implement sessions, want 4", n)
	}
}

func TestCLIOpenPRs(t *testing.T) {
	var calls []string
	c := scripted(map[string][]byte{prCmd: []byte(`[{"number":7},{"number":9}]`)}, nil, &calls)
	n, err := c.OpenPRs(context.Background())
	if err != nil || n != 2 {
		t.Errorf("got %d, %v; want 2", n, err)
	}
	if !slices.Equal(calls, []string{"/repo$ " + prCmd}) {
		t.Errorf("calls %q", calls)
	}
}

func TestCLIErrors(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()
	var calls []string
	failing := scripted(nil, map[string]error{usageCmd: boom, listCmd: boom, prCmd: boom}, &calls)
	if _, err := failing.Usage(ctx); !errors.Is(err, boom) {
		t.Errorf("Usage err %v", err)
	}
	if _, err := failing.ImplementSessions(ctx); !errors.Is(err, boom) {
		t.Errorf("ImplementSessions err %v", err)
	}
	if _, err := failing.OpenPRs(ctx); !errors.Is(err, boom) {
		t.Errorf("OpenPRs err %v", err)
	}
	garbage := scripted(map[string][]byte{usageCmd: []byte("nope"), listCmd: []byte("{"), prCmd: []byte("")}, nil, &calls)
	for name, err := range map[string]error{
		"usage": second(garbage.Usage(ctx)),
		"list":  second(garbage.ImplementSessions(ctx)),
		"prs":   second(garbage.OpenPRs(ctx)),
	} {
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Errorf("%s: err %v, want a parse error", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestGather(t *testing.T) {
	boom := errors.New("boom")
	r := reading(1, 2)
	in := Gather(context.Background(), &Fake{Reading: r, Implement: 1, Open: 2, OpenErr: boom}, now)
	if in.Reading != r || in.Implement != 1 || in.OpenPRs != 2 || in.OpenPRsErr != boom || !in.Now.Equal(now) {
		t.Errorf("got %+v", in)
	}
}
