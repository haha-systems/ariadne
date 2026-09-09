package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/session"
)

// limitedController is a session with only a log surface — the shape of an
// issue run. It exists to prove the interface gates on capabilities rather
// than on kind: the surfaces it lacks must be refused with an explanation,
// not offered and then silently fail.
type limitedController struct {
	*fakeController
	desc session.Descriptor
}

func (c *limitedController) Describe(context.Context) (session.Descriptor, error) {
	return c.desc, nil
}

func (c *limitedController) Capabilities() session.Capabilities {
	return session.NewCapabilities(session.CapAgentLog)
}

func newHarnessModel(t *testing.T) (*Model, *session.Registry, *fakeController, *limitedController) {
	t.Helper()

	registry := session.NewRegistry()

	research := newFakeController()
	researchDesc := session.Descriptor{
		ID: "research:arachne", Kind: session.KindResearch,
		Title: "arachne", State: control.RunStateRunning, Summary: "cycle 14 · ari",
	}
	adapted, err := session.Adapt(researchDesc, session.ResearchCapabilities(), research)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if err := registry.Register(researchDesc, adapted); err != nil {
		t.Fatalf("Register research: %v", err)
	}

	issueDesc := session.Descriptor{
		ID: "issue:run_88", Kind: session.KindIssue,
		Title: "fix login redirect", State: control.RunStateRunning, Summary: "running on claude",
	}
	issue := &limitedController{fakeController: newFakeController(), desc: issueDesc}
	if err := registry.Register(issueDesc, issue); err != nil {
		t.Fatalf("Register issue: %v", err)
	}

	m := NewHarness(context.Background(), registry)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	runCmd(t, m, m.Init())
	return m, registry, research, issue
}

func TestHarnessOpensOnSessionListShowingEveryKind(t *testing.T) {
	m, _, _, _ := newHarnessModel(t)

	if m.view != viewSessions {
		t.Fatalf("view = %v, want the session list", m.view)
	}
	if len(m.data.sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(m.data.sessions))
	}

	out := m.render()
	for _, want := range []string{"arachne", "fix login redirect", "RESEARCH", "ISSUE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("session list is missing %q:\n%s", want, out)
		}
	}
}

func TestHarnessAttachesToSelectedSession(t *testing.T) {
	m, _, _, _ := newHarnessModel(t)

	// The list sorts active sessions by recency; attach to whichever the
	// cursor starts on and assert the model followed it.
	want, ok := m.sessionsScreen.selected(&m.data)
	if !ok {
		t.Fatal("no session selected")
	}

	press(t, m, "enter")

	if m.activeID != want.ID {
		t.Fatalf("activeID = %q, want %q", m.activeID, want.ID)
	}
	if !m.attached() {
		t.Fatal("model is not attached after enter")
	}
	if m.view != viewSessions && m.view != viewResearch {
		t.Fatalf("view = %v, want the session's own view after attaching", m.view)
	}
}

// Switching sessions must not blend two sessions' output together.
func TestSwitchingSessionsClearsBufferedData(t *testing.T) {
	m, _, _, _ := newHarnessModel(t)

	press(t, m, "enter")
	first := m.activeID

	m.appendEvent(control.Event{Name: "left-over"})
	m.appendLog(control.AgentLogEntry{Agent: "ari", Text: "left-over line"})

	press(t, m, "0")
	press(t, m, "j")
	press(t, m, "enter")

	if m.activeID == first {
		t.Skip("only one session was selectable; nothing to switch to")
	}
	for _, ev := range m.data.events {
		if ev.Name == "left-over" {
			t.Fatal("events from the previous session survived the switch")
		}
	}
	for _, entry := range m.data.logs {
		if entry.Text == "left-over line" {
			t.Fatal("logs from the previous session survived the switch")
		}
	}
}

// The point of Capabilities: an issue run has no memory or QAC, and the
// interface must say so rather than offering a key that does nothing.
func TestCapabilityGatingRefusesUnsupportedSurfaces(t *testing.T) {
	m, _, _, _ := newHarnessModel(t)

	if cmd := m.attach("issue:run_88"); cmd != nil {
		runCmd(t, m, cmd)
	}
	if m.activeID != "issue:run_88" {
		t.Fatalf("activeID = %q, want issue:run_88", m.activeID)
	}

	for _, tc := range []struct{ key, want string }{
		{"5", "memory"},
		{"4", "escalations"},
		{"s", "steering"},
		{"p", "pause"},
	} {
		m.alert = ""
		press(t, m, tc.key)
		if !strings.Contains(m.alert, tc.want) {
			t.Fatalf("key %q: alert = %q, want it to mention %q", tc.key, m.alert, tc.want)
		}
	}

	// The surface it does have must still work.
	m.alert = ""
	press(t, m, "2")
	if m.view != viewAgents {
		t.Fatalf("view = %v, want the agents view, which an issue run supports", m.view)
	}
}

