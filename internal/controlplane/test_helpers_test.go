package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	workerA = "worker-a"
	tokenA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func boolPointer(value bool) *bool {
	return &value
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/controlplane.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func registerTestWorker(
	t *testing.T,
	store *Store,
	id string,
	capacity int,
	repositories ...protocol.RepositoryRegistration,
) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), id, protocol.WorkerRegistration{
		Name: id, WorkerVersion: "test", ClaimProtocolVersion: protocol.ClaimProtocolVersion,
		Runtime:        protocol.RuntimeCodex,
		RuntimeVersion: "codex-test", Capacity: capacity, Health: "healthy",
		Repositories: repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func registerTestRepository(
	t *testing.T, store *Store, identity string,
) protocol.ManagedRepository {
	t.Helper()
	repository, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

// seedTaskForTest creates a minimal task so a run row has a valid task_id.
func seedTaskForTest(t *testing.T, store *Store, repositoryID string) string {
	t.Helper()
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		RequestKey:       "seed-" + repositoryID,
		Name:             "Seed",
		Prompt:           "spec text",
		Runtime:          "claude-code",
		TimeoutSeconds:   3600,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repositoryID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	return task.ID
}

// claimRequestForTest builds a minimal valid claim request. ClaimRequest has
// no runtime field: runtime compatibility is decided by matching the
// session's required_runtime against the worker's advertised capabilities,
// not by anything on the request.
func claimRequestForTest() protocol.ClaimRequest {
	return protocol.ClaimRequest{RequestID: "draft-inert-claim", LeaseToken: tokenA}
}

// firstTwo drops the created boolean from AdmitWork's return so test bodies
// calling it through a single assignment stay readable.
func firstTwo(
	response protocol.AdmitWorkResponse, _ bool, err error,
) (protocol.AdmitWorkResponse, error) {
	return response, err
}
