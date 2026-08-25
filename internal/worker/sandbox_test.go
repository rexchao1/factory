package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestSandboxNetworkPostureMapsOntoDocker(t *testing.T) {
	cases := map[string]string{
		protocol.NetworkNone: "none",
		protocol.NetworkOpen: "bridge",
		// allowlist never reaches the Worker, because the control plane
		// rejects it at profile validation. If it ever did, the safe reading
		// is the closed one, not the open one.
		protocol.NetworkAllowlist: "none",
		"":                        "none",
	}
	for posture, want := range cases {
		if got := sandboxNetworkArgument(posture); got != want {
			t.Fatalf("sandboxNetworkArgument(%q) = %q, want %q", posture, got, want)
		}
	}
}

// INV-6: no agent process runs with the operator credential in its
// environment. Inside a container the environment is built from an allowlist,
// so anything not named is absent by construction.
func TestSandboxEnvironmentDropsEverythingOutsideTheAllowlist(t *testing.T) {
	source := []string{
		"FACTORY_UPDATE_TOKEN=report-token",
		"FACTORY_ATTEMPT_ID=attempt-1",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-token",
		"PATH=/usr/bin",
		"HOME=/home/factory",
		// The things that must not cross the boundary.
		"FACTORY_WORKER_ENROLLMENT_TOKEN=operator-secret",
		"AWS_SECRET_ACCESS_KEY=operator-secret",
		"OP_SERVICE_ACCOUNT_TOKEN=operator-secret",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		// An empty value is not worth passing.
		"TZ=",
	}
	environment := sandboxEnvironment(source)
	for _, entry := range environment {
		if strings.Contains(entry, "operator-secret") {
			t.Fatalf("environment carries an operator credential: %q", entry)
		}
	}
	for _, unwanted := range []string{"SSH_AUTH_SOCK", "TZ"} {
		for _, entry := range environment {
			if strings.HasPrefix(entry, unwanted+"=") {
				t.Fatalf("environment carries %s: %q", unwanted, entry)
			}
		}
	}
	for _, required := range []string{
		"FACTORY_UPDATE_TOKEN=report-token",
		"FACTORY_ATTEMPT_ID=attempt-1",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-token",
		"PATH=/usr/bin",
		"HOME=/home/factory",
	} {
		if !slices.Contains(environment, required) {
			t.Fatalf("environment is missing %q: %v", required, environment)
		}
	}
	if !slices.IsSorted(environment) {
		t.Fatalf("environment is not sorted, so the argument vector is unstable: %v", environment)
	}
}

func TestSandboxArgumentsMountTheWorktreeAtTheSamePath(t *testing.T) {
	arguments := sandboxArguments(
		protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkNone, CPU: "2", Memory: "4g"},
		"/Users/mickey/.factory/workers/worker/worktrees/attempt-1",
		"/Users/mickey/.factory/sockets/attempt-1.sock",
		[]string{"PATH=/usr/bin", "FACTORY_UPDATE_TOKEN=report-token"},
		"/usr/local/bin/claude",
		[]string{"--print", "--output-format", "stream-json"},
	)
	joined := strings.Join(arguments, " ")

	worktree := "/Users/mickey/.factory/workers/worker/worktrees/attempt-1"
	if !strings.Contains(joined, "--volume "+worktree+":"+worktree) {
		t.Fatalf("worktree is not mounted at its own path: %v", arguments)
	}
	if !strings.Contains(joined, "--workdir "+worktree) {
		t.Fatalf("workdir is not the worktree: %v", arguments)
	}
	socketDirectory := filepath.Dir("/Users/mickey/.factory/sockets/attempt-1.sock")
	if !strings.Contains(joined, "--volume "+socketDirectory+":"+socketDirectory) {
		t.Fatalf("the report socket directory is not mounted, so factory update cannot reach the Worker: %v", arguments)
	}
	if !strings.Contains(joined, "--network none") {
		t.Fatalf("network posture is missing: %v", arguments)
	}
	if !strings.Contains(joined, "--cpus 2") || !strings.Contains(joined, "--memory 4g") {
		t.Fatalf("resource limits are missing: %v", arguments)
	}

	// The image must come last before the runtime, and the runtime arguments
	// must survive in order, or the agent is invoked differently inside the
	// container than outside it.
	image := slices.Index(arguments, "factory/agent:1")
	if image < 0 || image != len(arguments)-5 {
		t.Fatalf("image is not immediately before the runtime command: %v", arguments)
	}
	if got := arguments[image+1:]; !slices.Equal(got,
		[]string{"/usr/local/bin/claude", "--print", "--output-format", "stream-json"}) {
		t.Fatalf("runtime command = %v", got)
	}
}

