package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestApproveMovesDraftOutOfDraft(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-approve", 2)
	admission := admitDraftForTest(t, store)

	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "rexchao1"})
	if err != nil {
		t.Fatal(err)
	}
	if work.State == protocol.SessionDraft {
		t.Fatal("approved work is still in draft")
	}
	if work.ApprovedBy != "rexchao1" {
		t.Fatalf("approved_by = %q, want rexchao1", work.ApprovedBy)
	}
	if work.ApprovedAt == nil {
		t.Fatal("approved_at was not recorded")
	}
	if work.Delivery != protocol.DeliveryPullRequest {
		t.Fatalf("delivery = %q, want %q", work.Delivery, protocol.DeliveryPullRequest)
	}
}

func TestApproveRequiresAnActor(t *testing.T) {
	store := newTestStore(t)
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "  "}); err == nil {
		t.Fatal("approval without an actor was accepted")
	}
}

func TestApproveRejectsNonDraft(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-approve-twice", 2)
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "rexchao1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "rexchao1"}); err == nil {
		t.Fatal("approving an already approved session was accepted")
	}
}

// INV-10: once queued, no approval gate remains.
func TestApprovedWorkHasNoRemainingGate(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-gate", 2)
	admission := admitDraftForTest(t, store)
	if _, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "rexchao1"}); err != nil {
		t.Fatal(err)
	}
	var drafts int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE run_id = ? AND state = 'draft'`,
		admission.RunID).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if drafts != 0 {
		t.Fatalf("drafts remaining after approval = %d, want 0", drafts)
	}
}
