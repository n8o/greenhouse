package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// PlanDir holds the exported plan (ADR-0005).
const PlanDir = ".beads/plans/"

// closeFields are the fields that closing a bead may add or change. Anything
// else on the bead's line is a plan field (title, description, dependencies,
// priority...) and must stay as it was. started_at comes along because bd
// stamps it when the bead first goes in progress, and the export keeps it.
var closeFields = []string{"status", "close_reason", "closed_at", "closed_by", "started_at"}

// CheckPlanScope compares the PR's diff under .beads/plans/ with the merge
// base and returns one message per violation, naming the bead ids. It
// passes (returns nil) only if the sole change is bead's own line, closing
// it. The error is non-nil only when git could not answer.
func CheckPlanScope(ctx context.Context, r Runner, dir, base, bead string) ([]string, error) {
	git := func(args ...string) (string, error) {
		out, err := r.Run(ctx, dir, "git", args...)
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	mb, err := git("merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	mb = strings.TrimSpace(mb)
	names, err := git("diff", "--name-status", "--no-renames", mb, "HEAD", "--", PlanDir)
	if err != nil {
		return nil, err
	}
	var before, after [][]byte
	for line := range strings.Lines(names) {
		status, path, ok := strings.Cut(strings.TrimRight(line, "\n"), "\t")
		if !ok {
			continue
		}
		var b, a string
		if status != "A" {
			if b, err = git("show", mb+":"+path); err != nil {
				return nil, err
			}
		}
		if status != "D" {
			if a, err = git("show", "HEAD:"+path); err != nil {
				return nil, err
			}
		}
		before, after = append(before, []byte(b)), append(after, []byte(a))
	}
	return planScope(bead, before, after), nil
}

// planScope is CheckPlanScope on the contents of the changed plan files:
// before[i] and after[i] are one file at the merge base and at HEAD, empty
// when the file was added or deleted.
func planScope(bead string, before, after [][]byte) []string {
	old, bad := beadLines(before)
	cur, bad2 := beadLines(after)
	var out []string
	for _, id := range slices.Sorted(maps.Keys(old)) {
		if _, ok := cur[id]; !ok {
			out = append(out, id+": removed")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(cur)) {
		o, existed := old[id]
		switch {
		case !existed:
			out = append(out, id+": added")
		case bytes.Equal(canon(o), canon(cur[id])):
		case id != bead:
			out = append(out, id+": changed (only "+bead+" may change)")
		default:
			if why := notClose(o, cur[id]); why != "" {
				out = append(out, id+": "+why)
			}
		}
	}
	if o, ok := old[bead]; ok && bytes.Equal(canon(o), canon(cur[bead])) || !ok && cur[bead] == nil {
		out = append(out, bead+": not closed by this PR")
	}
	return slices.Concat(out, bad, bad2)
}

// beadLines indexes the JSONL files by bead id. A line with no readable id
// is reported rather than skipped, so it cannot hide a change.
func beadLines(files [][]byte) (map[string]map[string]any, []string) {
	m := map[string]map[string]any{}
	var bad []string
	for _, f := range files {
		for line := range bytes.Lines(f) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var b map[string]any
			if err := json.Unmarshal(line, &b); err != nil {
				bad = append(bad, "unparseable line under "+PlanDir)
				continue
			}
			if id, _ := b["id"].(string); id == "" {
				bad = append(bad, "line without an id under "+PlanDir)
				continue
			}
			m[b["id"].(string)] = b
		}
	}
	return m, bad
}

func canon(b map[string]any) []byte {
	out, _ := json.Marshal(b) // map keys marshal sorted
	return out
}

// notClose returns why cur is not just old closed, or "" if it is.
func notClose(old, cur map[string]any) string {
	if old["status"] == "closed" {
		return "was already closed, so it may not change"
	}
	if cur["status"] != "closed" {
		return fmt.Sprintf("changed without closing (status %v)", cur["status"])
	}
	var plan []string
	for _, k := range slices.Sorted(maps.Keys(union(old, cur))) {
		if slices.Contains(closeFields, k) {
			continue
		}
		if !bytes.Equal(canon(map[string]any{k: old[k]}), canon(map[string]any{k: cur[k]})) {
			plan = append(plan, k)
		}
	}
	if len(plan) > 0 {
		return "plan fields changed: " + strings.Join(plan, ", ")
	}
	return ""
}

func union(a, b map[string]any) map[string]any {
	u := maps.Clone(a)
	maps.Copy(u, b)
	return u
}
