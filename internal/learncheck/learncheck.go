// Package learncheck is the deterministic check of a learn session's output
// (ADR-0001): the lesson it proposes is a draft PR that adds proposed
// lessons under LessonsDir and changes nothing else. It is pure: the caller
// reads the PR and its files.
package learncheck

import (
	"fmt"
	"strings"
)

// LessonsDir is where a learn PR may change files.
const LessonsDir = ".nugit/lessons/"

// File is one file a learn PR changes.
type File struct {
	Path    string
	Added   bool   // new in the PR, not on the base
	Content string // its content on the PR's head; read for added files
}

// Check returns why a learn PR with files, draft and open as given, is not
// an acceptable lesson proposal, or nil if it is.
func Check(files []File, draft, open bool) error {
	if !open {
		return fmt.Errorf("the learn PR is not open")
	}
	if !draft {
		return fmt.Errorf("the learn PR is not a draft; the human marks a lesson ready by ratifying it")
	}
	added := 0
	for _, f := range files {
		if !strings.HasPrefix(f.Path, LessonsDir) {
			return fmt.Errorf("the learn PR changes %s, outside %s", f.Path, LessonsDir)
		}
		if !f.Added {
			continue
		}
		added++
		if st := status(f.Content); st != "proposed" {
			return fmt.Errorf("lesson %s has status %q, want proposed; only the human ratifies a lesson", f.Path, st)
		}
	}
	if added == 0 {
		return fmt.Errorf("the learn PR adds no lesson under %s", LessonsDir)
	}
	return nil
}

// status is the top-level status: of content's YAML front matter, "" if it
// has none.
func status(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(l, "status:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
