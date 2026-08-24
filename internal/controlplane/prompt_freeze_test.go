package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const freezeRepositoryIdentity = "github.com/example/freeze"

const freezeSpec = `## Add a farewell function

### Done when
- farewell('world') returns Goodbye, world!

### Out of scope
- Renaming the existing module.`

// AC-9 and INV-9: every rendered stage prompt carries the whole frozen spec,
// never a summary produced by a previous stage.
func TestEveryStagePromptContainsTheFrozenSpec(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerFor(t, store, "worker-freeze", "freeze", freezeRepositoryIdentity, admissionRuntime)
	repository := registerTestRepository(t, store, freezeRepositoryIdentity)

	pipeline, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Three stages",
		Stages: []protocol.PipelineStage{
			{Position: 0, Name: "Implement", Prompt: "Implement this.\n\n{{ task.prompt }}"},
			{Position: 1, Name: "Test", Prompt: "Test this.\n\n{{ task.prompt }}"},
			{Position: 2, Name: "Review", Prompt: "Review this.\n\n{{ task.prompt }}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	admission, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey:  "22000000-0000-4000-8000-000000000001",
		Repository:  repository.RemoteIdentity,
		Name:        "Add a farewell function",
		Spec:        freezeSpec,
		Runtime:     admissionRuntime,
		Source:      protocol.WorkSourceOrchestrator,
		PreApproved: true,
		PipelineID:  pipeline.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The frozen spec only matters if the Work is actually going to run, so
	// this fixture proves it reached queued rather than stalling blocked.
	if admission.State != protocol.SessionQueued {
		t.Fatalf("state = %q, want queued", admission.State)
	}
	assertQueued(t, store, admission.WorkIDs[0])

	rows, err := store.db.QueryContext(context.Background(), `
		SELECT position, prompt FROM session_stages WHERE session_id = ? ORDER BY position
	`, admission.WorkIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stages := 0
	for rows.Next() {
		var position int
		var prompt string
		if err := rows.Scan(&position, &prompt); err != nil {
			t.Fatal(err)
		}
		stages++
		if !strings.Contains(prompt, freezeSpec) {
			t.Errorf("stage %d prompt does not contain the full frozen spec:\n%s", position, prompt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if stages != 3 {
		t.Fatalf("stages rendered = %d, want 3", stages)
	}
}
