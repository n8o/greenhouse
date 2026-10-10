package verify

import (
	"context"
	"strings"
)

// Reply is a canned response of a Fake.
type Reply struct {
	Out string
	Err error
	// Block makes Run wait for ctx to end, as a hung command does.
	Block bool
}

// Fake is an in-memory Runner for tests. It answers by the command line
// ("git merge-base origin/main HEAD", "sh -c go test ./...") and records
// every call; a command it has no reply for succeeds with no output.
type Fake struct {
	Replies map[string]Reply
	Calls   []string
}

// Run implements Runner.
func (f *Fake) Run(ctx context.Context, _, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.Calls = append(f.Calls, key)
	rep := f.Replies[key]
	if rep.Block {
		<-ctx.Done()
		return []byte(rep.Out), ctx.Err()
	}
	return []byte(rep.Out), rep.Err
}
