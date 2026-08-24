package protocol

import (
	"strings"
	"testing"
)

func TestFormatAgentPromptPreservesSafetyAndBranchContract(t *testing.T) {
	want := "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the Session before returning a concise result.\n\n" +
		"Task: Fix the prompt\n" +
		"Repository: github.com/owainlewis/factory\n" +
		"Working branch: factory/123456789abc-abcdef123456\n" +
		"Target base branch: main\n\n" +
		"Keep the change focused."

	if got := FormatAgentPrompt(
		"Fix the prompt",
		"github.com/owainlewis/factory",
		"factory/123456789abc-abcdef123456",
		"main",
		"Keep the change focused.",
	); got != want {
		t.Fatalf("FormatAgentPrompt() = %q, want %q", got, want)
	}
}

func TestFormatAgentUpdatePromptNamesImmutablePublishBranch(t *testing.T) {
	const publishBranch = "factory/work-1111111111111111"
	prompt := FormatAgentUpdatePrompt(
		"Fix the prompt",
		"github.com/owainlewis/factory",
		"factory/123456789abc-abcdef123456",
		"main",
		publishBranch,
		"Keep the change focused.",
	)
	if !strings.Contains(prompt, "Working branch: factory/123456789abc-abcdef123456") ||
		!strings.Contains(prompt, "Factory publish branch: "+publishBranch) ||
		!strings.Contains(prompt, AgentUpdatePromptContract) {
		t.Fatalf("agent-update prompt = %q", prompt)
	}
	if !AgentUpdatePromptFits(
		"Fix the prompt", "github.com/owainlewis/factory", publishBranch, "Keep the change focused.",
	) {
		t.Fatal("bounded agent-update prompt rejected the exact publish branch")
	}
}
