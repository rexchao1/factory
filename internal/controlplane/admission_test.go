package controlplane

import (
	"context"
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

// AC-1
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
