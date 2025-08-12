// investment/invest_manager.go
package investment

import (
	"auto_bian_go_1/exchange"
	"auto_bian_go_1/logs"
	"context"
	"math"
	"strings"
)

// IClient defines the client interface required by this module, convenient for testing
type IClient interface {
	GetPositionInfo(ctx context.Context, symbol, positionSide string) (*exchange.PositionInfo, error)
}

// Manager is responsible for monitoring total investment amount
type Manager struct {
	client            IClient
	symbol            string
	mainPositionSide  string
	hedgePositionSide string
	investmentLimit   float64
	isLimitExceeded   bool
}

// NewManager creates a new investment manager
func NewManager(client IClient, symbol, mainPosSide string, limit float64) *Manager {
	var hedgePosSide string
	// Determine hedge position direction based on main position direction
	if strings.ToLower(mainPosSide) == "long" {
		hedgePosSide = "SHORT"
	} else {
		hedgePosSide = "LONG"
	}

	return &Manager{
		client:            client,
		symbol:            symbol,
		mainPositionSide:  strings.ToUpper(mainPosSide),
		hedgePositionSide: hedgePosSide,
		investmentLimit:   limit,
	}
}

// CheckAndUpdate checks current investment amount and updates status
func (m *Manager) CheckAndUpdate() {
	if m.investmentLimit <= 0 { // If no limit is set, return directly
		if m.isLimitExceeded { // If previously exceeded limit, now need to restore
			m.isLimitExceeded = false
			logs.Infof("[Investment-Management-Restore] Investment limit removed or set to 0, resuming position opening.")
		}
		return
	}

	ctx := context.Background()
	mainPos, err1 := m.client.GetPositionInfo(ctx, m.symbol, m.mainPositionSide)
	hedgePos, err2 := m.client.GetPositionInfo(ctx, m.symbol, m.hedgePositionSide)

	if err1 != nil || err2 != nil {
		logs.Errorf("[Investment-Management-Error] Failed to get position information: %v, %v", err1, err2)
		return
	}

	currentInvestment := math.Abs(mainPos.Notional) + math.Abs(hedgePos.Notional)

	if currentInvestment >= m.investmentLimit {
		if !m.isLimitExceeded { // Print log when first exceeding limit
			logs.Warnf("[Investment-Management-Warning] Total notional value %.4f USDT has reached or exceeded limit %.4f USDT. Will prohibit new position opening.",
				currentInvestment, m.investmentLimit)
		}
		m.isLimitExceeded = true
	} else {
		if m.isLimitExceeded { // Print log when first returning to within limit
			logs.Infof("[Investment-Management-Restore] Total notional value %.4f USDT has fallen back below limit %.4f USDT. Resuming position opening.",
				currentInvestment, m.investmentLimit)
		}
		m.isLimitExceeded = false
	}
}

// IsTradingHalted returns whether new position opening should be paused
func (m *Manager) IsTradingHalted() bool {
	return m.isLimitExceeded
}
