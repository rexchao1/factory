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

func TestRoadmapReadsProjectCheckpointsAndPebbles(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if !roadmap.Configured {
		t.Fatal("a roadmap read from a real root reports configured")
	}
	if len(roadmap.Projects) != 1 {
		t.Fatalf("one project directory is one project, got %d", len(roadmap.Projects))
	}
	boulder := roadmap.Projects[0]
	if boulder.Project != "payer" {
		t.Errorf("project = %q, want payer", boulder.Project)
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
	second := roadmap.Projects[0].Checkpoints[1]
	if second.Status != "review" {
		t.Errorf("status = %q, want review from the plan file, not frozen from the route", second.Status)
	}
	if second.Title != "The dashboard finishes a parked live payer" {
		t.Errorf("title = %q, want the plan's heading", second.Title)
	}
	if !second.Planned {
		t.Error("a checkpoint with a plan file reports planned")
	}
	if third := roadmap.Projects[0].Checkpoints[2]; third.Planned {
		t.Error("a checkpoint with no plan file does not report planned")
	}
}

func TestRoadmapCostsEachPassAndRollsUpToTheProject(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	boulder := roadmap.Projects[0]
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
	if waiting.Project != "payer" {
		t.Errorf("waiting names its project, got %q", waiting.Project)
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
	if len(roadmap.Projects) != 0 {
		t.Error("an empty root has no boulders")
	}
}

func TestRoadmapMissingCheckpointsDirectoryIsEmptyNotAnError(t *testing.T) {
	roadmap, err := readRoadmap(t.TempDir())
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if !roadmap.Configured || len(roadmap.Projects) != 0 {
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
	if len(roadmap.Projects) != 0 {
		t.Errorf("a symlinked route file is skipped, got %+v", roadmap.Projects)
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
	if len(roadmap.Projects) != 1 || len(roadmap.Waiting) != 1 {
		t.Fatalf("endpoint returns the parsed roadmap, got %+v", roadmap)
	}
}

// writeRoadmapBoulders puts a manifest beside checkpoint 2's tasks, the way the
// orchestrator's pebble pass does.
func writeRoadmapBoulders(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, "checkpoints", "payer", "2", "boulders.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func roadmapFixtureCheckpoint(t *testing.T, root string, number int) RoadmapCheckpoint {
	t.Helper()
	roadmap, err := readRoadmap(root)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(roadmap.Projects) != 1 {
		t.Fatalf("one project, got %d", len(roadmap.Projects))
	}
	for _, checkpoint := range roadmap.Projects[0].Checkpoints {
		if checkpoint.Number == number {
			return checkpoint
		}
	}
	t.Fatalf("checkpoint %d is missing", number)
	return RoadmapCheckpoint{}
}

func TestRoadmapGroupsPebblesByTheManifest(t *testing.T) {
	root := writeRoadmapFixture(t)
	writeRoadmapBoulders(t, root, `{"checkpoint":2,"boulders":[
	  {"id":"B1","title":"Rebuild the driver","statement":"The driver comes back.","pebbles":["01-live-driver"]},
	  {"id":"B2","title":"Publish it","statement":"The dashboard sees it.","pebbles":["02-publish"]}]}`)
	checkpoint := roadmapFixtureCheckpoint(t, root, 2)
	if len(checkpoint.Boulders) != 2 {
		t.Fatalf("two manifest boulders, got %d", len(checkpoint.Boulders))
	}
	if checkpoint.Boulders[0].ID != "B1" || checkpoint.Boulders[0].Title != "Rebuild the driver" {
		t.Errorf("first boulder = %+v", checkpoint.Boulders[0])
	}
	if checkpoint.Boulders[0].Statement != "The driver comes back." {
		t.Errorf("statement = %q", checkpoint.Boulders[0].Statement)
	}
	if len(checkpoint.Boulders[1].Pebbles) != 1 || checkpoint.Boulders[1].Pebbles[0].Slug != "02-publish" {
		t.Errorf("second boulder pebbles = %+v", checkpoint.Boulders[1].Pebbles)
	}
	if len(checkpoint.Pebbles) != 2 {
		t.Errorf("the flat pebble list survives grouping, got %d", len(checkpoint.Pebbles))
	}
}

func TestRoadmapWithoutAManifestIsOneBoulder(t *testing.T) {
	checkpoint := roadmapFixtureCheckpoint(t, writeRoadmapFixture(t), 2)
	if len(checkpoint.Boulders) != 1 {
		t.Fatalf("no manifest is one boulder, got %d", len(checkpoint.Boulders))
	}
	if checkpoint.Boulders[0].Title != "Everything in this checkpoint" {
		t.Errorf("title = %q", checkpoint.Boulders[0].Title)
	}
	if len(checkpoint.Boulders[0].Pebbles) != 2 {
		t.Errorf("it holds every pebble, got %d", len(checkpoint.Boulders[0].Pebbles))
	}
}

func TestRoadmapManifestNeverHidesAPebble(t *testing.T) {
	root := writeRoadmapFixture(t)
	writeRoadmapBoulders(t, root, `{"checkpoint":2,"boulders":[
	  {"id":"B1","title":"Rebuild the driver","pebbles":["01-live-driver","99-not-on-disk"]}]}`)
	checkpoint := roadmapFixtureCheckpoint(t, root, 2)
	if len(checkpoint.Boulders) != 2 {
		t.Fatalf("the forgotten pebble gets a catch-all, got %d boulders", len(checkpoint.Boulders))
	}
	if got := len(checkpoint.Boulders[0].Pebbles); got != 1 {
		t.Errorf("a pebble the manifest invents is dropped, got %d", got)
	}
	catchAll := checkpoint.Boulders[1]
	if catchAll.ID != "B2" || catchAll.Title != "The rest of the checkpoint" {
		t.Errorf("catch-all = %+v", catchAll)
	}
	if len(catchAll.Pebbles) != 1 || catchAll.Pebbles[0].Slug != "02-publish" {
		t.Errorf("catch-all pebbles = %+v", catchAll.Pebbles)
	}
}

func TestRoadmapUnreadableManifestFallsBackRatherThanFailing(t *testing.T) {
	root := writeRoadmapFixture(t)
	writeRoadmapBoulders(t, root, "{ this is not json")
	checkpoint := roadmapFixtureCheckpoint(t, root, 2)
	if len(checkpoint.Boulders) != 1 || len(checkpoint.Boulders[0].Pebbles) != 2 {
		t.Errorf("bad json still shows every pebble, got %+v", checkpoint.Boulders)
	}
}

func TestRoadmapPebbleSummaryIsTheOpeningParagraph(t *testing.T) {
	root := writeRoadmapFixture(t)
	path := filepath.Join(root, "checkpoints", "payer", "2", "tasks", "01-live-driver.md")
	body := "## Rebuild a resumed live payer's own driver\n\n### What are we building?\nThe runner loses the driver\non a resume.\n\n### Why?\nBecause it does.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite task: %v", err)
	}
	checkpoint := roadmapFixtureCheckpoint(t, root, 2)
	if got := checkpoint.Pebbles[0].Summary; got != "The runner loses the driver on a resume." {
		t.Errorf("summary = %q", got)
	}
}

func TestRoadmapRollUpRanksTroubleOverProgressOverDone(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{"nothing started", []string{"", ""}, "planned"},
		{"one running", []string{"succeeded", "running"}, "working"},
		{"running outranks failed", []string{"failed", "running"}, "working"},
		{"failed outranks done", []string{"succeeded", "failed"}, "failed"},
		{"all terminal", []string{"succeeded", "no-change"}, "done"},
		{"partly done", []string{"succeeded", ""}, "part"},
		{"waiting on an answer is working", []string{"needs-input"}, "working"},
		{"a pull request is still working", []string{"ready"}, "working"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pebbles := make([]RoadmapPebble, 0, len(tc.states))
			for _, state := range tc.states {
				pebbles = append(pebbles, RoadmapPebble{State: state})
			}
			if got := roadmapRollUp(pebbles); got != tc.want {
				t.Errorf("roll up %v = %q, want %q", tc.states, got, tc.want)
			}
		})
	}
	if got := roadmapRollUp(nil); got != "planned" {
		t.Errorf("an empty boulder = %q, want planned", got)
	}
}

