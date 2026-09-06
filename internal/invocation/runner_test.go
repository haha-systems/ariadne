package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/haha-systems/ariadne/internal/provider"
)

// fakeHandle is a no-op provider.RunHandle.
type fakeHandle struct{ err error }

func (h fakeHandle) Wait() error   { return h.err }
func (h fakeHandle) Cancel() error { return nil }

// fakeProvider is a hand-written fake provider.AgentProvider (no mocking
// framework, matching the repo's existing test conventions). writeEnvelope,
// when set, is written to rc.OutputFile as part of Run — simulating an agent
// that produces a structured result.
type fakeProvider struct {
	name          string
	writeEnvelope *Envelope
	runErr        error
	waitErr       error
	gotRunContext provider.RunContext
}

func (p *fakeProvider) Name() string                        { return p.name }
func (p *fakeProvider) Capabilities() []provider.Capability { return nil }
func (p *fakeProvider) CostEstimate(int) (float64, bool)    { return 0, false }
func (p *fakeProvider) Run(_ context.Context, rc provider.RunContext) (provider.RunHandle, error) {
	p.gotRunContext = rc
	if p.runErr != nil {
		return nil, p.runErr
	}
	if p.writeEnvelope != nil {
		data, err := json.Marshal(p.writeEnvelope)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(rc.OutputFile, data, 0600); err != nil {
			return nil, err
		}
	}
	return fakeHandle{err: p.waitErr}, nil
}

func TestProviderRunner_Invoke_ParsesEnvelope(t *testing.T) {
	dir := t.TempDir()
	want := Envelope{Status: StatusEscalate, Summary: "two hypotheses survive", EscalationReason: "need a discriminating experiment"}
	p := &fakeProvider{name: "fake", writeEnvelope: &want}
	r := ProviderRunner{Provider: p, RepoPath: dir}

	got, err := r.Invoke(context.Background(), Request{Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Invoke() = %+v, want %+v", got, want)
	}

	// The prompt should have been written to a task file inside RepoPath.
	if p.gotRunContext.TaskFile == "" {
		t.Fatal("expected TaskFile to be set")
	}
	data, err := os.ReadFile(p.gotRunContext.TaskFile)
	if err != nil {
		t.Fatalf("reading task file: %v", err)
	}
	if string(data) != "do the thing" {
		t.Errorf("task file contents = %q, want %q", data, "do the thing")
	}
}

func TestProviderRunner_Invoke_NoEnvelopeMeansComplete(t *testing.T) {
	dir := t.TempDir()
	p := &fakeProvider{name: "fake"} // never writes an output file
	r := ProviderRunner{Provider: p, RepoPath: dir}

	got, err := r.Invoke(context.Background(), Request{Prompt: "legacy issue task"})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got.Status != StatusComplete {
		t.Errorf("Status = %q, want %q (backward compatibility with providers that don't emit envelopes)", got.Status, StatusComplete)
	}
}

func TestProviderRunner_Invoke_LaunchErrorWrapped(t *testing.T) {
	dir := t.TempDir()
	launchErr := fmt.Errorf("binary not found")
	p := &fakeProvider{name: "fake", runErr: launchErr}
	r := ProviderRunner{Provider: p, RepoPath: dir}

	_, err := r.Invoke(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, launchErr) {
		t.Errorf("expected wrapped launch error, got %v", err)
	}
}

func TestProviderRunner_Invoke_WaitErrorWrapped(t *testing.T) {
	dir := t.TempDir()
	waitErr := fmt.Errorf("exit status 1")
	p := &fakeProvider{name: "fake", waitErr: waitErr}
	r := ProviderRunner{Provider: p, RepoPath: dir}

	_, err := r.Invoke(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, waitErr) {
		t.Errorf("expected wrapped wait error, got %v", err)
	}
}

func TestProviderRunner_Invoke_CustomOutputFile(t *testing.T) {
	dir := t.TempDir()
	want := Envelope{Status: StatusComplete, Summary: "done"}
	p := &fakeProvider{name: "fake", writeEnvelope: &want}
	r := ProviderRunner{Provider: p, RepoPath: dir}

	customPath := filepath.Join(dir, "custom-envelope.json")
	got, err := r.Invoke(context.Background(), Request{Prompt: "x", OutputFile: customPath})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Invoke() = %+v, want %+v", got, want)
	}
	if p.gotRunContext.OutputFile != customPath {
		t.Errorf("OutputFile = %q, want %q", p.gotRunContext.OutputFile, customPath)
	}
}
