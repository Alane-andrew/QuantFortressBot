package profit

import (
	"math"
	"sync"
)

// Trade represents a single transaction, containing all details needed for precise profit/loss tracking.
type Trade struct {
	Side        string  // Transaction direction, "BUY" or "SELL"
	Price       float64 // Execution price
	Quantity    float64 // Execution quantity
	Timestamp   int64   // Transaction timestamp
	TradeType   string  // Transaction type, such as "GRID", "HEDGE", "LIQUIDATION", etc.
	PairedPrice float64 // For grid closing transactions, this field records the paired opening order price.
}

// PositionState represents the overall position state of an asset.
type PositionState struct {
	TotalQuantity       float64 // Total quantity of assets held. Negative for short positions.
	AverageCost         float64 // Current weighted average cost of the position.
	RealizedProfit      float64 // Cumulative profit from closed transactions.
	UnrealizedProfit    float64 // Floating profit/loss of current position.
	RealizedGridProfit  float64 // New: Grid realized profit
	RealizedHedgeProfit float64 // New: Hedge realized profit
}

// Accountant is responsible for tracking transactions, calculating and managing position profits and losses.
// It uses weighted average cost method to handle positions.
type Accountant struct {
	mu           sync.Mutex
	position     PositionState
	tradeHistory []Trade
}

// NewAccountant creates a new accounting core.
func NewAccountant() *Accountant {
	return &Accountant{
		position: PositionState{
			TotalQuantity:    0,
			AverageCost:      0,
			RealizedProfit:   0,
			UnrealizedProfit: 0,
		},
		tradeHistory: make([]Trade, 0),
	}
}

// RecordTrade records a new transaction and updates position state.
// This is the core logic for the entire profit/loss calculation.
func (a *Accountant) RecordTrade(trade Trade) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tradeHistory = append(a.tradeHistory, trade)

	isBuy := trade.Side == "BUY"
	tradeQty := trade.Quantity
	currentPosQty := a.position.TotalQuantity
	currentAvgCost := a.position.AverageCost

	// Determine if it's a closing transaction (transaction direction opposite to position direction)
	isClosingTrade := (currentPosQty > 0 && !isBuy) || (currentPosQty < 0 && isBuy)

	// --- 1. Generalized realized profit calculation ---
	// Applicable to all closing transactions (grid, hedge, etc.)
	if isClosingTrade && currentPosQty != 0 {
		// Determine actual closing quantity (cannot exceed existing position quantity)
		qtyToClose := math.Min(math.Abs(currentPosQty), tradeQty)
		var pnl float64

		if isBuy { // Buy to close short position
			pnl = (currentAvgCost - trade.Price) * qtyToClose
		} else { // Sell to close long position
			pnl = (trade.Price - currentAvgCost) * qtyToClose
		}
		a.position.RealizedProfit += pnl
	}

	// --- 2. Update position state (quantity and average cost) ---
	signedTradeQty := tradeQty
	if !isBuy {
		signedTradeQty = -tradeQty
	}

	// If transaction direction is the same as current position direction, it's a simple position increase.
	if (currentPosQty >= 0 && isBuy) || (currentPosQty <= 0 && !isBuy) {
		oldValue := currentAvgCost * math.Abs(currentPosQty)
		newValue := oldValue + (trade.Price * tradeQty)

		a.position.TotalQuantity += signedTradeQty
		if a.position.TotalQuantity != 0 {
			a.position.AverageCost = newValue / math.Abs(a.position.TotalQuantity)
		} else {
			a.position.AverageCost = 0
		}
	} else { // If transaction direction is opposite, it means partial or full position closing.
		a.position.TotalQuantity += signedTradeQty

		// If position is only partially closed without reversal, the average cost of remaining position doesn't change.
		// If position is reversed (e.g., from -10 to +5), then the average cost of the new position is the price of this transaction.
		if currentPosQty*a.position.TotalQuantity < 0 { // Sign change, meaning position direction reversal
			a.position.AverageCost = trade.Price
		} else if a.position.TotalQuantity == 0 {
			a.position.AverageCost = 0
		}
	}
}

// UpdateUnrealizedProfit calculates unrealized floating profit/loss based on current market latest price.
func (a *Accountant) UpdateUnrealizedProfit(currentPrice float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.position.TotalQuantity != 0 {
		// Long position floating P&L = (current price - average cost) * position quantity
		// Short position floating P&L = (average cost - current price) * absolute value of position quantity
		if a.position.TotalQuantity > 0 {
			a.position.UnrealizedProfit = (currentPrice - a.position.AverageCost) * a.position.TotalQuantity
		} else {
			a.position.UnrealizedProfit = (a.position.AverageCost - currentPrice) * math.Abs(a.position.TotalQuantity)
		}
	} else {
		a.position.UnrealizedProfit = 0
	}
}

// GetPositionState returns a copy of the current position state.
func (a *Accountant) GetPositionState() PositionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.position
}

// Restore recovers realized profit from persistent state.
func (a *Accountant) Restore(realizedProfit float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.position.RealizedProfit = realizedProfit
}

// RecordPNL directly records a profit/loss, used for non-transaction events (such as funding fees) or P&L calculated by external modules.
func (a *Accountant) RecordPNL(pnl float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.position.RealizedProfit += pnl
}

// GetRealizedPNL returns cumulative realized profit.
func (a *Accountant) GetRealizedPNL() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.position.RealizedProfit
}

// If is a utility function for conditional expressions.
func If(condition bool, trueVal, falseVal float64) float64 {
	if condition {
		return trueVal
	}
	return falseVal
}
