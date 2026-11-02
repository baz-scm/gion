package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Agents add --md to every command (the skill tells them to), so every
// command must accept it, even ones whose output it can't change.
func TestEveryCommandAcceptsMD(t *testing.T) {
	old := helpOut
	defer func() { helpOut = old }()
	for name, fn := range commands() {
		var buf bytes.Buffer
		helpOut = &buf
		if err := fn(context.Background(), []string{"-h"}); err != errHelpShown {
			t.Errorf("%s -h: got %v, want errHelpShown", name, err)
			continue
		}
		if !strings.Contains(buf.String(), "--md") {
			t.Errorf("%s does not accept --md:\n%s", name, buf.String())
		}
	}
}
