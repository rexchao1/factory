package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// AdmitWork accepts one spec and produces one Run. It is the single admission
// path described in design.md section 6. Orchestrator submissions may carry
// pre_approved because a human was present while the spec was written. Every
// other source lands in draft and waits for an explicit approval.
func (s *Store) AdmitWork(
	ctx context.Context, input protocol.AdmitWorkRequest,
) (protocol.AdmitWorkResponse, bool, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Repository = strings.TrimSpace(input.Repository)
	input.Name = strings.TrimSpace(input.Name)
	input.Runtime = strings.TrimSpace(input.Runtime)

	if input.RequestKey == "" || len(input.RequestKey) > 200 {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(input.RequestKey, "schedule:") {
		return protocol.AdmitWorkResponse{}, false,
			invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}
	if !protocol.SupportedWorkSource(input.Source) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_source", "source must be orchestrator, cockpit, or github")
	}
	// INV-1. Only the orchestrator may assert that a human already approved.
	if input.PreApproved && input.Source != protocol.WorkSourceOrchestrator {
		return protocol.AdmitWorkResponse{}, false, invalid(
			"pre_approval_not_permitted",
			"only orchestrator submissions may set pre_approved",
		)
	}
	// An explicit delivery is validated here. An omitted one is resolved to
	// the repository's own default below, once the repository is known.
	if input.Delivery != "" && !protocol.SupportedDeliveryMode(input.Delivery) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_delivery", "delivery must be pr, pr+automerge, or branch")
	}
	if strings.TrimSpace(input.Spec) == "" {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_spec", "spec is required")
	}

	if existing, found, err := s.admittedWork(ctx, input.RequestKey); err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	} else if found {
		return existing, false, nil
	}

	repository, err := s.managedRepositoryByIdentity(ctx, input.Repository)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	if input.Delivery == "" {
		input.Delivery = repository.DefaultDelivery
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultAdmissionTimeoutSeconds
	}

	task, err := s.CreateTask(ctx, protocol.SaveTaskRequest{
		RequestKey:       "admit:" + input.RequestKey,
		Name:             input.Name,
		Prompt:           input.Spec,
		Runtime:          input.Runtime,
		TimeoutSeconds:   input.TimeoutSeconds,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repository.ID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
		OutcomeContract:  protocol.OutcomeAgentUpdate,
		PipelineID:       input.PipelineID,
	})
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}

	// AdmitAsDraft is consumed inside RunTask's own transaction, so a session
	// is never briefly created as queued before being demoted to draft. See
	// the AdmitAsDraft wiring in tasks.go.
	detail, _, err := s.RunTask(ctx, task.ID, protocol.RunTaskRequest{
		RequestKey:   input.RequestKey,
		AdmitAsDraft: !input.PreApproved,
	})
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}

	if err := s.applyAdmissionProvenance(ctx, detail.Run.ID, input); err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	return s.admissionResponse(ctx, detail.Run.ID, task.ID, input.Source)
}

// applyAdmissionProvenance stamps source, pre_approved, and delivery onto the
// run and its sessions. It does not set draft; RunTask already did that under
// AdmitAsDraft, inside the transaction that created the sessions.
func (s *Store) applyAdmissionProvenance(
	ctx context.Context, runID string, input protocol.AdmitWorkRequest,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = tx.Rollback() }()

	preApproved := 0
	if input.PreApproved {
		preApproved = 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET source = ?, pre_approved = ? WHERE id = ?
	`, string(input.Source), preApproved, runID); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET delivery = ? WHERE run_id = ?
	`, string(input.Delivery), runID); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) admissionResponse(
	ctx context.Context, runID, taskID string, source protocol.WorkSource,
) (protocol.AdmitWorkResponse, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, state FROM sessions WHERE run_id = ? ORDER BY target_position, id
	`, runID)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	defer rows.Close()
	response := protocol.AdmitWorkResponse{RunID: runID, TaskID: taskID, Source: source}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return protocol.AdmitWorkResponse{}, false, unavailable(err)
		}
		response.WorkIDs = append(response.WorkIDs, id)
		response.State = protocol.SessionState(state)
	}
	if err := rows.Err(); err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	return response, true, nil
}

// admittedWork replays a previous admission for the same request key, so two
// clients submitting the same work create one Run. AC-3.
func (s *Store) admittedWork(
	ctx context.Context, requestKey string,
) (protocol.AdmitWorkResponse, bool, error) {
	var runID, taskID, source string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, source FROM runs WHERE request_key = ?
	`, requestKey).Scan(&runID, &taskID, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AdmitWorkResponse{}, false, nil
	}
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	response, _, err := s.admissionResponse(ctx, runID, taskID, protocol.WorkSource(source))
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	return response, true, nil
}

const defaultAdmissionTimeoutSeconds = 3600
