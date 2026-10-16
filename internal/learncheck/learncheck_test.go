package learncheck

import (
	"strings"
	"testing"
)

func lesson(status string) string {
	return "---\nschema_version: 1\nid: LESSON-x\ntype: lesson\nscope: cli\nstatus: " + status +
		"\ncreated: 2026-10-16T00:00:00Z\nprovenance:\n  commit: abc123\nconfidence: low\n---\n\n# Lesson — x\n"
}

func TestCheck(t *testing.T) {
	good := File{Path: ".nugit/lessons/x.md", Added: true, Content: lesson("proposed")}
	tests := []struct {
		name        string
		files       []File
		draft, open bool
		want        string // substring of the error; "" means it passes
	}{
		{"pass", []File{good}, true, true, ""},
		{"pass with an edited lesson too", []File{good, {Path: ".nugit/lessons/y.md"}}, true, true, ""},
		{"quoted status", []File{{Path: good.Path, Added: true, Content: lesson(`"proposed"`)}}, true, true, ""},
		{"closed", []File{good}, true, false, "not open"},
		{"ready, not draft", []File{good}, false, true, "not a draft"},
		{"touches a non-lessons path", []File{good, {Path: "cmd/greenhouse/tick.go"}}, true, true, "outside .nugit/lessons/"},
		{"status active", []File{{Path: good.Path, Added: true, Content: lesson("active")}}, true, true, `status "active"`},
		{"no front matter", []File{{Path: good.Path, Added: true, Content: "# Lesson\nstatus: proposed\n"}}, true, true, `status ""`},
		{"zero lesson files", nil, true, true, "adds no lesson"},
		{"only edits a lesson", []File{{Path: ".nugit/lessons/y.md"}}, true, true, "adds no lesson"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.files, tt.draft, tt.open)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("want pass, got %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}
