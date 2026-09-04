package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// storedAnswerActor reads the column out of the row itself, so these tests
// prove what was written rather than what some loader chose to surface.
func storedAnswerActor(t *testing.T, store *Store, workID, requestID string) string {
	t.Helper()
	var actor string
	err := store.db.QueryRowContext(context.Background(),
		`SELECT actor FROM work_answers WHERE work_id = ? AND request_id = ?`,
		workID, requestID).Scan(&actor)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func countStoredAnswers(t *testing.T, store *Store, workID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM work_answers WHERE work_id = ?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertInvalidActor(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("answer with an invalid actor was accepted")
	}
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid_actor" {
		t.Fatalf("err = %#v, want 400 invalid_actor", err)
	}
}

// assertStillNeedsInput proves a rejected answer left nothing behind: no
// work_answers row, no projected answer, and the Work still waiting.
func assertStillNeedsInput(t *testing.T, store *Store, workID string) {
	t.Helper()
	if count := countStoredAnswers(t, store, workID); count != 0 {
		t.Fatalf("rejected answer stored %d work_answers rows, want 0", count)
	}
	work, err := store.Work(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if work.State != protocol.WorkNeedsInput || work.Answer != "" || work.AnsweredBy != "" {
		t.Fatalf("rejected answer changed Work = %#v", work)
	}
}

func TestAnswerWorkRecordsActor(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "65000000-0000-4000-8000-000000000001",
		Message:   "Keep the public behavior backward compatible.",
		Actor:     "overseer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Actor != "overseer" {
		t.Fatalf("answer actor = %q, want overseer", answer.Actor)
	}
	if stored := storedAnswerActor(t, store, work.ID, answer.RequestID); stored != "overseer" {
		t.Fatalf("work_answers.actor = %q, want overseer", stored)
	}
	answered, err := store.Work(context.Background(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.AnsweredBy != "overseer" || answered.Answer != answer.Message {
		t.Fatalf("Work after answer = answered_by %q answer %q, want overseer and the answer",
			answered.AnsweredBy, answered.Answer)
	}
	// A replay carrying the same actor is the same answer.
	replayed, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: answer.RequestID, Message: answer.Message, Actor: "overseer",
	})
	if err != nil || replayed.ID != answer.ID || replayed.Actor != "overseer" {
		t.Fatalf("answer replay = %#v, error %v", replayed, err)
	}
}

func TestAnswerWorkDefaultsActorToOperator(t *testing.T) {
	for name, actor := range map[string]string{"absent": "", "whitespace": " \t\n "} {
		t.Run(name, func(t *testing.T) {
			store, _, _, work := needsInputWork(t)
			answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: "65000000-0000-4000-8000-000000000002",
				Message:   "Keep the public behavior backward compatible.",
				Actor:     actor,
			})
			if err != nil {
				t.Fatal(err)
			}
			if answer.Actor != "operator" {
				t.Fatalf("answer actor = %q, want operator", answer.Actor)
			}
			if stored := storedAnswerActor(t, store, work.ID, answer.RequestID); stored != "operator" {
				t.Fatalf("work_answers.actor = %q, want operator", stored)
			}
			answered, err := store.Work(context.Background(), work.ID)
			if err != nil {
				t.Fatal(err)
			}
			if answered.AnsweredBy != "operator" {
				t.Fatalf("answered_by = %q, want operator", answered.AnsweredBy)
			}
			// The default resolves before the replay comparison, so an
			// explicit operator replays an absent one.
			replayed, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: answer.RequestID, Message: answer.Message, Actor: "operator",
			})
			if err != nil || replayed.ID != answer.ID {
				t.Fatalf("explicit operator replay = %#v, error %v", replayed, err)
			}
		})
	}
}

// The actor field is bounded to 255 bytes, matching the work_answers.actor and
// sessions.answered_by CHECKs. Without an application-level bound, an
// over-long actor fails the INSERT's CHECK and surfaces as a 503 instead of a
// 400. Invalid UTF-8 is refused the same way.
func TestAnswerWorkRejectsActorOverByteLimit(t *testing.T) {
	for name, actor := range map[string]string{
		"256 bytes":    strings.Repeat("a", 256),
		"invalid utf8": "over\xffseer",
	} {
		t.Run(name, func(t *testing.T) {
			store, _, _, work := needsInputWork(t)
			_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: "65000000-0000-4000-8000-000000000003",
				Message:   "Keep the public behavior backward compatible.",
				Actor:     actor,
			})
			assertInvalidActor(t, err)
			assertStillNeedsInput(t, store, work.ID)
		})
	}
}

