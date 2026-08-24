package controlplane

import (
	"context"
	"testing"
	"time"
)

// draftSession inserts one run and one draft session directly, so this task
// does not depend on the admission endpoint that Task 3 adds. These are
// store-level tests: they assert how a draft behaves, not how it was created.
func draftSession(t *testing.T, store *Store) (runID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	repository := registerTestRepository(t, store, "github.com/example/inert")
	taskID := seedTaskForTest(t, store, repository.ID)
	runID = "33000000-0000-4000-8000-000000000001"
	sessionID = "33000000-0000-4000-8000-000000000002"
	now := time.Now().UTC().UnixMilli()

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO runs(id, request_key, request_digest, task_id, task_snapshot,
		                 source, admitted_at, updated_at, pre_approved)
		VALUES (?, ?, X'00', ?, '{}', 'cockpit', ?, ?, 0)
	`, runID, "inert-"+runID, taskID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO sessions(id, run_id, repository_id, repository_identity,
		                     resolved_prompt, required_runtime, timeout_seconds,
		                     state, admitted_at, delivery)
		VALUES (?, ?, ?, ?, 'spec text', 'claude-code', 3600, 'draft', ?, 'pr')
	`, sessionID, runID, repository.ID, repository.RemoteIdentity, now); err != nil {
		t.Fatal(err)
	}
	return runID, sessionID
}

func TestDraftIsNotClaimable(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, "worker-draft", 2)
	_, sessionID := draftSession(t, store)

	claim, err := store.Claim(context.Background(), worker.ID, claimRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatalf("a draft session was claimed: %s", claim.Session.ID)
	}
	var state string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT state FROM sessions WHERE id = ?`, sessionID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "draft" {
		t.Fatalf("session state = %q, want draft", state)
	}
}

func TestDraftDoesNotMarkRunTerminal(t *testing.T) {
	store := newTestStore(t)
	runID, _ := draftSession(t, store)
	var terminalAt *int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT terminal_at FROM runs WHERE id = ?`, runID).Scan(&terminalAt); err != nil {
		t.Fatal(err)
	}
	if terminalAt != nil {
		t.Fatalf("runs.terminal_at = %d for a draft-only run, want NULL", *terminalAt)
	}
}

func TestDraftOnlyRunReportsDraftState(t *testing.T) {
	store := newTestStore(t)
	runID, _ := draftSession(t, store)
	detail, err := store.Run(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.State != "draft" {
		t.Fatalf("run state = %q, want draft", detail.Run.State)
	}
}
