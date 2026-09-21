package claude

import (
	"ds2api/internal/httpapi/openai/shared"
)

// cleanVisibleOutput is the Claude adapter for the shared visible-text
// sanitizer. It must stay a thin delegation: citation stripping AND the DSML /
// role-marker / think-tag leak sanitizer are shared business behavior (see
// AGENTS.md Protocol Adapter Boundary), so the same leaked markup that the
// OpenAI surfaces strip is stripped here too.
func cleanVisibleOutput(text string, stripReferenceMarkers bool) string {
	return shared.CleanVisibleOutput(text, stripReferenceMarkers)
}
