package controlplane

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeRoadmapFixture builds a roadmap root shaped exactly like the
// orchestrator's, so the parser is proven against the real layout rather than
// against a convenience of the test's own.
func writeRoadmapFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "checkpoints", "payer")
	if err := os.MkdirAll(filepath.Join(project, "2", "tasks"), 0o755); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(project, "route.md"), `# Route: a new payer onboards itself

## Boulder
Give it a new payer portal and it works, unattended.
The office names a payer and walks away.

## Checkpoints
1. One payer brings itself up live: the daily ledger allows 20 logins. Status: built
2. The dashboard finishes a parked payer: the office fills a field into Settings. Status: frozen
3. A payer's profile is data, not code: a bring-up writes the adapter itself. Status: planned

## Not on the route
- Anything about calling.
`)
	write(filepath.Join(project, "1.md"), "# Checkpoint 1: One payer brings itself up live\n\nStatus: built\n")
	write(filepath.Join(project, "2.md"), "# Checkpoint 2: The dashboard finishes a parked live payer\n\nStatus: review\n")
	write(filepath.Join(project, "2", "tasks", "01-live-driver.md"), "## Rebuild a resumed live payer's own driver\n\nbody\n")
	write(filepath.Join(project, "2", "tasks", "02-publish.md"), "## Let a live terminal run publish to the dashboard\n")
	write(filepath.Join(root, "checkpoints", "ledger.tsv"),
		"ts\tproject\tcheckpoint\tmode\tround\truntime\tmodel\teffort\tcost_usd\tduration_ms\toutcome\n"+
			"2026-09-04T07:38:12Z\tpayer\t-\troute\t1\tclaude\tclaude-fable-5-1\thigh\t1.50\t111134\tok\n"+
			"2026-09-04T07:43:04Z\tpayer\t2\tdraft\t1\tclaude\tclaude-fable-5-1\thigh\t2.25\t289636\tok\n"+
			"2026-09-04T07:48:46Z\tpayer\t2\tcritique\t1\tclaude\tclaude-opus-5\txhigh\t0.75\t339585\tok\n")
	return root
}

func TestRoadmapReadsBoulderCheckpointsAndPebbles(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if !roadmap.Configured {
		t.Fatal("a roadmap read from a real root reports configured")
	}
	if len(roadmap.Boulders) != 1 {
		t.Fatalf("one project is one boulder, got %d", len(roadmap.Boulders))
	}
	boulder := roadmap.Boulders[0]
	if boulder.ID != "B1" {
		t.Errorf("boulder id = %q, want B1", boulder.ID)
	}
	if boulder.Title != "a new payer onboards itself" {
		t.Errorf("title = %q", boulder.Title)
	}
	if boulder.Statement == "" || boulder.Statement[:7] != "Give it" {
		t.Errorf("statement = %q, want the Boulder section prose", boulder.Statement)
	}
	if len(boulder.Checkpoints) != 3 {
		t.Fatalf("three route lines are three checkpoints, got %d", len(boulder.Checkpoints))
	}
	first := boulder.Checkpoints[0]
	if first.Number != 1 || first.Title != "One payer brings itself up live" {
		t.Errorf("first checkpoint = %d %q", first.Number, first.Title)
	}
	if first.Summary != "the daily ledger allows 20 logins" {
		t.Errorf("summary = %q", first.Summary)
	}
	second := boulder.Checkpoints[1]
	if len(second.Pebbles) != 2 {
		t.Fatalf("two task files are two pebbles, got %d", len(second.Pebbles))
	}
	if second.Pebbles[0].Ordinal != 1 || second.Pebbles[0].Title != "Rebuild a resumed live payer's own driver" {
		t.Errorf("first pebble = %d %q", second.Pebbles[0].Ordinal, second.Pebbles[0].Title)
	}
	if boulder.BuiltCount != 1 {
		t.Errorf("built count = %d, want 1", boulder.BuiltCount)
	}
}

// A written plan is what the checkpoint became. The route line is what it was
// going to be, so the plan's Status and heading win wherever both exist.
func TestRoadmapPlanStatusOverridesTheRouteLine(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	second := roadmap.Boulders[0].Checkpoints[1]
	if second.Status != "review" {
		t.Errorf("status = %q, want review from the plan file, not frozen from the route", second.Status)
	}
	if second.Title != "The dashboard finishes a parked live payer" {
		t.Errorf("title = %q, want the plan's heading", second.Title)
	}
	if !second.Planned {
		t.Error("a checkpoint with a plan file reports planned")
	}
	if third := roadmap.Boulders[0].Checkpoints[2]; third.Planned {
		t.Error("a checkpoint with no plan file does not report planned")
	}
}

