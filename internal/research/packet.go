package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/haha-systems/ariadne/internal/memory"
)

// Bounds keep the packet a short, deliberately compact prompt rather than
// the full accumulated conversation — Astra is scarce, so every packet
// should cost as little of its context as possible.
const (
	maxFrontierChars = 2000
	maxEvidenceItems = 20
	maxEvidenceChars = 300
)

// Packet is the compact research context handed to Astra for one
// escalation: the current frontier, cited evidence, and the question that
// triggered the escalation. It intentionally excludes the raw conversation
// history.
type Packet struct {
	Project  string
	Question string
	Evidence []memory.Record
	Frontier memory.Record
}

// BuildPacket assembles a Packet from Ghostdive (or whichever memory.Store
// is configured): the project's current frontier (if any) and the cited
// evidence records. A missing frontier is not an error — early in a
// project's life there may be none yet.
func BuildPacket(ctx context.Context, store memory.Store, project, question string, evidenceRefs []string) (Packet, error) {
	frontier, _ := store.Frontier(ctx, project) // absent frontier is fine, zero value renders as nothing

	var evidence []memory.Record
	if len(evidenceRefs) > 0 {
		refs := evidenceRefs
		if len(refs) > maxEvidenceItems {
			refs = refs[:maxEvidenceItems]
		}
		ev, err := store.GetEvidence(ctx, project, refs)
		if err != nil {
			return Packet{}, fmt.Errorf("research: build packet: %w", err)
		}
		evidence = ev
	}

	return Packet{Project: project, Question: question, Evidence: evidence, Frontier: frontier}, nil
}

// Render formats the Packet as bounded-length prompt text.
func (p Packet) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "GOAL\n\n%s\n\n", p.Question)

	if p.Frontier.Summary != "" {
		fmt.Fprintf(&b, "FRONTIER\n\n%s\n\n", truncate(p.Frontier.Summary, maxFrontierChars))
	}

	if len(p.Evidence) > 0 {
		b.WriteString("EVIDENCE\n\n")
		for _, e := range p.Evidence {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", e.Kind, e.Subject, truncate(e.Summary, maxEvidenceChars))
		}
		b.WriteString("\n")
	}

	b.WriteString("TASK\n\nDesign the cheapest experiment that maximally resolves the goal above.\n")
	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
