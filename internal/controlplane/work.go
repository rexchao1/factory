package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultWorkUpdatePageSize = 100
	maxWorkUpdatePageSize     = 200
)

func (s *Store) AppendAgentUpdate(
	ctx context.Context,
	attemptID string,
	input protocol.AttemptUpdateRequest,
) (protocol.WorkUpdate, error) {
	if !validUUID(attemptID) || !validUUID(input.RequestID) {
		return protocol.WorkUpdate{}, invalid("invalid_update_identity", "attempt_id and request_id must be UUIDs")
	}
	if err := validateToken(input.LeaseToken); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if !protocol.SupportedWorkUpdateStatus(input.Status) {
		return protocol.WorkUpdate{}, invalid(
			"invalid_update_status",
			"status must be running, ready, needs-input, failed, or no-change",
		)
	}
	if !utf8.ValidString(input.Message) || strings.TrimSpace(input.Message) == "" {
		return protocol.WorkUpdate{}, invalid("invalid_update_message", "message must be non-empty UTF-8 text")
	}
	messageLimit := protocol.MaxOutcomeBytes
	if input.Status == protocol.WorkUpdateRunning {
		messageLimit = protocol.MaxProgressBytes
	}
	if len([]byte(input.Message)) > messageLimit {
		return protocol.WorkUpdate{}, &ServiceError{
			Code: "update_message_too_large", Message: "update message exceeds its status limit", Status: 413,
		}
	}
	if len([]byte(input.PullRequestURL)) > 2048 || len([]byte(input.PullRequestHeadBranch)) > 255 ||
		len(input.PullRequestHeadSHA) > 64 || len(input.CheckpointSHA) > 64 {
		return protocol.WorkUpdate{}, invalid("invalid_update_evidence", "update evidence exceeds its storage limit")
	}
	if input.Status == protocol.WorkUpdateReady {
		if input.PullRequestURL == "" || (!input.ReplayOnly && (input.PullRequestHeadBranch == "" ||
			!validCommitSHA(input.PullRequestHeadSHA))) || input.CheckpointSHA != "" {
			return protocol.WorkUpdate{}, invalid(
				"invalid_delivery_evidence",
				"ready requires a pull request URL, head branch, and full head SHA",
			)
		}
	} else if input.PullRequestURL != "" || input.PullRequestHeadBranch != "" || input.PullRequestHeadSHA != "" {
		return protocol.WorkUpdate{}, invalid("unexpected_delivery_evidence", "only ready accepts pull request evidence")
	}
	if input.Status == protocol.WorkUpdateNeedsInput {
		if !input.ReplayOnly && !validCommitSHA(input.CheckpointSHA) {
			return protocol.WorkUpdate{}, invalid("invalid_checkpoint", "needs-input requires a full checkpoint SHA")
		}
	} else if input.CheckpointSHA != "" || input.CheckpointPublished {
		return protocol.WorkUpdate{}, invalid("unexpected_checkpoint", "only needs-input accepts a checkpoint SHA")
	}
	if input.ReplayOnly && (input.PullRequestHeadBranch != "" || input.PullRequestHeadSHA != "" ||
		input.CheckpointSHA != "" || input.CheckpointPublished) {
		return protocol.WorkUpdate{}, invalid("unexpected_replay_evidence", "replay lookup cannot include Worker-derived evidence")
	}

	digest, err := agentUpdateRequestDigest(input)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}

	// INV-3. A ready delivery is verified by the server against GitHub before
	// it is recorded, and the check runs HERE, before the transaction opens.
	//
	// Three things make that placement load bearing:
	//
	//  1. SQLite has a 5 second busy timeout. A network call inside a write
	//     transaction would hold the write lock across it and block every
	//     other writer for as long as GitHub takes to answer.
	//  2. A ReplayOnly probe is skipped. The worker sends one before every
	//     real forward, so verifying there would double every retry's GitHub
	//     calls and let an outage reject the replay of an outcome that is
	//     already durable.
	//  3. Verification runs only for a caller that already owns the lease, so
	//     a wrong token cannot make the server issue GitHub requests.
	if input.Status == protocol.WorkUpdateReady && !input.ReplayOnly {
		identity, publishBranch, owned, err := s.readyDeliveryTarget(ctx, attemptID, input.LeaseToken)
		if err != nil {
			return protocol.WorkUpdate{}, err
		}
		// When the attempt is unknown or the lease is not owned, fall through
		// to the transaction and let the existing errors say so. Reporting a
		// verification failure for what is really an ownership problem would
		// name the wrong fact.
		if owned {
			if err := verifyReadyDelivery(ctx, s.github, identity, publishBranch, input); err != nil {
				// Refuse the outcome. An unverified ready is exactly what
				// INV-3 exists to prevent, so the attempt fails loudly rather
				// than recording a delivery the server could not confirm.
				return protocol.WorkUpdate{}, conflict("ready_verification_failed", err.Error())
			}
		}
	}

	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.WorkUpdate{}, err
	}
	if !equalDigest(lease.digest, digestToken(input.LeaseToken)) {
		return protocol.WorkUpdate{}, conflict("lease_not_owner", "the lease token does not own this attempt")
	}
	if stored, storedDigest, found, err := storedAgentUpdateRequest(ctx, tx, attemptID, input.RequestID); err != nil {
		return protocol.WorkUpdate{}, err
	} else if found {
		if !equalDigest(storedDigest, digest) {
			return protocol.WorkUpdate{}, conflict(
				"update_request_conflict",
				"request_id was already used with different update fields",
			)
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		return stored, nil
	}
	if input.ReplayOnly {
		return protocol.WorkUpdate{}, &ServiceError{
			Code: "update_request_not_found", Message: "the update request has not been stored", Status: 404,
		}
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if lease.outcomeContract != protocol.OutcomeAgentUpdate {
		return protocol.WorkUpdate{}, conflict(
			"agent_update_not_allowed",
			"this Attempt uses process-exit completion and cannot accept semantic updates",
		)
	}
	if lease.attemptState != "running" || lease.executionState != "running" || lease.cancel {
		return protocol.WorkUpdate{}, conflict("update_not_active", "the Attempt is not active for updates")
	}
	var workID, workState, owner string
	if err := tx.QueryRowContext(ctx, `
		SELECT session.id, session.state, session.execution_owner
		FROM sessions session
		JOIN executions execution ON execution.session_id = session.id
		WHERE execution.id = ?
	`, lease.executionID).Scan(&workID, &workState, &owner); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if workState != string(protocol.WorkRunning) || owner != string(protocol.ExecutionOwnerWorkerAttempt) {
		return protocol.WorkUpdate{}, conflict("update_not_active", "the Work is not owned by this active Attempt")
	}
	existingOutcome, hasOutcome, err := attemptOutcome(ctx, tx, attemptID)
	if err != nil {
		return protocol.WorkUpdate{}, err
	}
	if hasOutcome {
		if input.Status == protocol.WorkUpdateRunning {
			return protocol.WorkUpdate{}, conflict("outcome_already_reported", "progress cannot follow an outcome report")
		}
		if !sameAgentUpdate(existingOutcome, input) {
			return protocol.WorkUpdate{}, conflict("outcome_already_reported", "a different outcome was already reported")
		}
		if err := storeAgentUpdateRequest(ctx, tx, attemptID, input.RequestID, digest, existingOutcome.ID, now); err != nil {
			return protocol.WorkUpdate{}, err
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		return existingOutcome, nil
	}
	if input.Status == protocol.WorkUpdateRunning {
		var progressCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM work_updates WHERE attempt_id = ? AND status = 'running'
		`, attemptID).Scan(&progressCount); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		if progressCount >= protocol.MaxProgressPerAttempt {
			return protocol.WorkUpdate{}, &ServiceError{
				Code: "progress_update_limit", Message: "the Attempt already has 199 progress updates; its outcome slot is reserved", Status: 409,
			}
		}
	}
	var sequence int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM work_updates WHERE work_id = ?
	`, workID).Scan(&sequence); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if input.Status == protocol.WorkUpdateNeedsInput {
		prospective := continuationHistory{
			Sequence: sequence, Kind: "update", Status: input.Status, Actor: string(protocol.WorkUpdateActorAgent),
			Message: input.Message, CheckpointSHA: input.CheckpointSHA, AcceptedAtMillis: now,
		}
		if err := validateContinuationWithinTx(
			ctx, tx, workID, input.Message, strings.Repeat("a", protocol.MaxAnswerBytes), prospective,
		); err != nil {
			return protocol.WorkUpdate{}, err
		}
	}
	updateID, err := newID()
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_updates(
			id, work_id, attempt_id, request_id, sequence, status, message,
			pull_request_url, pull_request_head_branch, pull_request_head_sha,
			checkpoint_sha, checkpoint_published, actor, accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'agent', ?)
	`, updateID, workID, attemptID, input.RequestID, sequence, input.Status, input.Message,
		input.PullRequestURL, input.PullRequestHeadBranch, input.PullRequestHeadSHA,
		input.CheckpointSHA, input.CheckpointPublished, now); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if err := storeAgentUpdateRequest(ctx, tx, attemptID, input.RequestID, digest, updateID, now); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if input.Status == protocol.WorkUpdateReady {
		// Record how this delivery was verified, in the same transaction that
		// records the outcome. A ready accepted before migration 039 carries
		// an empty source, which is what distinguishes it from one the server
		// checked itself. Four of INV-3's five clauses; see Gap 9 for the
		// fifth, which is not server observable.
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET delivery_verified_at = ?, delivery_verification_source = 'server-github'
			WHERE id = ?
		`, now, workID); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
	}
	if input.Status == protocol.WorkUpdateRunning {
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET latest_progress = ? WHERE id = ? AND state = 'running'
		`, input.Message, workID); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	return s.workUpdate(ctx, updateID)
}

// readyDeliveryTarget loads the two facts INV-3 verification compares against,
// outside any transaction, and reports whether the caller owns the attempt's
// lease. A caller that does not own it gets owned = false rather than an
// error, so the transaction below can produce the ownership error the rest of
// the codebase already returns for that case.
func (s *Store) readyDeliveryTarget(
	ctx context.Context,
	attemptID string,
	leaseToken string,
) (identity string, publishBranch string, owned bool, err error) {
	var storedDigest []byte
	queryErr := s.db.QueryRowContext(ctx, `
		SELECT session.repository_identity, session.publish_branch, attempt.lease_digest
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE attempt.id = ?
	`, attemptID).Scan(&identity, &publishBranch, &storedDigest)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if queryErr != nil {
		return "", "", false, unavailable(queryErr)
	}
	if !equalDigest(storedDigest, digestToken(leaseToken)) {
		return "", "", false, nil
	}
	return identity, publishBranch, true, nil
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func agentUpdateRequestDigest(input protocol.AttemptUpdateRequest) ([]byte, error) {
	// The digest covers only fields supplied through the agent-visible local
	// protocol. Worker-derived delivery and checkpoint evidence can change in
	// the environment, but an exact agent retry must still replay its original
	// durable result.
	body, err := json.Marshal(struct {
		RequestID      string                    `json:"request_id"`
		Status         protocol.WorkUpdateStatus `json:"status"`
		Message        string                    `json:"message"`
		PullRequestURL string                    `json:"pull_request_url,omitempty"`
	}{
		RequestID: input.RequestID, Status: input.Status, Message: input.Message,
		PullRequestURL: input.PullRequestURL,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func storedAgentUpdateRequest(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, requestID string,
) (protocol.WorkUpdate, []byte, bool, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	var digest []byte
	err := tx.QueryRowContext(ctx, `
		SELECT stored_update.id, stored_update.work_id, COALESCE(stored_update.attempt_id, ''), stored_update.request_id,
		       stored_update.sequence, stored_update.status, stored_update.message, stored_update.pull_request_url,
		       stored_update.pull_request_head_branch, stored_update.pull_request_head_sha,
		       stored_update.checkpoint_sha, stored_update.checkpoint_published,
		       stored_update.actor, stored_update.accepted_at, request.request_digest
		FROM agent_update_requests request
		JOIN work_updates stored_update ON stored_update.id = request.update_id
		WHERE request.attempt_id = ? AND request.request_id = ?
	`, attemptID, requestID).Scan(
		&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID, &update.Sequence,
		&update.Status, &update.Message, &update.PullRequestURL, &update.PullRequestHeadBranch,
		&update.PullRequestHeadSHA, &update.CheckpointSHA, &update.CheckpointPublished,
		&update.Actor, &acceptedAt, &digest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkUpdate{}, nil, false, nil
	}
	if err != nil {
		return protocol.WorkUpdate{}, nil, false, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, digest, true, nil
}

func attemptOutcome(ctx context.Context, tx *sql.Tx, attemptID string) (protocol.WorkUpdate, bool, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, checkpoint_published, actor, accepted_at
		FROM work_updates WHERE attempt_id = ? AND status != 'running'
	`, attemptID).Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
		&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
		&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
		&update.CheckpointPublished, &update.Actor, &acceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkUpdate{}, false, nil
	}
	if err != nil {
		return protocol.WorkUpdate{}, false, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, true, nil
}

