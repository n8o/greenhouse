package launch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n8o/greenhouse/internal/config"
	"github.com/n8o/greenhouse/internal/stage"
)

// TestE2ESpec launches one real spec session through agent-deck and waits
// for its parsed result. It spends quota, so it runs only when asked:
//
//	GREENHOUSE_E2E_BEAD=<throwaway bead> GREENHOUSE_E2E_TITLE=... \
//	GREENHOUSE_E2E_DONE_WHEN=... go test -run TestE2ESpec -v -timeout 20m ./internal/launch
//
// It pins the cheapest model that can run in auto mode (sonnet; haiku
// cannot) at low effort with a small turn budget. The
// session, its worktree and the bead are left for the operator to inspect
// and remove.
func TestE2ESpec(t *testing.T) {
	bead := os.Getenv("GREENHOUSE_E2E_BEAD")
	if bead == "" {
		t.Skip("set GREENHOUSE_E2E_BEAD to launch a real spec session")
	}
	cfg, err := config.Load(filepath.Join("..", "..", config.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckMCPApproved(cfg.Repo); err != nil {
		t.Fatal(err)
	}
	cfg.Stages[string(stage.Spec)] = config.Stage{Model: "sonnet", Effort: "low", MaxTurns: 8}
	l := New(cfg, NewCLI())
	ctx := context.Background()

	run, err := l.Launch(ctx, Request{Bead: bead, Stage: stage.Spec,
		Title: os.Getenv("GREENHOUSE_E2E_TITLE"), DoneWhen: os.Getenv("GREENHOUSE_E2E_DONE_WHEN")})
	if err != nil {
		t.Fatalf("launch: %v (run %+v)", err, run)
	}
	t.Logf("launched %s (%s) model=%s effort=%s resent=%v", run.Title, run.ID, run.Model, run.Effort, run.Resent)

	for deadline := time.Now().Add(15 * time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Second) {
		ev, done, err := l.Result(ctx, run.ID, stage.Spec)
		if !done && err == nil {
			continue
		}
		if err != nil {
			t.Fatalf("result: %v", err)
		}
		t.Logf("event: %+v", ev)
		return
	}
	t.Fatal("no result within 15m")
}
