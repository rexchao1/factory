package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func admitForTest(
	t *testing.T, store *Store, source protocol.WorkSource, preApproved bool, requestKey string,
) (protocol.AdmitWorkResponse, error) {
	t.Helper()
	repository := registerTestRepository(t, store, "github.com/example/scratch")
	return firstTwo(store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  requestKey,
		Repository:  repository.RemoteIdentity,
		Name:        "Add a farewell function",
		Spec:        "## Add a farewell function\n\nDone when farewell('world') returns Goodbye, world!",
		Runtime:     "claude-code",
		Source:      source,
		PreApproved: preApproved,
	}))
}

// admitDraftForTest creates one draft through the real admission path.
func admitDraftForTest(t *testing.T, store *Store) protocol.AdmitWorkResponse {
	t.Helper()
	response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// AC-1. The two assertions on assigned_worker_id and executions distinguish
// "inserted as draft" from "inserted queued, then demoted": a demote-after
// the fact would still leave the session state as draft, but only the
// insert-as-draft path leaves assigned_worker_id NULL and creates no
// executions row, since worker selection never ran. This is what turns the
// no-window guarantee from Step 4b from reviewable into tested.
func TestAdmitWithoutPreApprovalLandsInDraft(t *testing.T) {
	store := newTestStore(t)
	response, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if response.State != protocol.SessionDraft {
		t.Fatalf("state = %q, want draft", response.State)
	}
	if len(response.WorkIDs) != 1 {
		t.Fatalf("work ids = %d, want 1", len(response.WorkIDs))
	}
	var assignedWorkerID sql.NullString
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT assigned_worker_id FROM sessions WHERE id = ?`, response.WorkIDs[0],
	).Scan(&assignedWorkerID); err != nil {
		t.Fatal(err)
	}
	if assignedWorkerID.Valid {
		t.Fatalf("assigned_worker_id = %q, want NULL; a draft never went through worker selection", assignedWorkerID.String)
	}
	var executions int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM executions WHERE session_id = ?`, response.WorkIDs[0],
	).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0; a draft session was queued and demoted rather than inserted as draft", executions)
	}
}

// AC-2
func TestOrchestratorPreApprovedSubmissionIsQueued(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-admit", 2)
	response, err := admitForTest(t, store, protocol.WorkSourceOrchestrator, true,
		"11000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if response.State == protocol.SessionDraft {
		t.Fatal("a pre-approved orchestrator submission was left in draft")
	}
	var approvedBy string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT approved_by FROM sessions WHERE id = ?`, response.WorkIDs[0]).Scan(&approvedBy); err != nil {
		t.Fatal(err)
	}
	if approvedBy != "" {
		t.Fatalf("approved_by = %q, want empty; pre-approval records no approver", approvedBy)
	}
	var runSource string
	var preApproved int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT source, pre_approved FROM runs WHERE id = ?`, response.RunID,
	).Scan(&runSource, &preApproved); err != nil {
		t.Fatal(err)
	}
	if runSource != "orchestrator" {
		t.Fatalf("runs.source = %q, want orchestrator", runSource)
	}
	if preApproved != 1 {
		t.Fatalf("runs.pre_approved = %d, want 1", preApproved)
	}
}

// Regression for the Critical review finding: tasks.name_key is UNIQUE, and
// admission gives every Run its own Task under the hood. Titles repeat
// constantly on the cockpit and GitHub paths ("Update dependencies", "Fix
// flaky test"), so a second admission sharing a title with an earlier one
// must still succeed and must still produce its own, distinct Run.
func TestRepeatedTitleDoesNotBreakAdmission(t *testing.T) {
	store := newTestStore(t)
	first, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000006")
	if err != nil {
		t.Fatalf("first admission with a shared title failed: %v", err)
	}
	second, err := admitForTest(t, store, protocol.WorkSourceCockpit, false,
		"11000000-0000-4000-8000-000000000007")
	if err != nil {
		t.Fatalf("second admission with the same title failed: %v", err)
	}
	if first.RunID == second.RunID {
		t.Fatal("two distinct admissions collapsed onto one run")
	}
	var runs int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}

// Regression: GitHub delivers repository identities in canonical case
// ("owner/Repo"), but repositories.remote_identity is stored lowercased by
// CreateManagedRepository. Admission must normalize the same way before
// matching, or a case difference alone returns repository_not_found for a
// repository that plainly is managed.
func TestAdmissionMatchesRepositoryIdentityCaseInsensitively(t *testing.T) {
	store := newTestStore(t)
	registerTestRepository(t, store, "github.com/Example/CaseSensitive")
	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "11000000-0000-4000-8000-000000000008",
		Repository: "github.com/Example/CaseSensitive",
		Name:       "Case sensitivity regression",
		Spec:       "spec text",
		Runtime:    "claude-code",
		Source:     protocol.WorkSourceCockpit,
	})
	if err != nil {
		t.Fatalf("admission with the exact registered case failed: %v", err)
	}
}

// INV-1, the teeth
func TestNonOrchestratorCannotSelfApprove(t *testing.T) {
	store := newTestStore(t)
	for index, source := range []protocol.WorkSource{
		protocol.WorkSourceCockpit, protocol.WorkSourceGitHub,
	} {
		key := fmt.Sprintf("11000000-0000-4000-8000-00000000010%d", index)
		if _, err := admitForTest(t, store, source, true, key); err == nil {
			t.Fatalf("source %q was allowed to set pre_approved", source)
		}
	}
}

func TestUnknownSourceIsRejected(t *testing.T) {
	store := newTestStore(t)
	if _, err := admitForTest(t, store, protocol.WorkSource("wherever"), false,
		"11000000-0000-4000-8000-000000000004"); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

// AC-3
func TestSameRequestKeyCreatesOneRun(t *testing.T) {
	store := newTestStore(t)
	key := "11000000-0000-4000-8000-000000000005"
	first, err := admitForTest(t, store, protocol.WorkSourceCockpit, false, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := admitForTest(t, store, protocol.WorkSourceCockpit, false, key)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID {
		t.Fatalf("run ids %s and %s differ; admission is not idempotent", first.RunID, second.RunID)
	}
	var runs int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}