func TestSandboxArgumentsOmitUnsetLimitsAndSocket(t *testing.T) {
	arguments := sandboxArguments(
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkOpen},
		"/w", "", nil, "/bin/sh", []string{"-c", "true"},
	)
	joined := strings.Join(arguments, " ")
	if strings.Contains(joined, "--cpus") || strings.Contains(joined, "--memory") {
		t.Fatalf("unset limits must not appear: %v", arguments)
	}
	if strings.Count(joined, "--volume") != 1 {
		t.Fatalf("only the worktree should be mounted: %v", arguments)
	}
	if !strings.Contains(joined, "--network bridge") {
		t.Fatalf("open posture is missing: %v", arguments)
	}
}

// AC-7 and INV-6 against real Docker. The assertion is not that the argument
// vector looks right, it is that a container built this way cannot reach the
// network.
func TestSandboxNetworkNoneCannotReachTheNetwork(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available on this machine")
	}
	if _, err := exec.LookPath(dockerExecutable); err != nil {
		t.Skip("docker client is not on PATH")
	}
	worktree := t.TempDir()

	confined := sandboxArguments(
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		"/bin/sh", []string{"-c", "wget -q -T 5 -O - https://example.com"},
	)
	output, err := exec.Command(dockerExecutable, confined...).CombinedOutput()
	if err == nil {
		t.Fatalf("AC-7 violated: a network=none container reached the network: %s", output)
	}
	t.Logf("network=none container failed as required: %v: %s", err, strings.TrimSpace(string(output)))

	// The negative half. If the image simply cannot run, the assertion above
	// would pass for the wrong reason.
	reachable := sandboxArguments(
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		"/bin/sh", []string{"-c", "echo container-ran"},
	)
	output, err = exec.Command(dockerExecutable, reachable...).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "container-ran") {
		t.Fatalf("the confined container could not run at all, so the egress assertion proves nothing: %v: %s", err, output)
	}
}

// The environment the container actually receives, measured rather than
// inferred from the argument vector.
func TestSandboxContainerEnvironmentExcludesHostSecrets(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available on this machine")
	}
	if _, err := exec.LookPath(dockerExecutable); err != nil {
		t.Skip("docker client is not on PATH")
	}
	worktree := t.TempDir()
	arguments := sandboxArguments(
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "",
		[]string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"FACTORY_UPDATE_TOKEN=report-token",
			"AWS_SECRET_ACCESS_KEY=operator-secret",
		},
		"/bin/sh", []string{"-c", "env"},
	)
	output, err := exec.Command(dockerExecutable, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v: %s", err, output)
	}
	if strings.Contains(string(output), "operator-secret") {
		t.Fatalf("INV-6 violated: the container environment carries a host secret:\n%s", output)
	}
	if !strings.Contains(string(output), "FACTORY_UPDATE_TOKEN=report-token") {
		t.Fatalf("the report channel did not reach the container:\n%s", output)
	}
}

func TestDockerAvailableReadsTheEnvironment(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		t.Skip("this machine has a docker socket, so the negative case is not observable")
	}
	t.Setenv("DOCKER_HOST", "")
	if dockerAvailable() {
		t.Fatal("dockerAvailable must be false with no socket and no DOCKER_HOST")
	}
	t.Setenv("DOCKER_HOST", "unix:///somewhere/docker.sock")
	if !dockerAvailable() {
		t.Fatal("dockerAvailable must honour DOCKER_HOST")
	}
}
