package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

// admissionRequestKeyPrefix namespaces the runs created by AdmitWork inside
// the shared runs.request_key column, so an AdmitWork call and an unrelated
// RunTask call can never collide on the same key and make AdmitWork replay
// someone else's run.
const admissionRequestKeyPrefix = "work:"

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
	if strings.HasPrefix(input.RequestKey, "schedule:") ||
		strings.HasPrefix(input.RequestKey, admissionRequestKeyPrefix) {
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

	runRequestKey := admissionRequestKeyPrefix + input.RequestKey
	if existing, found, err := s.admittedWork(ctx, runRequestKey); err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	} else if found {
		return existing, false, nil
	}

	// The repository column on repositories is normalized (lowercased, ".git"
	// stripped) at write time by CreateManagedRepository. GitHub webhooks and
	// most callers send canonical-case identities, so the lookup normalizes
	// the same way before matching, or a case difference alone would return
	// repository_not_found for a repository that plainly is managed.
	identity, err := normalizeManagedGitHubRemote(input.Repository)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	repository, err := s.managedRepositoryByIdentity(ctx, identity)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	if input.Delivery == "" {
		input.Delivery = repository.DefaultDelivery
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultAdmissionTimeoutSeconds
	}

	specification := protocol.SaveTaskRequest{
		Name:             admissionTaskName(input.RequestKey, input.Name),
		Prompt:           input.Spec,
		Runtime:          input.Runtime,
		TimeoutSeconds:   input.TimeoutSeconds,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repository.ID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
		OutcomeContract:  protocol.OutcomeAgentUpdate,
		PipelineID:       input.PipelineID,
	}
	// A task_name_conflict here is not a caller error. admissionTaskName is
	// deterministic in the request key, so the conflict IS the signal that
	// this exact request key already created the task. Adopt it and carry
	// on: see adoptAdmittedTask.
	task, err := s.CreateTask(ctx, specification)
	if serviceErrorCode(err, "task_name_conflict") {
		task, err = s.adoptAdmittedTask(ctx, specification)
	}
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}

	// admitTask is called directly, not through RunTask, so source,
	// pre_approved, and delivery are set in the same transaction that
	// inserts the run and its sessions. That also closes the Step 4b race:
	// asDraft is decided before worker selection ever runs, inside that one
	// transaction, so a session is never briefly queued before landing in
	// draft.
	detail, _, err := s.admitTask(ctx, task.ID, runRequestKey, nil, nil, "", admissionProvenance{
		source:      input.Source,
		preApproved: input.PreApproved,
		delivery:    input.Delivery,
		asDraft:     !input.PreApproved,
	})
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	return s.admissionResponse(ctx, detail.Run.ID, task.ID, input.Source)
}

// admissionTaskName is the deterministic Task name one admission uses.
//
// tasks.name_key is unique, and admission titles repeat constantly ("Update
// dependencies", "Fix flaky test"). A deterministic suffix derived from the
// request key keeps the task creatable on every admission while staying
// stable across a retry of the same key. The base name is truncated, by rune
// rather than by byte so a multibyte title is never cut mid-character, to
// keep the combined name within normalizeTask's 200 rune limit. Only the
// task's name is affected: this is an internal admission artifact, not
// input.Name itself, which AdmitWorkResponse never even returns.
//
// Being deterministic in the request key is also what makes adoption safe:
// see adoptAdmittedTask.
func admissionTaskName(requestKey, name string) string {
	digest := sha256.Sum256([]byte(requestKey))
	suffix := " (" + hex.EncodeToString(digest[:])[:8] + ")"
	base := []rune(name)
	if limit := maxTaskNameRunes - utf8.RuneCountInString(suffix); len(base) > limit {
		base = base[:limit]
	}
	return string(base) + suffix
}

// adoptAdmittedTask resolves the Task a previous attempt at this exact
// request key already created, so admission heals itself instead of
// permanently rejecting the key.
//
// AdmitWork creates the Task in CreateTask's transaction and the Run in
// admitTask's. The replay check keys on runs.request_key, which only exists
// once the second transaction commits, so between the two a duplicate has
// nothing to replay against and only tasks.name_key stops it. Two failures
// follow, and adoption answers both:
//
//   - Concurrent duplicate. Two clients POST the same request_key, both pass
//     the replay check because neither run has committed, and one loses the
//     insert race. Adopting lets the loser continue into admitTask, which
//     then does hit the replay check and returns the winner's Work. One Work
//     record: AC-3 held properly rather than by accident.
//   - Partial failure. admitTask failed transiently (SQLITE_BUSY) after
//     CreateTask committed, so the run that would trigger the replay never
//     existed and every later retry of the key hit the same 409 forever,
//     with an orphaned hash-suffixed task left in the Tasks list. Adopting
//     the orphan completes the admission and un-poisons the key.
//
// Running both inserts in one transaction would also fix this, but it means
// extracting tx-taking inner functions from CreateTask and admitTask, a
// large refactor of tasks.go, which is already the most expensive file to
// carry across upstream merges. Self-healing is the cheaper equivalent.
//
// The suffix is only 8 hex characters of a sha256, so the name alone is not
// proof of identity. The stored prompt and runtime are compared against this
// submission's, normalized exactly as CreateTask normalized them on the way
// in. A mismatch is a genuine collision rather than this submission's task,
// and is refused with its own code: better a clear error than running the
// wrong spec under a colliding name.
func (s *Store) adoptAdmittedTask(
	ctx context.Context, specification protocol.SaveTaskRequest,
) (protocol.Task, error) {
	wanted, err := normalizeTask(specification, s.now())
	if err != nil {
		return protocol.Task{}, err
	}
	var id, prompt, runtime string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, prompt, runtime FROM tasks WHERE name_key = ? AND migration_only = 0
	`, wanted.nameKey).Scan(&id, &prompt, &runtime)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing adoptable holds the name. Report the conflict CreateTask
		// already raised rather than inventing a different story.
		return protocol.Task{}, conflict("task_name_conflict", "a Task with this name already exists")
	}
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if prompt != wanted.prompt || runtime != wanted.runtime {
		return protocol.Task{}, conflict(
			"admission_name_collision",
			"a different Task already holds the name generated for this request_key",
		)
	}
	return s.Task(ctx, id)
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

// admittedWork replays a previous admission for the same (already namespaced)
// request key, so two clients submitting the same work create one Run. AC-3.
func (s *Store) admittedWork(
	ctx context.Context, runRequestKey string,
) (protocol.AdmitWorkResponse, bool, error) {
	var runID, taskID, source string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, source FROM runs WHERE request_key = ?
	`, runRequestKey).Scan(&runID, &taskID, &source)
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

// maxTaskNameRunes mirrors normalizeTask's own name length limit
// (internal/controlplane/tasks.go), so the unique-name suffix can be sized
// against the same bound rather than a second, independently maintained one.
const maxTaskNameRunes = 200