func TestRoadmapCostsEachPassAndRollsUpToTheBoulder(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	boulder := roadmap.Boulders[0]
	second := boulder.Checkpoints[1]
	if len(second.Passes) != 2 {
		t.Fatalf("checkpoint 2 has two ledger rows, got %d", len(second.Passes))
	}
	if second.Passes[0].Mode != "draft" || second.Passes[1].Mode != "critique" {
		t.Errorf("passes are in time order, got %q then %q", second.Passes[0].Mode, second.Passes[1].Mode)
	}
	if second.CostUSD < 2.99 || second.CostUSD > 3.01 {
		t.Errorf("checkpoint cost = %v, want the two rows summed", second.CostUSD)
	}
	// The route pass is charged to the boulder and to no checkpoint, so the
	// boulder total is more than the sum of its checkpoints.
	if boulder.CostUSD < 4.49 || boulder.CostUSD > 4.51 {
		t.Errorf("boulder cost = %v, want the route pass included", boulder.CostUSD)
	}
}

// Waiting is the page the human opens. A checkpoint the factory is still
// building is not waiting on anyone and must not appear there.
func TestRoadmapWaitingHoldsOnlyWhatNeedsTheHuman(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(roadmap.Waiting) != 1 {
		t.Fatalf("one checkpoint in review is one waiting item, got %d", len(roadmap.Waiting))
	}
	waiting := roadmap.Waiting[0]
	if waiting.Number != 2 || waiting.Status != "review" || waiting.Action != "Review the plan" {
		t.Errorf("waiting = %+v", waiting)
	}
	if waiting.Boulder != "B1" || waiting.Project != "payer" {
		t.Errorf("waiting names its boulder and project, got %q %q", waiting.Boulder, waiting.Project)
	}
}

func TestRoadmapFrozenWithNoPebblesIsWaitingButWithPebblesIsNot(t *testing.T) {
	root := writeRoadmapFixture(t)
	plan := filepath.Join(root, "checkpoints", "payer", "2.md")
	if err := os.WriteFile(plan, []byte("# Checkpoint 2: Parked payer\n\nStatus: frozen\n"), 0o644); err != nil {
		t.Fatalf("rewrite plan: %v", err)
	}
	roadmap, err := readRoadmap(root)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(roadmap.Waiting) != 0 {
		t.Fatalf("frozen with pebbles is building, not waiting, got %+v", roadmap.Waiting)
	}
	tasks := filepath.Join(root, "checkpoints", "payer", "2", "tasks")
	if err := os.RemoveAll(tasks); err != nil {
		t.Fatalf("remove tasks: %v", err)
	}
	roadmap, err = readRoadmap(root)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(roadmap.Waiting) != 1 || roadmap.Waiting[0].Action != "Split into pebbles" {
		t.Fatalf("frozen with no pebbles waits to be split, got %+v", roadmap.Waiting)
	}
}

// An unconfigured factory is the default. It reports configured false and no
// error, because a cockpit that has never been pointed at a roadmap is not
// broken.
func TestRoadmapUnconfiguredIsNotAnError(t *testing.T) {
	roadmap, err := readRoadmap("  ")
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if roadmap.Configured {
		t.Error("an empty root reports configured false")
	}
	if len(roadmap.Boulders) != 0 {
		t.Error("an empty root has no boulders")
	}
}

func TestRoadmapMissingCheckpointsDirectoryIsEmptyNotAnError(t *testing.T) {
	roadmap, err := readRoadmap(t.TempDir())
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if !roadmap.Configured || len(roadmap.Boulders) != 0 {
		t.Errorf("a root with no checkpoints directory is configured and empty, got %+v", roadmap)
	}
}

// A route line whose colon falls inside the sentence rather than after a short
// title keeps the whole line as the title. A 200-character "title" is worse
// than no split at all.
func TestRoadmapKeepsALongRouteLineWhole(t *testing.T) {
	title, summary := roadmapSplitTitle("The walker types a discovered field on resume so a parked payer finishes without anyone opening a terminal: and that is the whole of it")
	if summary != "" {
		t.Errorf("summary = %q, want the line kept whole", summary)
	}
	if title == "" {
		t.Error("title is the whole line")
	}
}

// A symlink under the roadmap root must not read a file elsewhere on the
// machine. The reader takes regular files only.
func TestRoadmapRefusesASymlinkedFile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.md")
	if err := os.WriteFile(secret, []byte("# Route: secret\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	project := filepath.Join(root, "checkpoints", "linked")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(project, "route.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roadmap, err := readRoadmap(root)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(roadmap.Boulders) != 0 {
		t.Errorf("a symlinked route file is skipped, got %+v", roadmap.Boulders)
	}
}

func TestRoadmapStatusFallsBackToPlanned(t *testing.T) {
	if got := roadmapStatus("SomethingNew"); got != "planned" {
		t.Errorf("unknown status = %q, want planned", got)
	}
	if got := roadmapStatus("  Built "); got != "built" {
		t.Errorf("built status = %q", got)
	}
}

func TestRoadmapEndpointServesTheParsedRoadmap(t *testing.T) {
	store := newTestStore(t)
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), WithRoadmapRoot(writeRoadmapFixture(t)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/roadmap", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var roadmap Roadmap
	if err := json.Unmarshal(recorder.Body.Bytes(), &roadmap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(roadmap.Boulders) != 1 || len(roadmap.Waiting) != 1 {
		t.Fatalf("endpoint returns the parsed roadmap, got %+v", roadmap)
	}
}
