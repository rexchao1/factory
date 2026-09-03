package protocol

// Stage kinds separate work a model does from work a command does. An agent
// stage carries a prompt and spawns a runtime. A code stage carries a command,
// runs it in the worktree, and never invokes a model, which is what INV-7
// requires and what makes a type check or a test suite free to run.
const (
	StageKindAgent = "agent"
	StageKindCode  = "code"

	MaxStageCommandBytes = 4096
)

// Network postures for a sandboxed execution profile. The design names three;
// broker is a fourth, added by Phase 7. allowlist needs an egress proxy that
// restricts egress to a host list and is rejected at validation rather than
// silently treated as open.
//
// broker is not that egress filter. It is bridge networking plus a route to
// the credential broker on the Worker's host, so an agent reaches third party
// APIs without holding their keys. Egress is still unrestricted, which is why
// it is a separate posture from allowlist rather than an implementation of it.
const (
	NetworkNone      = "none"
	NetworkAllowlist = "allowlist"
	NetworkOpen      = "open"
	NetworkBroker    = "broker"
)

// StageKind resolves the stored value. Stages frozen before stage kinds
// existed carry an empty string and must keep behaving as agent stages.
func StageKind(value string) string {
	if value == "" {
		return StageKindAgent
	}
	return value
}

func SupportedStageKind(value string) bool {
	return StageKind(value) == StageKindAgent || StageKind(value) == StageKindCode
}

func IsCodeStage(value string) bool { return StageKind(value) == StageKindCode }

func SupportedNetworkPosture(value string) bool {
	return value == NetworkNone || value == NetworkAllowlist ||
		value == NetworkOpen || value == NetworkBroker
}

// ImplementedNetworkPosture is the narrower question the fork can answer today.
func ImplementedNetworkPosture(value string) bool {
	return value == NetworkNone || value == NetworkOpen || value == NetworkBroker
}

// Sandbox is the frozen container posture for one Run. It is copied onto the
// execution snapshot at admission, so a profile edited mid Run cannot change
// the posture an already dispatched attempt executes under.
type Sandbox struct {
	Image   string `json:"image"`
	Network string `json:"network"`
	CPU     string `json:"cpu,omitempty"`
	Memory  string `json:"memory,omitempty"`
}