// A research session keeps every surface it had before sessions existed.
func TestResearchSessionKeepsEverySurface(t *testing.T) {
	m, _, research, _ := newHarnessModel(t)

	if cmd := m.attach("research:arachne"); cmd != nil {
		runCmd(t, m, cmd)
	}

	m.alert = ""
	press(t, m, "p")
	if strings.Contains(m.alert, "no pause") {
		t.Fatalf("pause was refused on a research session: %q", m.alert)
	}

	var paused bool
	for _, cmd := range research.sentCommands() {
		if control.CommandName(cmd) == "pause" {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("research session never received the pause command; got %v", research.sentCommands())
	}
}

// A run that finishes and is unregistered must not leave the interface
// rendering a controller the host has disowned.
func TestRemovedSessionDetachesTheInterface(t *testing.T) {
	m, registry, _, _ := newHarnessModel(t)

	if cmd := m.attach("issue:run_88"); cmd != nil {
		runCmd(t, m, cmd)
	}

	registry.Unregister("issue:run_88")
	m.Update(SessionChangeMsg{Change: session.Change{
		Kind:       session.ChangeRemoved,
		Descriptor: session.Descriptor{ID: "issue:run_88", Title: "fix login redirect"},
	}})

	if m.attached() {
		t.Fatal("still attached to a session the host removed")
	}
	if m.view != viewSessions {
		t.Fatalf("view = %v, want the session list after the session ended", m.view)
	}
	if !strings.Contains(m.alert, "ended") {
		t.Fatalf("alert = %q, want it to say the session ended", m.alert)
	}
	for _, d := range m.data.sessions {
		if d.ID == "issue:run_88" {
			t.Fatal("removed session is still in the list")
		}
	}
}

// An added session must appear without the user re-listing.
func TestAddedSessionAppearsLive(t *testing.T) {
	m, _, _, _ := newHarnessModel(t)

	m.Update(SessionChangeMsg{Change: session.Change{
		Kind: session.ChangeAdded,
		Descriptor: session.Descriptor{
			ID: "issue:run_89", Kind: session.KindIssue,
			Title: "add retry backoff", State: control.RunStateRunning,
		},
	}})

	if len(m.data.sessions) != 3 {
		t.Fatalf("session count = %d, want 3 after an add", len(m.data.sessions))
	}
	if !strings.Contains(m.render(), "add retry backoff") {
		t.Fatal("newly added session is not rendered")
	}
}

// With nothing attached the harness must render and stay usable rather than
// dereferencing a controller that is not there.
func TestEmptyHarnessRendersAndDoesNotPanic(t *testing.T) {
	registry := session.NewRegistry()
	m := NewHarness(context.Background(), registry)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	runCmd(t, m, m.Init())

	out := m.render()
	if !strings.Contains(out, "no session") {
		t.Fatalf("empty harness should say it has no session:\n%s", out)
	}

	for _, key := range []string{"1", "2", "s", "p", "r", "a", "j", "enter", "tab"} {
		press(t, m, key)
	}
	if m.attached() {
		t.Fatal("empty harness reports an attached session")
	}
}

// A harness holding exactly one session should not make the user pick it
// out of a list of one.
func TestSingleSessionAutoAttaches(t *testing.T) {
	registry := session.NewRegistry()
	desc := session.Descriptor{
		ID: "research:solo", Kind: session.KindResearch,
		Title: "solo", State: control.RunStateRunning,
	}
	adapted, err := session.Adapt(desc, session.ResearchCapabilities(), newFakeController())
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if err := registry.Register(desc, adapted); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m := NewHarness(context.Background(), registry)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	runCmd(t, m, m.Init())

	if !m.attached() || m.activeID != "research:solo" {
		t.Fatalf("attached=%v activeID=%q, want an automatic attach to the only session",
			m.attached(), m.activeID)
	}
}

// The pre-harness entry point must keep working unchanged.
func TestSingleControllerEntryPointStillAttaches(t *testing.T) {
	m := newTestModel(t, newFakeController())

	if !m.attached() {
		t.Fatal("New(ctx, controller) produced an unattached model")
	}
	if m.view != viewResearch {
		t.Fatalf("view = %v, want the research view for a single-controller model", m.view)
	}
	if !m.can(session.CapSteer) || !m.can(session.CapMemory) {
		t.Fatal("a single research controller lost its capabilities")
	}
}
