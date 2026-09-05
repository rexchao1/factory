package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	MaxResolvedPromptBytes = 64 << 10
	MaxAgentPromptBytes    = 72 << 10
	MaxAgentBranchBytes    = 1 << 10
	// Stage handoffs carry concise verification or review evidence, not raw
	// command logs. Keeping them small prevents a noisy check from consuming
	// the next agent's context window.
	MaxStageHandoffBytes = 8 << 10
)

// AttributionPolicy is the one Factory-owned wording of the rule. It exists as
// a single constant because three separately worded copies had already grown
// across the prompt paths, and a policy stated three ways is three policies.
//
// FormatAgentPrompt is the single funnel every model-facing prompt passes
// through on every runtime, so applying it there covers each stage of each
// pipeline, resumed continuations included, without a per-path copy.
//
// This is prompt text and nothing more. Factory adds no commit hook, rewrites
// no commit, and rejects no delivery over it.
const AttributionPolicy = "Do not add “Generated with Claude Code”, " +
	"“Co-Authored-By: Claude”, or equivalent AI attribution trailers to commits, " +
	"pull requests, source files, documentation, or generated artifacts."

const AgentUpdatePromptContract = `Factory update contract:
This Work is unfinished until you call factory update. Use status running for useful progress only. Before exiting, report exactly one outcome: ready, needs-input, failed, or no-change. Ready requires --pr with the GitHub pull request URL. Needs-input ends this Attempt and requires a clean worktree with all changed work committed and pushed to the immutable Factory publish branch. Always include a concise non-empty --message.`

func ResolveTaskSchedulePrompt(prompt string, scheduledAt time.Time, cron, timezone string) (string, error) {
	occurrence, err := json.Marshal(struct {
		Type        string    `json:"type"`
		ScheduledAt time.Time `json:"scheduled_at"`
		Cron        string    `json:"cron"`
		Timezone    string    `json:"timezone"`
	}{"schedule", scheduledAt.UTC(), cron, timezone})
	if err != nil {
		return "", err
	}
	return prompt +
		"\n\nSchedule instruction:\n\n" +
		"Execute this Task for the scheduled occurrence. There is no provider item to revalidate." +
		"\n\nTrusted schedule occurrence:\n\n" + string(occurrence), nil
}

// StageReportContract asks for the bounded report Factory parses into the
// Outcome view. It is stated last in the wrapper, after the untrusted task
// text, so a task prompt cannot displace it.
//
// It asks only for what Factory can use. Anything an agent writes here is its
// own claim and is labelled agent-reported wherever it is shown, so the
// contract does not pretend to make the agent authoritative.
const StageReportContract = `End your result with this exact block, and keep it short:

Changes:
- <up to five bullets>

Verification:
- <command or check> - passed|failed|not-run

Risk:
- <none, or one concise caveat>

List a check under Verification only if you actually ran it. Do not report a test count Factory cannot see; name the command instead.`

func FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. " + AttributionPolicy +
		" Complete and verify the Session before returning a concise result.\n\n" +
		"Task: " + title + "\n" +
		"Repository: " + repository + "\n" +
		"Working branch: " + workingBranch + "\n" +
		"Target base branch: " + targetBaseBranch + "\n\n" +
		resolvedPrompt +
		"\n\n" + StageReportContract
}

func AgentPromptFits(title, repository, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentPrompt(title, repository, maxBranch, maxBranch, resolvedPrompt))) <= MaxAgentPromptBytes
}

func FormatAgentUpdatePrompt(
	title, repository, workingBranch, targetBaseBranch, publishBranch, resolvedPrompt string,
) string {
	return FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt) +
		"\n\nFactory publish branch: " + publishBranch +
		"\n\n" + AgentUpdatePromptContract
}

func AgentUpdatePromptFits(title, repository, publishBranch, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentUpdatePrompt(
		title, repository, maxBranch, maxBranch, publishBranch, resolvedPrompt,
	))) <= MaxAgentPromptBytes
}
