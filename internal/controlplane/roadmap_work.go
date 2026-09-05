package controlplane

import (
	"context"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// roadmapWorkPages is how many pages of work rows the join reads. The page size
// is the store's maximum, so this is a few hundred of the most recent runs:
// enough to cover every pebble anyone is currently building, and bounded so a
// long-lived factory does not turn one page view into a full table scan.
const roadmapWorkPages = 3

// roadmapWorkState is the join between the orchestrator's plan on disk and the
// factory's own runs. A pebble becomes a submitted task under its own title,
// so the title is the key. It is a plain string match on the trimmed title
// because that is what the orchestrator actually sends, and a fuzzier rule
// would colour a boulder from somebody else's work.
type roadmapWorkState struct {
	State          string
	WorkID         string
	PullRequestURL string
}

// roadmapWorkIndex reads recent work and indexes it by task name. When one name
// has several runs, the newest wins, because a pebble that failed and was
// rerun should show what it is doing now, not what it did first. WorkPage
// returns newest first, so the first row seen for a name is the newest one.
func roadmapWorkIndex(ctx context.Context, store *Store) map[string]roadmapWorkState {
	index := map[string]roadmapWorkState{}
	if store == nil {
		return index
	}
	cursor := ""
	for page := 0; page < roadmapWorkPages; page++ {
		result, err := store.WorkPage(ctx, protocol.WorkFilter{}, maxTaskPageSize, cursor)
		if err != nil {
			return index
		}
		for _, work := range result.Work {
			name := strings.TrimSpace(work.TaskName)
			if name == "" {
				continue
			}
			if _, seen := index[name]; seen {
				continue
			}
			index[name] = roadmapWorkState{
				State:          string(work.State),
				WorkID:         work.ID,
				PullRequestURL: work.PullRequestURL,
			}
		}
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
	}
	return index
}

// roadmapApplyWork stamps every pebble with the state of the run that built it,
// then rolls each boulder up from its pebbles. The roadmap on disk says what is
// planned; this is what makes the page say what is happening.
func roadmapApplyWork(roadmap *Roadmap, index map[string]roadmapWorkState) {
	if roadmap == nil || len(index) == 0 {
		return
	}
	for p := range roadmap.Projects {
		project := &roadmap.Projects[p]
		for c := range project.Checkpoints {
			checkpoint := &project.Checkpoints[c]
			for i := range checkpoint.Pebbles {
				roadmapStampPebble(&checkpoint.Pebbles[i], index)
			}
			for b := range checkpoint.Boulders {
				boulder := &checkpoint.Boulders[b]
				for i := range boulder.Pebbles {
					roadmapStampPebble(&boulder.Pebbles[i], index)
				}
				boulder.State = roadmapRollUp(boulder.Pebbles)
			}
		}
	}
}

func roadmapStampPebble(pebble *RoadmapPebble, index map[string]roadmapWorkState) {
	work, ok := index[strings.TrimSpace(pebble.Title)]
	if !ok {
		return
	}
	pebble.State = work.State
	pebble.WorkID = work.WorkID
	pebble.PullRequestURL = work.PullRequestURL
}

// roadmapRollUp turns a boulder's pebble states into the one word the page
// colours the box by. Trouble outranks progress and progress outranks done, so
// a boulder never looks finished while part of it is broken or still moving.
//
//	working  something is running, queued or waiting on an answer right now
//	failed   nothing is moving and at least one pebble failed or was cancelled
//	done     every pebble reached a terminal success
//	part     some pebbles are done and the rest have not started
//	planned  no pebble has a run yet
func roadmapRollUp(pebbles []RoadmapPebble) string {
	if len(pebbles) == 0 {
		return "planned"
	}
	active, failed, done := 0, 0, 0
	for _, pebble := range pebbles {
		switch pebble.State {
		case "queued", "blocked", "preparing", "running", "needs-input", "ready":
			active++
		case "failed", "cancelled":
			failed++
		case "succeeded", "no-change":
			done++
		}
	}
	switch {
	case active > 0:
		return "working"
	case failed > 0:
		return "failed"
	case done == len(pebbles):
		return "done"
	case done > 0:
		return "part"
	default:
		return "planned"
	}
}

// roadmapSortWaiting keeps the bell's list in a stable order so the count and
// the list agree between two reads of the same state.
func roadmapSortWaiting(waiting []RoadmapWaiting) {
	sort.SliceStable(waiting, func(i, j int) bool {
		if waiting[i].Project != waiting[j].Project {
			return waiting[i].Project < waiting[j].Project
		}
		return waiting[i].Number < waiting[j].Number
	})
}
