package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/research/prompts"
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
// escalation: the current frontier, cited evidence, and the escalation
// request that triggered it. It intentionally excludes the raw conversation
// history.
type Packet struct {
	Project string
	// Request is Ari's structured escalation, carrying the question Astra
	// must resolve and why routine work could not.
	Request  invocation.Escalation
	Evidence []memory.Record
	Frontier memory.Record
	// Guidance is the same durable, project-specific instruction text given
	// to Ari (see AriLoop.Guidance) — Astra needs the scope notice and
	// team mandate just as much as Ari does, and it is fenced the same way
	// so it cannot redefine Astra's role either.
	Guidance string
	// ApprovalReason records the QAC decision that authorized this
	// invocation, so the approval is stated in the prompt rather than
	// implied by the fact that Astra is running at all.
	ApprovalReason string
}

// BuildPacket assembles a Packet from Ghostdive (or whichever memory.Store
// is configured): the project's current frontier (if any) and the evidence
// records the escalation cites. A missing frontier is not an error — early
// in a project's life there may be none yet.
func BuildPacket(ctx context.Context, store memory.Store, project string, req invocation.Escalation, guidance, approvalReason string) (Packet, error) {
	frontier, _ := store.Frontier(ctx, project) // absent frontier is fine, zero value renders as nothing

	var evidence []memory.Record
	if len(req.EvidenceRefs) > 0 {
		refs := req.EvidenceRefs
		if len(refs) > maxEvidenceItems {
			refs = refs[:maxEvidenceItems]
		}
		ev, err := store.GetEvidence(ctx, project, refs)
		if err != nil {
			return Packet{}, fmt.Errorf("research: build packet: %w", err)
		}
		evidence = ev
	}

	return Packet{
		Project:        project,
		Request:        req,
		Evidence:       evidence,
		Frontier:       frontier,
		Guidance:       guidance,
		ApprovalReason: approvalReason,
	}, nil
}

// Render formats the Packet as Astra's bounded-length prompt.
func (p Packet) Render() string {
	evidence := make([]string, 0, len(p.Evidence))
	for _, e := range p.Evidence {
		evidence = append(evidence, fmt.Sprintf("- [%s] %s: %s", e.Kind, e.Subject, truncate(e.Summary, maxEvidenceChars)))
	}

	approval := "This escalation was approved by Ariadne's QAC scheduler."
	if reason := strings.TrimSpace(p.ApprovalReason); reason != "" {
		approval += "\n\nScheduler's stated reason: " + reason
	}
	if n := p.Request.FailedLocalAttempts; n > 0 {
		approval += fmt.Sprintf("\n\nAri reports %d failed local attempt(s) on this question.", n)
	}

	return prompts.BuildAstra(prompts.AstraInput{
		Project:      p.Project,
		Guidance:     p.Guidance,
		Question:     p.Request.Question,
		Reason:       p.Request.Reason,
		Frontier:     truncate(p.Frontier.Summary, maxFrontierChars),
		Evidence:     evidence,
		ApprovalNote: approval,
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