func sameAgentUpdate(update protocol.WorkUpdate, input protocol.AttemptUpdateRequest) bool {
	return update.Status == input.Status && update.Message == input.Message &&
		update.PullRequestURL == input.PullRequestURL &&
		update.PullRequestHeadBranch == input.PullRequestHeadBranch &&
		update.PullRequestHeadSHA == input.PullRequestHeadSHA &&
		update.CheckpointSHA == input.CheckpointSHA &&
		update.CheckpointPublished == input.CheckpointPublished
}

func storeAgentUpdateRequest(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, requestID string,
	digest []byte,
	updateID string,
	now int64,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_update_requests(attempt_id, request_id, request_digest, update_id, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, attemptID, requestID, digest, updateID, now); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) workUpdate(ctx context.Context, id string) (protocol.WorkUpdate, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, checkpoint_published, actor, accepted_at
		FROM work_updates WHERE id = ?
	`, id).Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
		&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
		&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
		&update.CheckpointPublished, &update.Actor, &acceptedAt)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, nil
}

// Work returns the product-facing Work record backed by the existing Session
// row. Keeping the adapter here lets existing Run and Session clients remain
// compatible while later operator surfaces adopt Work directly.
func (s *Store) Work(ctx context.Context, id string) (protocol.Work, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM sessions WHERE id = ?`, id).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Work{}, ErrNotFound
	}
	if err != nil {
		return protocol.Work{}, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	if err != nil {
		return protocol.Work{}, err
	}
	for _, work := range detail.Sessions {
		if work.ID == id {
			rows, queryErr := s.db.QueryContext(ctx, `
				SELECT position, name, kind, prompt, command, model, effort,
				       state, result, error, started_at, completed_at, `+stageCostColumns+`
				FROM session_stages WHERE session_id = ? ORDER BY position
			`, id)
			if queryErr != nil {
				return protocol.Work{}, unavailable(queryErr)
			}
			work.Stages = nil
			for rows.Next() {
				stage, scanErr := scanStageRun(rows)
				if scanErr != nil {
					rows.Close()
					return protocol.Work{}, unavailable(scanErr)
				}
				work.Stages = append(work.Stages, stage)
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				rows.Close()
				return protocol.Work{}, unavailable(rowsErr)
			}
			if closeErr := rows.Close(); closeErr != nil {
				return protocol.Work{}, unavailable(closeErr)
			}
			return work, nil
		}
	}
	return protocol.Work{}, ErrNotFound
}

