package controlplane

import (
	"context"
	"errors"
	"strings"
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

// FINDING 1: the actor field must be bounded to 255 bytes, matching the
// sessions.approved_by CHECK (length(CAST(approved_by AS BLOB)) <= 255).
// Without an application-level bound, an over-long actor fails the UPDATE's
// CHECK constraint and surfaces as a generic 503 instead of a 400.
func TestApproveRejectsActorOverByteLimit(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-approve-actor-long", 2)
	admission := admitDraftForTest(t, store)

	actor := strings.Repeat("a", 256)
	_, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: actor})
	if err == nil {
		t.Fatal("approval with a 256-byte actor was accepted")
	}
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid_actor" {
		t.Fatalf("err = %#v, want 400 invalid_actor", err)
	}
}

func TestApproveAcceptsActorAtByteLimit(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, "worker-approve-actor-max", 2)
	admission := admitDraftForTest(t, store)

	actor := strings.Repeat("a", 255)
	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if work.ApprovedBy != actor {
		t.Fatalf("approved_by = %q, want the 255-byte actor", work.ApprovedBy)
	}
}

// FINDING 2: approval must still succeed, and must still record who approved
// it and when, even when no worker is eligible yet. Approval and worker
// assignment are separate concerns; the human gate closes regardless of
// whether the scheduler can act on it immediately.
func TestApproveWithNoEligibleWorkerLandsBlocked(t *testing.T) {
	store := newTestStore(t)
	admission := admitDraftForTest(t, store)

	work, err := store.ApproveWork(context.Background(), admission.WorkIDs[0],
		protocol.ApproveWorkRequest{Actor: "rexchao1"})
	if err != nil {
		t.Fatal(err)
	}
	if work.State != protocol.SessionBlocked {
		t.Fatalf("state = %q, want blocked", work.State)
	}
	if work.BlockedReason == "" {
		t.Fatal("blocked_reason was not recorded")
	}
	if work.ApprovedBy != "rexchao1" {
		t.Fatalf("approved_by = %q, want rexchao1", work.ApprovedBy)
	}
	if work.ApprovedAt == nil {
		t.Fatal("approved_at was not recorded even though the work is blocked")
	}
}
