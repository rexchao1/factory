package controlplane

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Roadmap reads planning state the orchestrator keeps as files and shows
// it in the cockpit. It never writes. The orchestrator owns those files and
// keeps owning them; nothing here creates, moves, or edits one, and no
// planning step runs because of a request to this package.
//
// The shape it reads is the orchestrator's own, one directory per project:
//
//	<root>/checkpoints/<project>/route.md      the boulder and its checkpoint list
//	<root>/checkpoints/<project>/<n>.md        checkpoint n's plan, with a Status line
//	<root>/checkpoints/<project>/<n>/tasks/    checkpoint n's pebbles, one file each
//	<root>/checkpoints/<project>/<n>/boulders.json  how those pebbles group
//	<root>/checkpoints/<project>/passes/<n>.json    a pass that is turning now
//	<root>/checkpoints/ledger.tsv              what each planning pass cost
//
// A missing piece is not an error. A project with a route and no plans is a
// boulder whose checkpoints are all still route lines, which is the normal
// state of a fresh route and has to render.
const (
	roadmapMaxProjects    = 64
	roadmapMaxCheckpoints = 200
	roadmapMaxPebbles     = 100
	roadmapMaxFileBytes   = 1 << 20
	roadmapMaxLedgerRows  = 20000
	roadmapMaxSummary     = 240
	// A pass marker is written when a pass starts and removed on every exit
	// path, so one this old belongs to a pass that was killed rather than to
	// one still running. Planning passes are minutes long, not hours.
	roadmapMarkerMaxAge = 2 * time.Hour
)

// RoadmapPebble is one unit of work a checkpoint was split into. The title is
// the task file's own heading, so it reads the way the orchestrator wrote it.
// State, WorkID and PullRequestURL are not on disk: they are joined in from the
// factory's own Work rows by name, so a pebble can say whether it was actually
// built rather than only that it was planned. An unjoined pebble has an empty
// state, which is different from a pebble whose build failed.
type RoadmapPebble struct {
	Ordinal        int    `json:"ordinal"`
	Slug           string `json:"slug"`
	Title          string `json:"title"`
	Summary        string `json:"summary,omitempty"`
	State          string `json:"state,omitempty"`
	WorkID         string `json:"work_id,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
}

// RoadmapBoulder is one big chunk of work inside a checkpoint: the answer to
// "what is this checkpoint made of". The orchestrator writes the grouping to
// <n>/boulders.json. A checkpoint split before that file existed, or one whose
// manifest forgot a pebble, still shows every pebble, because a missing group
// falls back to one boulder holding the rest. Nothing is hidden by a manifest.
type RoadmapBoulder struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Statement string          `json:"statement,omitempty"`
	Pebbles   []RoadmapPebble `json:"pebbles"`
	State     string          `json:"state"`
}

// RoadmapPass is one planning pass out of the ledger: a draft, a critique, a
// revision. Cost is what that single pass cost, which is the number that says
// whether a critic that found nothing was worth running.
type RoadmapPass struct {
	At         time.Time `json:"at"`
	Mode       string    `json:"mode"`
	Round      int       `json:"round"`
	Model      string    `json:"model,omitempty"`
	CostUSD    float64   `json:"cost_usd"`
	DurationMS int       `json:"duration_ms,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
}

// RoadmapLivePass is a pass that is turning at this moment. bin/checkpoint-pass
// holds a marker file for as long as its model runs and removes it on every
// exit path, because the ledger only gains a row once a pass has finished: with
// nothing else on disk, a pass in flight would be invisible. A killed pass
// cannot remove its own marker, so a marker older than roadmapMarkerMaxAge is
// read as the debris it is and ignored.
type RoadmapLivePass struct {
	Mode    string    `json:"mode"`
	Round   int       `json:"round"`
	Model   string    `json:"model,omitempty"`
	Started time.Time `json:"started"`
}

