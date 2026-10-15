package cli

import "strings"

var guide string

// SetGuide sets the agent guide (the skill file) printed by `help --md`.
func SetGuide(g string) { guide = g }

// agentGuide returns the guide without its YAML front matter.
func agentGuide() string {
	if rest, ok := strings.CutPrefix(guide, "---\n"); ok {
		if _, body, ok := strings.Cut(rest, "\n---\n"); ok {
			return strings.TrimLeft(body, "\n")
		}
	}
	return guide
}
