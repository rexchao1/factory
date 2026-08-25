package worker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// dockerExecutable is the client the Worker shells out to. It is a package
// variable rather than a constant so the sandbox tests can point it at a
// recording stub without needing Docker installed.
var dockerExecutable = "docker"

// sandboxEnvironmentAllowlist is the whole environment an agent gets inside a
// container. It is an allowlist rather than a denylist because INV-6 says no
// agent process runs with the operator credential in its environment, and a
// denylist can only exclude the credentials someone remembered to name.
//
// The persistent backend still copies os.Environ() wholesale, which is the
// clause of INV-6 that only the docker backend satisfies.
var sandboxEnvironmentAllowlist = []string{
	// The report channel. Without these factory update cannot reach the
	// Worker from inside the container, and no outcome is recordable.
	"FACTORY_RUN_ID",
	"FACTORY_SESSION_ID",
	"FACTORY_WORK_ID",
	"FACTORY_ATTEMPT_ID",
	"FACTORY_UPDATE_SOCKET",
	"FACTORY_UPDATE_TOKEN",
	// The agent's own credential. A long-lived OAuth token from
	// claude setup-token, which needs no Keychain and keeps subscription
	// authentication rather than falling back to a metered API key.
	"CLAUDE_CODE_OAUTH_TOKEN",
	// Delivery.
	"GH_TOKEN",
	"GITHUB_TOKEN",
	// Ordinary process hygiene.
	"HOME",
	"PATH",
	"LANG",
	"TZ",
}

// sandboxNetworkArgument maps a declared posture onto what docker understands.
// allowlist never reaches here: the control plane rejects it at profile
// validation, because docker alone cannot restrict egress to a host list.
func sandboxNetworkArgument(posture string) string {
	if posture == protocol.NetworkOpen {
		return "bridge"
	}
	return "none"
}

// sandboxEnvironment builds the container environment from the allowlist,
// taking values from the process environment the supervisor already assembled.
// Returns the docker -e arguments and the resolved values, sorted so the
// argument vector is stable and testable.
func sandboxEnvironment(source []string) []string {
	values := map[string]string{}
	for _, entry := range source {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[key] = value
	}
	allowed := make([]string, 0, len(sandboxEnvironmentAllowlist))
	for _, key := range sandboxEnvironmentAllowlist {
		value, present := values[key]
		if !present || value == "" {
			continue
		}
		allowed = append(allowed, key+"="+value)
	}
	sort.Strings(allowed)
	return allowed
}

// sandboxArguments builds the full docker run argument vector for one agent
// process.
//
// The worktree is mounted at the same absolute path inside the container as
// outside. The agent's prompt names host paths and the delivery evidence the
// Worker revalidates after the process stops is keyed on them, so a matching
// path removes a translation layer that would otherwise leak into INV-4.
func sandboxArguments(
	sandbox protocol.Sandbox,
	worktree string,
	updateSocket string,
	environment []string,
	runtimeExecutable string,
	runtimeArguments []string,
) []string {
	arguments := []string{
		"run", "--rm", "--init",
		"--network", sandboxNetworkArgument(sandbox.Network),
		"--volume", worktree + ":" + worktree,
		"--workdir", worktree,
	}
	// The report channel is a unix socket on the Worker's filesystem. Its
	// directory is mounted so factory update can reach it from inside.
	if updateSocket != "" {
		directory := filepath.Dir(updateSocket)
		arguments = append(arguments, "--volume", directory+":"+directory)
	}
	if sandbox.CPU != "" {
		arguments = append(arguments, "--cpus", sandbox.CPU)
	}
	if sandbox.Memory != "" {
		arguments = append(arguments, "--memory", sandbox.Memory)
	}
	for _, entry := range sandboxEnvironment(environment) {
		arguments = append(arguments, "--env", entry)
	}
	arguments = append(arguments, sandbox.Image, runtimeExecutable)
	return append(arguments, runtimeArguments...)
}

// dockerAvailable reports whether this machine can run the sandbox at all. Used
// by the integration test to skip rather than fail on a machine without Docker.
func dockerAvailable() bool {
	_, err := os.Stat("/var/run/docker.sock")
	if err == nil {
		return true
	}
	return os.Getenv("DOCKER_HOST") != ""
}
