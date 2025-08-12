// risk/actions.go
package risk

import "fmt"

// Action is a generic interface for any action returned by the risk manager.
// It's currently used more as a marker, but could be extended.
type Action interface {
	Description() string
}

// === Specific Action Implementations ===

// NoOpAction represents that no action should be taken.
type NoOpAction struct{}

func (a *NoOpAction) Description() string { return "No operation." }

// OpenHedgeAction details the parameters for opening a new hedge position.
type OpenHedgeAction struct {
	Symbol       string
	Side         string
	PositionSide string
	Amount       float64
	Price        float64
}

func (a *OpenHedgeAction) Description() string {
	return fmt.Sprintf("Open hedge: %s %s on %s side, Amount: %.4f, Price: %.4f", a.Side, a.Symbol, a.PositionSide, a.Amount, a.Price)
}

// CloseHedgeAction details the parameters for closing an existing hedge position.
type CloseHedgeAction struct {
	Symbol       string
	Side         string
	PositionSide string
	Amount       float64
	Price        float64
	EntryPrice   float64 // Included to allow PNL calculation by the monitor
}

func (a *CloseHedgeAction) Description() string {
	return fmt.Sprintf("Close hedge: %s %s, Amount: %.4f, Price: %.4f", a.Side, a.Symbol, a.Amount, a.Price)
}

// LiquidateAndStopAction is a critical action to liquidate all positions and halt the strategy.
type LiquidateAndStopAction struct {
	Symbol string
}

func (a *LiquidateAndStopAction) Description() string {
	return fmt.Sprintf("Circuit break and liquidate all positions for %s", a.Symbol)
}

// HaltGridAction instructs the strategy to pause placing new open orders.
type HaltGridAction struct{}

func (a *HaltGridAction) Description() string {
	return "Halt grid trading"
}

// ResumeGridAction instructs the strategy to resume placing open orders.
type ResumeGridAction struct{}

func (a *ResumeGridAction) Description() string {
	return "Resume grid trading"
}