// RoadmapCheckpoint is one rung of a project's route, and the unit that has a
// written PRD. Boulders is the grouped view of the same pebbles Pebbles holds
// flat; both are sent because the page reads the grouping and the counts read
// the flat list.
type RoadmapCheckpoint struct {
	Number     int              `json:"number"`
	Title      string           `json:"title"`
	Summary    string           `json:"summary,omitempty"`
	Status     string           `json:"status"`
	Planned    bool             `json:"planned"`
	Boulders   []RoadmapBoulder `json:"boulders"`
	Pebbles    []RoadmapPebble  `json:"pebbles"`
	Passes     []RoadmapPass    `json:"passes"`
	Live       *RoadmapLivePass `json:"live,omitempty"`
	CostUSD    float64          `json:"cost_usd"`
	PassRounds int              `json:"pass_rounds"`
}

// RoadmapProject is one project's route: the thing the human said they wanted,
// and the checkpoints that get there. It is keyed by Project, the directory
// name, because that is the name the orchestrator's own commands take.
type RoadmapProject struct {
	Project     string              `json:"project"`
	Title       string              `json:"title"`
	Statement   string              `json:"statement,omitempty"`
	Checkpoints []RoadmapCheckpoint `json:"checkpoints"`
	Live        *RoadmapLivePass    `json:"live,omitempty"`
	CostUSD     float64             `json:"cost_usd"`
	BuiltCount  int                 `json:"built_count"`
}

// RoadmapWaiting is a checkpoint that cannot move until the human does
// something. It is derived, never stored, so it cannot go stale against the
// files it was read from.
type RoadmapWaiting struct {
	Project    string  `json:"project"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason"`
	Action     string  `json:"action"`
	CostUSD    float64 `json:"cost_usd"`
	PassRounds int     `json:"pass_rounds"`
}

// Roadmap is the whole response. Configured false means no roadmap root was
// named, which is the default and is not an error: the cockpit shows an empty
// state explaining what to point it at.
type Roadmap struct {
	Configured bool             `json:"configured"`
	Projects   []RoadmapProject `json:"projects"`
	Waiting    []RoadmapWaiting `json:"waiting"`
	ReadAt     time.Time        `json:"read_at"`
}

var (
	roadmapRouteTitle    = regexp.MustCompile(`(?m)^#\s+(?:Route:\s*)?(.+?)\s*$`)
	roadmapCheckpointRow = regexp.MustCompile(`^(\d{1,3})\.\s+(.*)$`)
	roadmapStatusSuffix  = regexp.MustCompile(`(?i)\.?\s*Status:\s*([A-Za-z-]+)\s*$`)
	roadmapPRDStatus     = regexp.MustCompile(`(?im)^Status:\s*([A-Za-z-]+)\s*$`)
	roadmapPRDTitle      = regexp.MustCompile(`(?m)^#\s+Checkpoint\s+\d+:\s*(.+?)\s*$`)
	roadmapPebbleTitle   = regexp.MustCompile(`(?m)^#{1,3}\s+(.+?)\s*$`)
	roadmapPebbleOrdinal = regexp.MustCompile(`^(\d{1,3})[-_]`)
	roadmapPebbleSection = regexp.MustCompile(`(?m)^#{2,4}\s+What are we building\?\s*$`)
)

// roadmapStatuses is the closed set the cockpit styles. A status the
// orchestrator invents later renders as planned rather than as an unstyled
// gap, and the raw word is not shown, because a status nothing can colour is
// worse than an honest default.
var roadmapStatuses = map[string]bool{
	"planned":  true,
	"drafting": true,
	"review":   true,
	"fog":      true,
	"frozen":   true,
	"built":    true,
}