func TestAnswerWorkAcceptsActorAtByteLimit(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	actor := strings.Repeat("a", 255)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "65000000-0000-4000-8000-000000000004",
		Message:   "Keep the public behavior backward compatible.",
		Actor:     actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Actor != actor {
		t.Fatalf("answer actor = %q, want the 255-byte actor", answer.Actor)
	}
	answered, err := store.Work(context.Background(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.AnsweredBy != actor {
		t.Fatalf("answered_by = %q, want the 255-byte actor", answered.AnsweredBy)
	}
	assertQueued(t, store, work.ID)
}

// 'agent' is the label a question update carries. An answer labelled the same
// way, in any letter case, would let trusted operator context read as agent
// output in the history, so it is refused before anything is written.
func TestAnswerWorkRejectsAgentActor(t *testing.T) {
	for _, actor := range []string{"agent", "Agent", "AGENT", "  agent  "} {
		t.Run(actor, func(t *testing.T) {
			store, _, _, work := needsInputWork(t)
			_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: "65000000-0000-4000-8000-000000000005",
				Message:   "Keep the public behavior backward compatible.",
				Actor:     actor,
			})
			assertInvalidActor(t, err)
			assertStillNeedsInput(t, store, work.ID)
		})
	}
}

func TestAnswerWorkReplayWithDifferentActorConflicts(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "65000000-0000-4000-8000-000000000006",
		Message:   "Keep the public behavior backward compatible.",
		Actor:     "overseer",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, actor := range map[string]string{"other label": "reviewer", "default operator": ""} {
		t.Run(name, func(t *testing.T) {
			_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: answer.RequestID, Message: answer.Message, Actor: actor,
			})
			var serviceErr *ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Status != 409 ||
				serviceErr.Code != "answer_request_conflict" {
				t.Fatalf("replay with actor %q err = %#v, want 409 answer_request_conflict", actor, err)
			}
		})
	}
	if stored := storedAnswerActor(t, store, work.ID, answer.RequestID); stored != "overseer" {
		t.Fatalf("conflicting replay rewrote work_answers.actor to %q", stored)
	}
	if count := countStoredAnswers(t, store, work.ID); count != 1 {
		t.Fatalf("conflicting replay stored %d work_answers rows, want 1", count)
	}
}

// TestMigration040LabelsExistingAnswersOperator is the migration test,
// written against behaviour rather than DDL text. Every answer that already
// existed when migration 040 ran was written without an actor, and only the
// operator could answer before this migration, so 'operator' is the right
// label for all of them. The first half writes rows the pre-040 way and shows
// the column default; the NULL write proves the NOT NULL half. The second
// half rewinds the database to the 039 shape, plants a Session that already
// holds an answer, and runs the migration again to prove the UPDATE labels
// it and leaves an unanswered Session alone.
func TestMigration040LabelsExistingAnswersOperator(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	ctx := context.Background()
	var questionUpdateID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT id FROM work_updates WHERE work_id = ? AND status = 'needs-input'
	`, work.ID).Scan(&questionUpdateID); err != nil {
		t.Fatal(err)
	}
	// The column list deliberately omits actor, the way every pre-040
	// INSERT did.
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO work_answers(id, work_id, question_update_id, request_id, message, accepted_at)
		VALUES ('answer-pre-040', ?, ?, 'pre-040', 'Legacy answer.', 1)
	`, work.ID, questionUpdateID); err != nil {
		t.Fatal(err)
	}
	if actor := storedAnswerActor(t, store, work.ID, "pre-040"); actor != "operator" {
		t.Fatalf("pre-migration answer actor = %q, want operator", actor)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE work_answers SET actor = NULL WHERE id = 'answer-pre-040'`); err == nil {
		t.Fatal("work_answers.actor accepted NULL; the column is not NOT NULL")
	}

	// Rewind to the 039 shape. Both columns were added with plain ADD COLUMN
	// and carry only their own CHECK, so DROP COLUMN takes them back off.
	for _, statement := range []string{
		`ALTER TABLE work_answers DROP COLUMN actor`,
		`ALTER TABLE sessions DROP COLUMN answered_by`,
		`DELETE FROM schema_migrations WHERE version = 40`,
		`UPDATE sessions SET answer = 'Legacy answer.' WHERE id = '` + work.ID + `'`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	_, unansweredID := draftSession(t, store)
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	answered, err := store.Work(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Answer != "Legacy answer." || answered.AnsweredBy != "operator" {
		t.Fatalf("migrated answered Session = answer %q answered_by %q, want operator",
			answered.Answer, answered.AnsweredBy)
	}
	unanswered, err := store.Work(ctx, unansweredID)
	if err != nil {
		t.Fatal(err)
	}
	if unanswered.AnsweredBy != "" {
		t.Fatalf("migration labelled an unanswered Session %q", unanswered.AnsweredBy)
	}
	if actor := storedAnswerActor(t, store, work.ID, "pre-040"); actor != "operator" {
		t.Fatalf("re-migrated answer actor = %q, want operator", actor)
	}
}