func (s *Store) WorkUpdates(
	ctx context.Context,
	workID string,
	limit int,
	after int,
) (protocol.WorkUpdatePage, error) {
	if limit == 0 {
		limit = defaultWorkUpdatePageSize
	}
	if limit < 1 || limit > maxWorkUpdatePageSize {
		return protocol.WorkUpdatePage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	if after < 0 {
		return protocol.WorkUpdatePage{}, invalid("invalid_cursor", "after must not be negative")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, workID).Scan(&exists); err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	if exists == 0 {
		return protocol.WorkUpdatePage{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, checkpoint_published, actor, accepted_at
		FROM work_updates
		WHERE work_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT ?
	`, workID, after, limit+1)
	if err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	defer rows.Close()
	updates := make([]protocol.WorkUpdate, 0, limit+1)
	for rows.Next() {
		var update protocol.WorkUpdate
		var acceptedAt int64
		if err := rows.Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
			&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
			&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
			&update.CheckpointPublished, &update.Actor, &acceptedAt); err != nil {
			return protocol.WorkUpdatePage{}, unavailable(err)
		}
		update.AcceptedAt = fromMillis(acceptedAt)
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	page := protocol.WorkUpdatePage{Updates: updates, NextAfter: after}
	if len(page.Updates) > limit {
		page.Updates = page.Updates[:limit]
		page.HasMore = true
	}
	if len(page.Updates) != 0 {
		page.NextAfter = page.Updates[len(page.Updates)-1].Sequence
	}
	return page, nil
}

func validateWorkRetryGuards(
	ctx context.Context,
	tx *sql.Tx,
	workID, repositoryID, targetKind, sourceKind, sourceKey string,
) error {
	if err := validateWorkApproved(ctx, tx, workID); err != nil {
		return err
	}
	var replacementCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions WHERE predecessor_work_id = ?
	`, workID).Scan(&replacementCount); err != nil {
		return unavailable(err)
	}
	if replacementCount != 0 {
		return conflict("work_replaced", "replaced Work cannot be retried")
	}
	var matchingNonterminal int
	var err error
	if targetKind == "work_item" {
		err = tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM sessions
			WHERE id != ? AND repository_id = ? AND source_kind = ? AND source_key = ?
			  AND state IN ('draft', 'blocked', 'queued', 'preparing', 'running', 'needs-input')
		`, workID, repositoryID, sourceKind, sourceKey).Scan(&matchingNonterminal)
	} else {
		err = tx.QueryRowContext(ctx, `
			WITH RECURSIVE lineage(work_id, root_id) AS (
				SELECT id, id FROM sessions WHERE predecessor_work_id IS NULL
				UNION ALL
				SELECT child.id, lineage.root_id
				FROM sessions child
				JOIN lineage ON child.predecessor_work_id = lineage.work_id
			)
			SELECT COUNT(*)
			FROM sessions candidate
			JOIN lineage candidate_lineage ON candidate_lineage.work_id = candidate.id
			JOIN lineage retried_lineage ON retried_lineage.work_id = ?
			WHERE candidate.id != ?
			  AND candidate.repository_id = ?
			  AND candidate_lineage.root_id = retried_lineage.root_id
			  AND candidate.state IN ('draft', 'blocked', 'queued', 'preparing', 'running', 'needs-input')
		`, workID, workID, repositoryID).Scan(&matchingNonterminal)
	}
	if err != nil {
		return unavailable(err)
	}
	if matchingNonterminal != 0 {
		return conflict("matching_work_active", "matching nonterminal Work already exists")
	}
	return nil
}

// validateWorkApproved holds INV-1 across the two paths that can put terminal
// Work back in the queue. Cancelling a draft leaves it in 'cancelled', not
// 'draft', so a state guard alone does not close the hole: retry and replace
// both see an ordinary terminal Session and would requeue a spec no human
// ever read.
//
// The predicate is deliberately three-part. "No approval recorded" on its own
// is true of every Run that predates admission, because manual, scheduled,
// and provider_history Runs never had a gate to satisfy and so never write
// sessions.approved_at. Only Work admitted through AdmitWork carries the
// gate, and AdmitWork is the only writer of the orchestrator, cockpit, and
// github sources. Within those, runs.pre_approved = 1 means the gate was
// already satisfied at admission, which is why a pre-approved orchestrator
// submission stays retryable without an approver on the Session.
func validateWorkApproved(ctx context.Context, tx *sql.Tx, workID string) error {
	var unapproved int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM sessions session
			JOIN runs run ON run.id = session.run_id
			WHERE session.id = ?
			  AND run.source IN ('orchestrator', 'cockpit', 'github')
			  AND run.pre_approved = 0
			  AND session.approved_at IS NULL
		)
	`, workID).Scan(&unapproved); err != nil {
		return unavailable(err)
	}
	if unapproved != 0 {
		return conflict(
			"work_not_approved",
			"Work admitted for approval must be approved before it can be retried or replaced",
		)
	}
	return nil
}