// readRoadmap parses the whole roadmap under root. An unreadable project is
// skipped rather than failing the request: one malformed route file should not
// hide every other project.
func readRoadmap(root string) (Roadmap, error) {
	roadmap := Roadmap{ReadAt: time.Now().UTC()}
	if strings.TrimSpace(root) == "" {
		return roadmap, nil
	}
	roadmap.Configured = true
	roadmap.Projects = []RoadmapProject{}
	roadmap.Waiting = []RoadmapWaiting{}
	checkpointsDir := filepath.Join(root, "checkpoints")
	entries, err := os.ReadDir(checkpointsDir)
	if errors.Is(err, os.ErrNotExist) {
		return roadmap, nil
	}
	if err != nil {
		return Roadmap{}, fmt.Errorf("read roadmap root: %w", err)
	}
	ledger := readRoadmapLedger(filepath.Join(checkpointsDir, "ledger.tsv"))
	projects := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		projects = append(projects, entry.Name())
	}
	sort.Strings(projects)
	if len(projects) > roadmapMaxProjects {
		projects = projects[:roadmapMaxProjects]
	}
	for _, project := range projects {
		read, ok := readRoadmapProject(filepath.Join(checkpointsDir, project), project, ledger)
		if !ok {
			continue
		}
		roadmap.Projects = append(roadmap.Projects, read)
	}
	roadmap.Waiting = roadmapWaiting(roadmap.Projects)
	return roadmap, nil
}

func readRoadmapProject(dir, project string, ledger map[string][]RoadmapPass) (RoadmapProject, bool) {
	route, err := readRoadmapFile(filepath.Join(dir, "route.md"))
	if err != nil {
		return RoadmapProject{}, false
	}
	live := readRoadmapLive(dir)
	read := RoadmapProject{
		Project:     project,
		Title:       roadmapRouteHeading(route, project),
		Statement:   roadmapSection(route, "Boulder"),
		Checkpoints: []RoadmapCheckpoint{},
	}
	for _, line := range roadmapCheckpointLines(route) {
		match := roadmapCheckpointRow.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil || number <= 0 {
			continue
		}
		checkpoint := RoadmapCheckpoint{Number: number, Status: "planned", Boulders: []RoadmapBoulder{}, Pebbles: []RoadmapPebble{}, Passes: []RoadmapPass{}}
		body := match[2]
		if status := roadmapStatusSuffix.FindStringSubmatch(body); status != nil {
			checkpoint.Status = roadmapStatus(status[1])
			body = strings.TrimSpace(roadmapStatusSuffix.ReplaceAllString(body, ""))
		}
		checkpoint.Title, checkpoint.Summary = roadmapSplitTitle(body)
		checkpointDir := filepath.Join(dir, strconv.Itoa(number))
		roadmapApplyPlan(&checkpoint, dir)
		checkpoint.Pebbles = readRoadmapPebbles(filepath.Join(checkpointDir, "tasks"))
		checkpoint.Boulders = readRoadmapBoulders(checkpointDir, checkpoint.Pebbles)
		checkpoint.Live = live[number]
		checkpoint.Passes = ledger[roadmapLedgerKey(project, number)]
		if checkpoint.Passes == nil {
			checkpoint.Passes = []RoadmapPass{}
		}
		for _, pass := range checkpoint.Passes {
			checkpoint.CostUSD += pass.CostUSD
		}
		checkpoint.PassRounds = len(checkpoint.Passes)
		read.CostUSD += checkpoint.CostUSD
		if checkpoint.Status == "built" {
			read.BuiltCount++
		}
		read.Checkpoints = append(read.Checkpoints, checkpoint)
		if len(read.Checkpoints) >= roadmapMaxCheckpoints {
			break
		}
	}
	// A route pass is charged to the project, not to any one checkpoint, so it
	// is added here rather than inside the loop above. A route pass in flight
	// lands on the project for the same reason.
	for _, pass := range ledger[roadmapLedgerKey(project, 0)] {
		read.CostUSD += pass.CostUSD
	}
	read.Live = live[0]
	sort.SliceStable(read.Checkpoints, func(i, j int) bool {
		return read.Checkpoints[i].Number < read.Checkpoints[j].Number
	})
	return read, true
}

