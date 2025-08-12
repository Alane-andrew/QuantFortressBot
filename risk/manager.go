// risk/manager.go
package risk

import "auto_bian_go_1/state"

// RiskManager defines the interface for any risk management module.
// This allows the core logic (like the Orchestrator) to remain independent
// of the specific risk management strategy being used (e.g., BHLS, simple stop-loss, etc.).
type RiskManager interface {
	// CheckAndManageRisk is the core function called periodically to assess risk.
	// It takes the current market price and returns a slice of actions to be executed.
	CheckAndManageRisk(currentPrice float64) []Action

	// Restore hydrates the manager's state from a previously saved state object.
	Restore(st *state.BHLSState)

	// GetState captures the current internal state of the manager for persistence.
	GetState() *state.BHLSState

	// SetCumulativePNL allows external components (like a PNL tracker) to sync the total PNL.
	SetCumulativePNL(pnl float64)

	// GetCumulativePNL returns the current cumulative PNL known to the manager.
	GetCumulativePNL() float64

	// GetActiveHedgeCount returns the number of currently active hedge positions.
	GetActiveHedgeCount() int

	// IsBreakerTipped reports whether the emergency stop (breaker) has been triggered.
	IsBreakerTipped() bool
}
