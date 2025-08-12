// Package profit - Trailing Take-Profit (TTP) module
// Status: EXPERIMENTAL. Not enabled by default.
// This module is currently used for logic refinement and will be completed in future releases.
// Guarded by config flag: profit.enable_ttp.

package profit

import (
	"auto_bian_go_1/exchange"
	"context"
	"fmt"
)

// Action defines the common interface for actions that the manager can return
type Action interface {
	Execute() error
	Description() string
}

// PlaceOrderAction implements the order placement action
type PlaceOrderAction struct {
	Client       exchange.Client // Directly use the real client interface
	Symbol       string
	Side         string
	PositionSide string
	Qty          float64
	ReduceOnly   bool
}

// NewPlaceOrderAction is the public constructor for PlaceOrderAction
func NewPlaceOrderAction(client exchange.Client, symbol, side, posSide string, qty float64, reduceOnly bool) Action {
	return &PlaceOrderAction{
		Client:       client,
		Symbol:       symbol,
		Side:         side,
		PositionSide: posSide,
		Qty:          qty,
		ReduceOnly:   reduceOnly,
	}
}

// Execute performs the order placement action
func (a *PlaceOrderAction) Execute() error {
	order := &exchange.Order{
		Symbol:       a.Symbol,
		Side:         exchange.OrderSide(a.Side),
		PositionSide: exchange.PositionSide(a.PositionSide),
		Type:         exchange.Market, // TTP closing uses market orders
		OrigQty:      fmt.Sprintf("%f", a.Qty),
		ReduceOnly:   a.ReduceOnly,
	}
	_, err := a.Client.PlaceOrder(context.Background(), order)
	return err
}

// Description returns a text description of the action
func (a *PlaceOrderAction) Description() string {
	desc := fmt.Sprintf("Market order: %s %s, quantity: %.4f", a.Side, a.Symbol, a.Qty)
	if a.ReduceOnly {
		desc += " (reduce only)"
	}
	return desc
}