// readRoadmapLive reads the markers bin/checkpoint-pass holds while a pass runs,
// keyed by the checkpoint the pass is for. A route pass is written as "-",
// because it belongs to the project and to no checkpoint, and is keyed 0 here.
// Anything unreadable, unparseable, or stale is skipped: this is a hint that a
// pass is turning, and a wrong hint is worse than none.
func readRoadmapLive(dir string) map[int]*RoadmapLivePass {
	live := map[int]*RoadmapLivePass{}
	passesDir := filepath.Join(dir, "passes")
	entries, err := os.ReadDir(passesDir)
	if err != nil {
		return live
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		number := 0
		if stem := strings.TrimSuffix(name, ".json"); stem != "-" {
			parsed, err := strconv.Atoi(stem)
			if err != nil || parsed <= 0 {
				continue
			}
			number = parsed
		}
		body, err := readRoadmapFile(filepath.Join(passesDir, name))
		if err != nil {
			continue
		}
		var pass RoadmapLivePass
		if err := json.Unmarshal([]byte(body), &pass); err != nil {
			continue
		}
		pass.Mode = strings.TrimSpace(pass.Mode)
		if pass.Mode == "" || pass.Started.IsZero() {
			continue
		}
		if time.Since(pass.Started) > roadmapMarkerMaxAge {
			continue
		}
		marker := pass
		live[number] = &marker
		if len(live) >= roadmapMaxCheckpoints {
			break
		}
	}
	return live
}

// roadmapBoulderFile is the on-disk shape of <n>/boulders.json, written by the
// orchestrator's pebble pass. It names pebbles by slug, so this file and the
// task files can be read independently and joined here.
type roadmapBoulderFile struct {
	Boulders []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Statement string   `json:"statement"`
		Pebbles   []string `json:"pebbles"`
	} `json:"boulders"`
}

// readRoadmapBoulders groups a checkpoint's pebbles the way boulders.json says
// to. Every pebble reaches the page exactly once: one the manifest forgot goes
// into a trailing catch-all, and a checkpoint with no manifest at all becomes a
// single boulder holding everything. A manifest naming a pebble that is not on
// disk names nothing, which is the orchestrator's error to fix, not this
// reader's to guess at.
func readRoadmapBoulders(checkpointDir string, pebbles []RoadmapPebble) []RoadmapBoulder {
	boulders := []RoadmapBoulder{}
	if len(pebbles) == 0 {
		return boulders
	}
	bySlug := make(map[string]RoadmapPebble, len(pebbles))
	for _, pebble := range pebbles {
		bySlug[pebble.Slug] = pebble
	}
	raw, err := readRoadmapFile(filepath.Join(checkpointDir, "boulders.json"))
	var manifest roadmapBoulderFile
	if err == nil {
		if jsonErr := json.Unmarshal([]byte(raw), &manifest); jsonErr != nil {
			manifest.Boulders = nil
		}
	}
	grouped := map[string]bool{}
	for _, entry := range manifest.Boulders {
		boulder := RoadmapBoulder{ID: entry.ID, Title: entry.Title, Statement: entry.Statement, Pebbles: []RoadmapPebble{}}
		for _, slug := range entry.Pebbles {
			pebble, ok := bySlug[slug]
			if !ok || grouped[slug] {
				continue
			}
			grouped[slug] = true
			boulder.Pebbles = append(boulder.Pebbles, pebble)
		}
		if len(boulder.Pebbles) == 0 {
			continue
		}
		if strings.TrimSpace(boulder.ID) == "" {
			boulder.ID = fmt.Sprintf("B%d", len(boulders)+1)
		}
		if strings.TrimSpace(boulder.Title) == "" {
			boulder.Title = boulder.ID
		}
		boulders = append(boulders, boulder)
	}
	rest := []RoadmapPebble{}
	for _, pebble := range pebbles {
		if !grouped[pebble.Slug] {
			rest = append(rest, pebble)
		}
	}
	if len(rest) > 0 {
		catchAll := RoadmapBoulder{
			ID:      fmt.Sprintf("B%d", len(boulders)+1),
			Title:   "The rest of the checkpoint",
			Pebbles: rest,
		}
		if len(boulders) == 0 {
			catchAll.Title = "Everything in this checkpoint"
		}
		boulders = append(boulders, catchAll)
	}
	for i := range boulders {
		boulders[i].State = roadmapRollUp(boulders[i].Pebbles)
	}
	return boulders
}