func TestRoadmapApplyWorkStampsPebblesAndColoursBoulders(t *testing.T) {
	root := writeRoadmapFixture(t)
	writeRoadmapBoulders(t, root, `{"checkpoint":2,"boulders":[
	  {"id":"B1","title":"Rebuild the driver","pebbles":["01-live-driver"]},
	  {"id":"B2","title":"Publish it","pebbles":["02-publish"]}]}`)
	roadmap, err := readRoadmap(root)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	roadmapApplyWork(&roadmap, map[string]roadmapWorkState{
		"Rebuild a resumed live payer's own driver":        {State: "running", WorkID: "w1"},
		"Let a live terminal run publish to the dashboard": {State: "succeeded", WorkID: "w2", PullRequestURL: "https://example.test/pr/2"},
	})
	var checkpoint RoadmapCheckpoint
	for _, c := range roadmap.Projects[0].Checkpoints {
		if c.Number == 2 {
			checkpoint = c
		}
	}
	if checkpoint.Boulders[0].State != "working" {
		t.Errorf("a running pebble colours its boulder working, got %q", checkpoint.Boulders[0].State)
	}
	if checkpoint.Boulders[1].State != "done" {
		t.Errorf("a succeeded pebble colours its boulder done, got %q", checkpoint.Boulders[1].State)
	}
	if got := checkpoint.Boulders[1].Pebbles[0].PullRequestURL; got != "https://example.test/pr/2" {
		t.Errorf("the pebble carries its pull request, got %q", got)
	}
	if checkpoint.Pebbles[0].WorkID != "w1" {
		t.Errorf("the flat list is stamped too, got %q", checkpoint.Pebbles[0].WorkID)
	}
}

func TestRoadmapApplyWorkIgnoresUnrelatedNames(t *testing.T) {
	roadmap, err := readRoadmap(writeRoadmapFixture(t))
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	roadmapApplyWork(&roadmap, map[string]roadmapWorkState{"Something else entirely": {State: "running"}})
	for _, checkpoint := range roadmap.Projects[0].Checkpoints {
		for _, pebble := range checkpoint.Pebbles {
			if pebble.State != "" {
				t.Errorf("%s took a state it did not earn: %q", pebble.Slug, pebble.State)
			}
		}
	}
}
