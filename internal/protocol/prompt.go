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

func FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the Session before returning a concise result.\n\n" +
		"Task: " + title + "\n" +
		"Repository: " + repository + "\n" +
		"Working branch: " + workingBranch + "\n" +
		"Target base branch: " + targetBaseBranch + "\n\n" +
		resolvedPrompt
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