// roadmapApplyPlan lets a written plan override the route line. The route says
// what a checkpoint will be; the plan file is what it became, so its Status and
// its own heading win wherever both exist.
func roadmapApplyPlan(checkpoint *RoadmapCheckpoint, dir string) {
	plan, err := readRoadmapFile(filepath.Join(dir, strconv.Itoa(checkpoint.Number)+".md"))
	if err != nil {
		return
	}
	checkpoint.Planned = true
	if status := roadmapPRDStatus.FindStringSubmatch(plan); status != nil {
		checkpoint.Status = roadmapStatus(status[1])
	}
	if title := roadmapPRDTitle.FindStringSubmatch(plan); title != nil {
		checkpoint.Title = strings.TrimSpace(title[1])
	}
}

func readRoadmapPebbles(dir string) []RoadmapPebble {
	pebbles := []RoadmapPebble{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return pebbles
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if len(pebbles) >= roadmapMaxPebbles {
			break
		}
		slug := strings.TrimSuffix(name, ".md")
		pebble := RoadmapPebble{Ordinal: len(pebbles) + 1, Slug: slug, Title: slug}
		if match := roadmapPebbleOrdinal.FindStringSubmatch(slug); match != nil {
			if ordinal, err := strconv.Atoi(match[1]); err == nil {
				pebble.Ordinal = ordinal
			}
		}
		if body, err := readRoadmapFile(filepath.Join(dir, name)); err == nil {
			if title := roadmapPebbleTitle.FindStringSubmatch(body); title != nil {
				pebble.Title = strings.TrimSpace(title[1])
			}
			pebble.Summary = roadmapPebbleSummary(body)
		}
		pebbles = append(pebbles, pebble)
	}
	return pebbles
}

// roadmapPebbleSummary is the first paragraph of a task file's opening section,
// which is where the orchestrator's own task template says what is being built.
// It is capped because this is a card subtitle, not the task.
func roadmapPebbleSummary(body string) string {
	rest := body
	if idx := roadmapPebbleSection.FindStringIndex(rest); idx != nil {
		rest = rest[idx[1]:]
	} else if idx := roadmapPebbleTitle.FindStringIndex(rest); idx != nil {
		rest = rest[idx[1]:]
	}
	for _, block := range strings.Split(rest, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") {
			continue
		}
		block = strings.Join(strings.Fields(block), " ")
		if len(block) > roadmapMaxSummary {
			block = strings.TrimSpace(block[:roadmapMaxSummary]) + "..."
		}
		return block
	}
	return ""
}

// roadmapWaiting is the whole point of the page: the checkpoints that stopped
// because they need the human, and nothing else. A checkpoint the factory is
// still building is not waiting on anyone and does not appear.
func roadmapWaiting(projects []RoadmapProject) []RoadmapWaiting {
	waiting := []RoadmapWaiting{}
	for _, project := range projects {
		for _, checkpoint := range project.Checkpoints {
			reason, action := "", ""
			switch checkpoint.Status {
			case "review":
				reason = "The plan is written and waiting for your answers. It cannot become tasks until you review it."
				action = "Review the plan"
			case "fog":
				reason = "Drafting stopped on questions it could not answer from the repository."
				action = "Answer the questions"
			case "frozen":
				if len(checkpoint.Pebbles) == 0 {
					reason = "Frozen and ready to be split into tasks."
					action = "Split into pebbles"
				}
			}
			if reason == "" {
				continue
			}
			waiting = append(waiting, RoadmapWaiting{
				Project:    project.Project,
				Number:     checkpoint.Number,
				Title:      checkpoint.Title,
				Status:     checkpoint.Status,
				Reason:     reason,
				Action:     action,
				CostUSD:    checkpoint.CostUSD,
				PassRounds: checkpoint.PassRounds,
			})
		}
	}
	return waiting
}

