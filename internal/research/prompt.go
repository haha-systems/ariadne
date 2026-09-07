package research

// scopeNotice is prepended to every Ari and Astra prompt, unconditionally.
// Providers like Codex/Claude have their own general-purpose defaults
// (checking GitHub issues, Linear tickets, etc.) that make sense for the
// issue-driven coding workflow but actively mislead a research invocation —
// this notice overrides that instinct rather than leaving it to chance.
const scopeNotice = `SCOPE: This is a standalone research workflow, not the GitHub/Linear issue-driven coding workflow. Do not check GitHub issues, GitHub pull requests, or Linear tickets — they are unrelated to this project. Ghostdive (the frontier and evidence provided below) is the sole source of truth for research progress; do not assume progress that isn't reflected there.`

// envelopeProtocol tells the agent how to report back. Without this, an
// agent has no way to know it's expected to write a structured result at
// all — it would otherwise always be treated as StatusComplete (see
// invocation.ProviderRunner's backward-compatibility default for providers
// that never learned about envelopes).
const envelopeProtocol = `REPORTING: Before you finish, write a JSON object to the file path given in the $ARIADNE_OUTPUT_FILE environment variable, shaped like:
  {"status": "continue|escalate|complete|blocked", "summary": "...", "escalation_reason": "...", "frontier": "..."}
- status "continue": there is more routine work to do next cycle.
- status "escalate": you need a scarcer, more capable agent's reasoning to resolve a genuine scientific fork — set escalation_reason to explain why.
- status "complete": the research goal has been achieved.
- status "blocked": you cannot proceed without human input.
- frontier (optional): an updated, compressed summary of the project's current research state. When set, this becomes the new durable frontier every future cycle (and any other researcher reading Ghostdive) will see — include it whenever your understanding of the project's state has materially changed.`

// buildPreamble assembles the parts of a prompt that are the same for
// every invocation regardless of what Ari or Astra is being asked to do:
// the scope notice, optional project-specific guidance, and the envelope
// reporting protocol.
func buildPreamble(guidance string) string {
	s := scopeNotice + "\n\n"
	if guidance != "" {
		s += guidance + "\n\n---\n\n"
	}
	return s
}
