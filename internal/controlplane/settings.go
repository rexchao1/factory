package controlplane

import (
	"context"
	"database/sql"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// factory_settings holds exactly one row, guarded by a CHECK on its primary
// key. It is the global floor of the stage execution precedence chain.

func (s *Store) StageDefaults(ctx context.Context) (protocol.StageDefaults, error) {
	var defaults protocol.StageDefaults
	err := s.db.QueryRowContext(ctx,
		`SELECT default_model, default_effort FROM factory_settings WHERE id = 1`,
	).Scan(&defaults.Model, &defaults.Effort)
	if err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return defaults, nil
}

// stageDefaultsTx reads the same row inside an open transaction. Admission
// resolves the chain while it holds the transaction that writes the Run, so it
// cannot use the Store method without reading outside its own snapshot.
func stageDefaultsTx(ctx context.Context, tx *sql.Tx) (protocol.StageDefaults, error) {
	var defaults protocol.StageDefaults
	err := tx.QueryRowContext(ctx,
		`SELECT default_model, default_effort FROM factory_settings WHERE id = 1`,
	).Scan(&defaults.Model, &defaults.Effort)
	if err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return defaults, nil
}

func (s *Store) SaveStageDefaults(
	ctx context.Context, input protocol.StageDefaults,
) (protocol.StageDefaults, error) {
	value := protocol.StageDefaults{
		Model:  strings.TrimSpace(input.Model),
		Effort: strings.TrimSpace(input.Effort),
	}
	// Empty clears the default, which is a real operation: it returns the
	// factory to passing no argument at all.
	if value.Model != "" && !protocol.SupportedModel(protocol.RuntimeClaudeCode, value.Model) {
		return protocol.StageDefaults{}, invalid(
			"invalid_stage_default_model", "default model must be one of: opus, sonnet, haiku, fable")
	}
	if value.Effort != "" && !protocol.SupportedEffort(value.Effort) {
		return protocol.StageDefaults{}, invalid(
			"invalid_stage_default_effort", "default effort must be one of: low, medium, high, xhigh, max")
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE factory_settings SET default_model = ?, default_effort = ?, updated_at = ? WHERE id = 1`,
		value.Model, value.Effort, s.now().UnixMilli()); err != nil {
		return protocol.StageDefaults{}, unavailable(err)
	}
	return value, nil
}
