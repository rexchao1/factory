package worker

import (
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestDeliveryBodyUsesBoundedPriorEvidence(t *testing.T) {
	body := deliveryBody("Fix the thing", strings.Repeat("x", protocol.MaxStageHandoffBytes+100))
	if !strings.Contains(body, "## Summary\n\nFix the thing") ||
		!strings.Contains(body, "## Verification and review") {
		t.Fatalf("unexpected delivery body: %q", body)
	}
	if len(body) > protocol.MaxStageHandoffBytes+200 {
		t.Fatalf("delivery body retained unbounded evidence: %d bytes", len(body))
	}
}

func TestDeliveryTitleRemovesOnlyAdmissionSuffix(t *testing.T) {
	if got := admissionTitleSuffix.ReplaceAllString("Fix payer (a1b2c3d4)", ""); got != "Fix payer" {
		t.Fatalf("title = %q", got)
	}
	if got := admissionTitleSuffix.ReplaceAllString("Keep (human text)", ""); got != "Keep (human text)" {
		t.Fatalf("ordinary title changed to %q", got)
	}
}
