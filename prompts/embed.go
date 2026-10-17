// Package prompts holds the stage prompt templates, embedded in the binary
// so greenhouse runs on any repo without a checkout of its own (ADR-0011).
// greenhouse.toml's `prompts` key points at a directory that replaces them.
package prompts

import "embed"

// FS is the stage templates: <stage>.md, retry.md and contract.md.
//
//go:embed *.md
var FS embed.FS