// readRoadmapLedger reads the planning cost ledger by header name rather than
// by column position, so a column added to the left of cost_usd later does not
// silently start reporting the wrong number.
func readRoadmapLedger(path string) map[string][]RoadmapPass {
	passes := map[string][]RoadmapPass{}
	file, err := os.Open(path)
	if err != nil {
		return passes
	}
	defer file.Close()
	reader := csv.NewReader(bufio.NewReader(io.LimitReader(file, roadmapMaxFileBytes*8)))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return passes
	}
	column := map[string]int{}
	for index, name := range header {
		column[strings.TrimSpace(name)] = index
	}
	field := func(row []string, name string) string {
		index, ok := column[name]
		if !ok || index >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[index])
	}
	for rows := 0; rows < roadmapMaxLedgerRows; rows++ {
		row, err := reader.Read()
		if err != nil {
			break
		}
		project := field(row, "project")
		if project == "" {
			continue
		}
		number := 0
		if raw := field(row, "checkpoint"); raw != "" && raw != "-" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				continue
			}
			number = parsed
		}
		pass := RoadmapPass{
			Mode:    strings.ToLower(field(row, "mode")),
			Model:   field(row, "model"),
			Outcome: field(row, "outcome"),
		}
		if at, err := time.Parse(time.RFC3339, field(row, "ts")); err == nil {
			pass.At = at.UTC()
		}
		pass.Round, _ = strconv.Atoi(field(row, "round"))
		pass.CostUSD, _ = strconv.ParseFloat(field(row, "cost_usd"), 64)
		pass.DurationMS, _ = strconv.Atoi(field(row, "duration_ms"))
		key := roadmapLedgerKey(project, number)
		passes[key] = append(passes[key], pass)
	}
	for key := range passes {
		rows := passes[key]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
		passes[key] = rows
	}
	return passes
}

func roadmapLedgerKey(project string, number int) string {
	return project + "\x00" + strconv.Itoa(number)
}

// roadmapRouteHeading takes the first heading in the route file, with a
// "Route:" prefix stripped when the orchestrator wrote one. Routes exist that
// open with a sentence instead of a heading, so the project name is the
// fallback rather than an empty title.
func roadmapRouteHeading(body, project string) string {
	if match := roadmapRouteTitle.FindStringSubmatch(body); match != nil {
		if title := strings.TrimSpace(match[1]); title != "" {
			return title
		}
	}
	return project
}

// roadmapSection returns the prose under a named heading, stopping at the next
// heading of any level.
func roadmapSection(body, heading string) string {
	lines := strings.Split(body, "\n")
	collected := make([]string, 0, 8)
	inside := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if inside {
				break
			}
			inside = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(trimmed, "# ")), heading)
			continue
		}
		if inside && trimmed != "" {
			collected = append(collected, trimmed)
		}
	}
	return strings.Join(collected, " ")
}

func roadmapCheckpointLines(body string) []string {
	lines := strings.Split(body, "\n")
	collected := make([]string, 0, 16)
	inside := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if inside {
				break
			}
			inside = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(trimmed, "# ")), "Checkpoints")
			continue
		}
		if inside && trimmed != "" {
			collected = append(collected, trimmed)
		}
	}
	return collected
}

// roadmapSplitTitle takes the orchestrator's "Title: the whole slice in one
// sentence" route line apart. The colon has to be the one that ends a short
// title, not one inside the sentence, so a long left side is treated as a line
// with no title at all rather than as a title nobody would read.
func roadmapSplitTitle(body string) (string, string) {
	body = strings.TrimSpace(body)
	index := strings.Index(body, ": ")
	if index <= 0 || index > 90 {
		return body, ""
	}
	return strings.TrimSpace(body[:index]), strings.TrimSpace(body[index+2:])
}

func roadmapStatus(raw string) string {
	status := strings.ToLower(strings.TrimSpace(raw))
	if roadmapStatuses[status] {
		return status
	}
	return "planned"
}

// readRoadmapFile refuses anything that is not a regular file, which keeps a
// symlink planted under the roadmap root from reading a file elsewhere on the
// machine, and caps the size so a stray large file cannot be pulled into a
// response.
func readRoadmapFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("roadmap file is not a regular file")
	}
	if info.Size() > roadmapMaxFileBytes {
		return "", errors.New("roadmap file is too large")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
