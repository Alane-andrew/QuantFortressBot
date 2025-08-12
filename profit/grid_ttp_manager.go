// Package profit - Trailing Take-Profit (TTP) module
// Status: EXPERIMENTAL. Not enabled by default.
// This module is currently used for logic refinement and will be completed in future releases.
// Guarded by config flag: profit.enable_ttp.

package profit

import (
	"auto_bian_go_1/exchange"
	"auto_bian_go_1/logs"
)

// GridTakeProfitManager implements the "overall reset" trailing take-profit strategy.
type GridTakeProfitManager struct {
	Client            exchange.Client // Directly use the real client interface
	ProfitTarget      float64
	ProfitDrawdownPct float64

	isTracking         bool
	peakRealizedProfit float64
	gridSymbol         string
	gridDirection      string
}

// NewGridTakeProfitManager creates a new grid trailing take-profit manager.
func NewGridTakeProfitManager(client exchange.Client, symbol, direction string, profitTarget, drawdownPct float64) *GridTakeProfitManager {
	return &GridTakeProfitManager{
		Client:            client,
		gridSymbol:        symbol,
		gridDirection:     direction,
		ProfitTarget:      profitTarget,
		ProfitDrawdownPct: drawdownPct,
	}
}

// Check is the core method of the module.
func (m *GridTakeProfitManager) Check(currentRealizedProfit, currentPositionQty float64) []Action {
	// If both target and drawdown are not set (0), this module does not perform any operations
	if m.ProfitTarget <= 0 || m.ProfitDrawdownPct <= 0 {
		return nil
	}

	if !m.isTracking {
		if currentRealizedProfit >= m.ProfitTarget {
			m.isTracking = true
			m.peakRealizedProfit = currentRealizedProfit
			logs.Infof("[Trailing Take-Profit] -> Target profit achieved, activating tracking! Current profit: %.4f, Peak profit: %.4f",
				currentRealizedProfit, m.peakRealizedProfit)
		}
	} else {
		if currentRealizedProfit > m.peakRealizedProfit {
			m.peakRealizedProfit = currentRealizedProfit
			logs.Debugf("[Trailing Take-Profit] -> Profit reached new high: %.4f", m.peakRealizedProfit)
		}

		drawdownTriggerProfit := m.peakRealizedProfit * (1 - m.ProfitDrawdownPct/100)
		if currentRealizedProfit < drawdownTriggerProfit {
			logs.Warnf("[Trailing Take-Profit] ->!!! Take-profit triggered !!! Peak profit: %.4f, Current profit: %.4f, Trigger line: %.4f",
				m.peakRealizedProfit, currentRealizedProfit, drawdownTriggerProfit)

			if currentPositionQty == 0 {
				logs.Info("[Trailing Take-Profit] -> Detected position quantity is 0, no need to generate close order.")
				m.Reset()
				return nil
			}

			var side, posSide string
			if m.gridDirection == "long" {
				side, posSide = "SELL", "SHORT"
			} else {
				side, posSide = "BUY", "LONG"
			}

			closeAction := NewPlaceOrderAction(
				m.Client,
				m.gridSymbol,
				side,
				posSide,
				currentPositionQty,
				true,
			)

			logs.Infof("[Trailing Take-Profit] -> Generated liquidation order: %s", closeAction.Description())
			m.Reset()
			return []Action{closeAction}
		}
	}
	return nil
}

// Reset resets the manager state.
func (m *GridTakeProfitManager) Reset() {
	m.isTracking = false
	m.peakRealizedProfit = 0
	logs.Info("[Trailing Take-Profit] -> Manager state has been reset.")
}
