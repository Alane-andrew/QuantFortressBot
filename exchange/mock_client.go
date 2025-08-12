package exchange

import (
	"auto_bian_go_1/config"
	"auto_bian_go_1/logs"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

//
// Complete mock client for running and testing strategies without real API
//

// Ensure MockClient implements Client interface
var _ Client = (*MockClient)(nil)

// MockClient is a mock implementation of the exchange.Client interface for testing.
type MockClient struct {
	mu                     sync.RWMutex // Upgraded to read-write lock
	positions              map[string]*PositionInfo
	openOrders             map[string]*Order // Active orders
	closedOrders           map[string]*Order // New: Store closed (filled/cancelled) orders
	nextOrderID            int64
	currentPrice           map[string]float64
	priceUpdateSubscribers []chan float64
	stopChan               chan struct{}
	subscribers            map[chan<- float64]bool
	priceIncrement         float64 // Price simulation step
	simulationMode         string  // "sine", "bhls_stress_test", "meltdown"
	simulationTime         float64 // For old sine wave simulation
	simInitialPrice        float64 // Starting price for simulated market
	simAmplitude           float64 // Amplitude for simulated market
	onOrderUpdate          OrderUpdateCallback

	// --- BHLS stress test specific state ---
	hedgeConfig              *config.BHLSConfig // Passed risk control parameters
	gridUpper                float64
	gridLower                float64
	stressTestPositionSide   string // "long" or "short"
	maxLossThreshold         float64
	hedgeStressPnl           float64
	stressTestDirection      int
	lastHedgeActionTime      time.Time
	lastHedgeEntryPrice      float64 // New: Record last hedge entry price for stop loss calculation
	strictPriceFilterEnabled bool

	// -- New: Add state for more realistic simulation --
	simulationSubMode string // attacking, recovering_hedge, replenishing_grid
	pendingGridPrices map[float64]bool
}

// HedgeTrade mirrors the structure in the risk package for the mock client's internal use.
type HedgeTrade struct {
	EntryPrice    float64
	Amount        float64
	StopLossPrice float64
	Level         int
}

// NewMockClient creates a new mock client.
func NewMockClient() *MockClient {
	mc := &MockClient{
		mu:                       sync.RWMutex{}, // Upgraded to read-write lock
		openOrders:               make(map[string]*Order),
		closedOrders:             make(map[string]*Order), // Initialize
		nextOrderID:              1,
		currentPrice:             make(map[string]float64),
		priceUpdateSubscribers:   make([]chan float64, 0),
		subscribers:              make(map[chan<- float64]bool),
		stopChan:                 make(chan struct{}),
		positions:                make(map[string]*PositionInfo),
		strictPriceFilterEnabled: false,
		simulationMode:           "sine", // Default sine wave
	}
	// Note: No longer automatically start goroutine here to avoid race conditions
	// go mc.runPriceSimulator()
	return mc
}

// Start starts the mock client's price update and order matching main loop.
// Must be called after the client is fully configured (e.g., after calling ActivateBHLSStressTest).
func (c *MockClient) Start() {
	go c.runPriceSimulator()
}

// Stop gracefully stops the simulator's internal goroutines.
func (c *MockClient) Stop() {
	close(c.stopChan)
}

// ActivateBHLSStressTest activates stress test mode for BHLS.
func (c *MockClient) ActivateBHLSStressTest(bhlsCfg *config.BHLSConfig, maxLoss, initialPrice, gridUpper, gridLower float64, positionSide string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.simulationMode = "bhls_stress_test"
	c.hedgeConfig = bhlsCfg
	c.maxLossThreshold = math.Abs(maxLoss) // Ensure it's positive
	c.simInitialPrice = initialPrice
	c.currentPrice["XRPUSDT"] = initialPrice // Assumed trading pair
	c.stressTestPositionSide = positionSide
	c.gridUpper = gridUpper           // Save grid upper bound
	c.gridLower = gridLower           // Save grid lower bound
	c.stressTestDirection = 1         // Initial direction: drive price toward hedge trigger direction
	c.lastHedgeEntryPrice = 0         // Initialize
	c.priceIncrement = 0.1            // **BUG fix**: Initialize price step to avoid price stagnation. **Debug acceleration**: Increase step size
	c.simulationSubMode = "attacking" // Initialize state machine, start in attack mode

	logs.Infof("[Mock Client] BHLS stress test mode activated. Target strategy: %s, max loss threshold: %.4f", positionSide, c.maxLossThreshold)
}

// SetPriceSimulationParams sets the parameters for simulated price.
func (c *MockClient) SetPriceSimulationParams(initialPrice, amplitude float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.simInitialPrice = initialPrice
	c.simAmplitude = amplitude
	// Set an initial price to prevent it from being 0 on the first loop
	for symbol := range c.currentPrice {
		c.currentPrice[symbol] = initialPrice
	}
	if len(c.currentPrice) == 0 && initialPrice > 0 {
		// If no trading pairs have been set yet, manually set one so the simulation can start
		// The trading pair name "XRPUSDT" is a reasonable assumption as it's used multiple times in the project
		c.currentPrice["XRPUSDT"] = initialPrice
	}
	logs.Infof("[Mock Client] Price simulator configured. Initial price: %.4f, amplitude: %.4f", initialPrice, amplitude)
}

// SyncTime simulates time synchronization, in the mock client, this is a no-op.
func (c *MockClient) SyncTime() error {
	// Mock client does not require network time synchronization
	logs.Debug("[Mock Client] Skipping time synchronization.")
	return nil
}

// EnableStrictPriceFilter enables or disables strict price filter mode.
func (c *MockClient) EnableStrictPriceFilter(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.strictPriceFilterEnabled = enabled
	if enabled {
		logs.Debug("[Mock Client] Strict price filter enabled.")
	} else {
		logs.Debug("[Mock Client] Strict price filter disabled.")
	}
}

// SetOrderUpdateCallback sets the callback function for order updates.
func (c *MockClient) SetOrderUpdateCallback(callback OrderUpdateCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// In the mock client, the callback function is used to notify order status changes
	// Note: This is implemented via WebSocket in a real client, here we simulate it with callbacks
	c.onOrderUpdate = callback
}

// GetSymbolInfo for MockClient returns a hardcoded, reasonable default for testing.
func (c *MockClient) GetSymbolInfo(symbol string) (SymbolInfo, bool) {
	// For testing purposes, we return a mock SymbolInfo.
	// This avoids having to implement the full exchange info endpoint in the mock.
	if symbol == "XRPUSDT" {
		return SymbolInfo{
			Symbol:            "XRPUSDT",
			Status:            "TRADING",
			PricePrecision:    4, // A common precision for price
			QuantityPrecision: 1, // A common precision for quantity for XRP
		}, true
	}
	// Return false for any other symbol to simulate not finding the info.
	return SymbolInfo{}, false
}

// SetMarginType simulates setting margin type.
func (c *MockClient) SetMarginType(symbol, marginType string) error {
	logs.Debugf("[Mock] Setting margin type for %s to %s (no-op)", symbol, marginType)
	return nil
}

func (c *MockClient) PlaceOrder(ctx context.Context, order *Order) (*Order, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Core fix: Distinguish "liquidation path" and "regular path"
	// When receiving a special liquidation instruction from the monitor, perform a fully synchronous blocking liquidation.
	isLiquidationTrigger := order.ReduceOnly && order.Type == Market && string(order.PositionSide) == c.stressTestPositionSide
	if isLiquidationTrigger {
		logs.Debugf("[Mock-Sync Liquidation] === Starting synchronous liquidation process (via PlaceOrder) ===")

		// 1. Synchronously clear all pending market orders
		c.forceSettlePendingMarketOrders_noLock_sync()

		// 2. Calculate the final real net position
		longPos, _, shortPos, _ := c.getCurrentPositions()
		netPos := longPos + shortPos // shortPos is negative
		logs.Infof("[Mock-Sync Liquidation] Final net position calculation: LONG: %.4f, SHORT: %.4f, NET: %.4f", longPos, shortPos, netPos)

		if math.Abs(netPos) < 0.0001 {
			logs.Debugf("[Mock-Sync Liquidation] Net position is already 0, no liquidation needed.")
		} else {
			// 3. Create and synchronously execute the final liquidation order
			finalQty := math.Abs(netPos)
			finalSide := Buy
			if netPos > 0 {
				finalSide = Sell
			}

			// Final fix: Directly create an order containing all correct information, no longer relying on helper functions
			c.nextOrderID++
			finalPrice := c.currentPrice[order.Symbol]
			finalLiquidationOrder := &Order{
				OrderID:      c.nextOrderID,
				Symbol:       order.Symbol,
				Type:         Market,
				Side:         finalSide,
				PositionSide: "BOTH",
				OrigQty:      strconv.FormatFloat(finalQty, 'f', -1, 64),
				AvgPrice:     strconv.FormatFloat(finalPrice, 'f', -1, 64),
				ReduceOnly:   true,
				Status:       "FILLED", // Set directly to FILLED as it's executed synchronously
			}

			logs.Infof("[Mock] Synchronous liquidation order filled: %s %s at %s, Qty: %s, OrderID: %d",
				finalLiquidationOrder.Side, finalLiquidationOrder.Symbol, finalLiquidationOrder.AvgPrice, finalLiquidationOrder.OrigQty, finalLiquidationOrder.OrderID)

			// Directly update position and order records
			c.updatePosition_noLock(finalLiquidationOrder)
			c.closedOrders[strconv.FormatInt(finalLiquidationOrder.OrderID, 10)] = finalLiquidationOrder
		}

		// 4. Force all positions to zero to complete liquidation
		logs.Debugf("[Mock-Sync Liquidation] Force all positions to zero to complete liquidation...")
		longPosToZero := c.findPosition_noLock(order.Symbol, "LONG")
		if longPosToZero != nil {
			longPosToZero.PositionAmt = 0
			longPosToZero.EntryPrice = 0
			longPosToZero.Notional = 0
		}
		shortPosToZero := c.findPosition_noLock(order.Symbol, "SHORT")
		if shortPosToZero != nil {
			shortPosToZero.PositionAmt = 0
			shortPosToZero.EntryPrice = 0
			shortPosToZero.Notional = 0
		}

		// 5. Final check
		finalLong, _, finalShort, _ := c.getCurrentPositions()
		logs.Infof("[Mock-Sync Liquidation] Final positions after liquidation: LONG: %.4f, SHORT: %.4f", finalLong, finalShort)
		if math.Abs(finalLong) > 0.0001 || math.Abs(finalShort) > 0.0001 {
			logs.Errorf("[Mock-Sync Liquidation] Critical error! Positions not zeroed after liquidation!")
		} else {
			logs.Infof("[Mock-Sync Liquidation] Positions successfully zeroed.")
		}

		logs.Debugf("[Mock-Sync Liquidation] === Synchronous liquidation process ended, waiting for monitor panic... ===")
		return order, nil // Return the final executed liquidation order
	}

	// --- Regular asynchronous order path ---
	// Generate ID and add to openOrders for all orders (including liquidation orders)
	newOrder := *order
	newOrder.OrderID = c.generateID_noLock()
	newOrder.Status = New

	// Only put orders into openOrders if there is an actual quantity
	if qty, _ := strconv.ParseFloat(newOrder.OrigQty, 64); qty > 0 {
		c.openOrders[strconv.FormatInt(newOrder.OrderID, 10)] = &newOrder
	}

	// Log pending orders
	logs.Debugf("[Mock] Pending order: %s %s %s, Qty: %s", newOrder.Side, newOrder.Type, newOrder.Symbol, newOrder.OrigQty)
	if newOrder.Type == Limit {
		logs.Debugf("[Mock] --> Price: %s, OrderID: %d", newOrder.Price, newOrder.OrderID)
	} else {
		logs.Debugf("[Mock] --> Price: , OrderID: %d", newOrder.OrderID)
	}

	return &newOrder, nil
}

// GetOrder simulates getting order information.
func (c *MockClient) GetOrder(ctx context.Context, symbol, orderID string) (*Order, error) {
	c.mu.RLock()         // Use read lock
	defer c.mu.RUnlock() // Use read lock

	// First, search in active orders
	order, ok := c.openOrders[orderID]
	if ok && order.Symbol == symbol {
		return order, nil
	}

	// If not found in active orders, search in closed orders
	order, ok = c.closedOrders[orderID]
	if ok && order.Symbol == symbol {
		return order, nil
	}

	return nil, fmt.Errorf("Mock order %s (trading pair %s) not found", orderID, symbol)
}

// CancelOrder simulates cancelling an order.
func (c *MockClient) CancelOrder(ctx context.Context, symbol, orderID string) (*Order, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	order, ok := c.openOrders[orderID]
	if !ok || order.Symbol != symbol {
		return nil, fmt.Errorf("Mock order %s (trading pair %s) not found", orderID, symbol)
	}

	order.Status = Canceled
	delete(c.openOrders, orderID)
	c.closedOrders[orderID] = order

	if c.onOrderUpdate != nil {
		// Deadlock fix: Asynchronously call the callback in a separate goroutine to simulate real exchange push behavior
		orderIDStr := strconv.FormatInt(order.OrderID, 10)
		statusStr := string(Canceled)
		go c.onOrderUpdate(orderIDStr, statusStr, 0, 0)
	}

	return order, nil
}

// CancelAllOpenOrders simulates cancelling all open orders.
func (c *MockClient) CancelAllOpenOrders(ctx context.Context, symbol string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ordersToCancel := make(map[string]*Order)
	for id, order := range c.openOrders {
		if order.Symbol == symbol {
			ordersToCancel[id] = order
		}
	}

	// Execute all callbacks without locks
	if c.onOrderUpdate != nil {
		for id, order := range ordersToCancel {
			order.Status = Canceled
			delete(c.openOrders, id)
			c.closedOrders[id] = order

			// Deadlock fix: Asynchronously call the callback in a separate goroutine
			orderIDStr := strconv.FormatInt(order.OrderID, 10)
			statusStr := string(Canceled)
			go func(idStr, status string) {
				c.onOrderUpdate(idStr, status, 0, 0)
			}(orderIDStr, statusStr)
		}
	}

	return nil
}

// GetPrice simulates getting the latest price.
func (c *MockClient) GetPrice(symbol string) (float64, error) {
	c.mu.RLock()         // Use read lock
	defer c.mu.RUnlock() // Use read lock
	price, ok := c.currentPrice[symbol]
	if !ok {
		return 0, fmt.Errorf("Mock price not found for %s", symbol)
	}
	return price, nil
}

// GetPositionInfo simulates getting position information.
func (c *MockClient) GetPositionInfo(ctx context.Context, symbol, positionSide string) (*PositionInfo, error) {
	c.mu.RLock()         // Use read lock
	defer c.mu.RUnlock() // Use read lock

	pos := c.findPosition_noLock(symbol, positionSide)
	if pos == nil {
		// If real position not found, return an empty default position object that meets the interface requirements
		return &PositionInfo{
			Symbol:       symbol,
			PositionSide: strings.ToUpper(positionSide),
			PositionAmt:  0,
			EntryPrice:   0,
		}, nil
	}
	return pos, nil
}

// findPosition_noLock is an internal helper function that searches for positions without a lock.
// **Core fix**: Now, when the position does not exist or the quantity is 0, it returns nil, which is crucial for the simulator logic.
// The caller must hold the lock.
func (c *MockClient) findPosition_noLock(symbol, positionSide string) *PositionInfo {
	upperPositionSide := strings.ToUpper(positionSide)
	exactMatchKey := fmt.Sprintf("%s_%s", symbol, upperPositionSide)

	// 1. Search for exact match positions
	if pos, exists := c.positions[exactMatchKey]; exists && pos.PositionAmt != 0 {
		c.updateUnrealizedProfitAndNotional_noLock(pos)
		return pos
	}

	// 2. If no positions or positions are 0, return nil
	return nil
}

func (c *MockClient) updateUnrealizedProfitAndNotional_noLock(pos *PositionInfo) {
	if pos == nil || pos.PositionAmt == 0 {
		if pos != nil {
			pos.UnrealizedProfit = 0
			pos.Notional = 0
		}
		return
	}
	currentPrice := c.currentPrice[pos.Symbol]
	pos.UnrealizedProfit = (currentPrice - pos.EntryPrice) * pos.PositionAmt
	pos.Notional = math.Abs(pos.PositionAmt * currentPrice)
}

// runPriceSimulator is the main loop of the mock client, responsible for periodically updating prices and matching orders.
func (c *MockClient) runPriceSimulator() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.mu.Lock() // Get write lock

			// --- Get state before changing price ---
			var prevHedgeAmt float64 = 0
			var hedgeSide string
			if c.stressTestPositionSide == "long" {
				hedgeSide = "SHORT"
			} else {
				hedgeSide = "LONG"
			}
			hedgePosBefore := c.findPosition_noLock("XRPUSDT", hedgeSide)
			if hedgePosBefore != nil {
				prevHedgeAmt = hedgePosBefore.PositionAmt
			}

			// **Defect four fix**: Update price first, then match with new price.
			// This ensures that in a single heartbeat, price, matching, and risk control see the same world state.
			switch c.simulationMode {
			case "bhls_stress_test":
				// --- Core fix: Implement a real, intelligent stress test price state machine ---
				if c.stressTestPositionSide == "short" { // Test short strategy
					if c.simulationSubMode == "attacking" {
						c.currentPrice["XRPUSDT"] += c.priceIncrement // Continuously pull up price to open hedge
					} else { // "recovering_hedge"
						c.currentPrice["XRPUSDT"] -= c.priceIncrement // Callback price to trigger stop loss
					}
				} else { // Test long strategy
					if c.simulationSubMode == "attacking" {
						c.currentPrice["XRPUSDT"] -= c.priceIncrement // Continuously lower price to open hedge
					} else { // "recovering_hedge"
						c.currentPrice["XRPUSDT"] += c.priceIncrement // Callback price to trigger stop loss
					}
				}
				c.broadcastPriceUpdate_noLock(c.currentPrice["XRPUSDT"])

			case "sine":
				c.runSimplePriceUpdate_noLock()
			}

			// --- 2. Check and match orders (refactored to fix "order duplicate matching" bug) ---
			var filledOrders []*Order
			ordersToMove := make(map[string]*Order)

			for id, order := range c.openOrders {
				currentPrice, ok := c.currentPrice[order.Symbol]
				if !ok {
					continue
				}

				isFilled := false
				if order.Type == Market {
					// Market orders fill immediately
					isFilled = true
					order.Price = strconv.FormatFloat(currentPrice, 'f', -1, 64) // Use current market price as fill price
					logs.Infof("[Mock] Market order filled: %s %s at %.4f, Qty: %s, OrderID: %d", order.Side, order.Symbol, currentPrice, order.OrigQty, order.OrderID)
				} else { // Limit orders
					orderPrice, _ := strconv.ParseFloat(order.Price, 64)
					if (order.Side == Buy && currentPrice <= orderPrice) || (order.Side == Sell && currentPrice >= orderPrice) {
						isFilled = true
						logs.Infof("[Mock] Limit order filled: %s %s at %.4f, Qty: %s, OrderID: %d", order.Side, order.Symbol, orderPrice, order.OrigQty, order.OrderID)
					}
				}

				if isFilled {
					order.Status = Filled
					if order.Type == Market {
						order.AvgPrice = order.Price
					} else {
						// For limit orders, the fill price in simulation can be more realistic using current price
						order.AvgPrice = strconv.FormatFloat(currentPrice, 'f', 4, 64)
					}
					order.ExecutedQty = order.OrigQty // Assume all filled
					filledOrders = append(filledOrders, order)
					ordersToMove[id] = order
				}
			}

			// Move filled orders from openOrders to closedOrders
			for id, order := range ordersToMove {
				delete(c.openOrders, id)
				c.closedOrders[id] = order
			}

			// Update position information (still within the lock, ensuring data consistency)
			for _, order := range filledOrders {
				c.updatePosition_noLock(order)
			}

			// --- 3. Get state after price change, and update the state machine for the next round ---
			var currentHedgeAmt float64 = 0
			hedgePosAfter := c.findPosition_noLock("XRPUSDT", hedgeSide)
			if hedgePosAfter != nil {
				currentHedgeAmt = hedgePosAfter.PositionAmt
			}

			if math.Abs(currentHedgeAmt) > math.Abs(prevHedgeAmt) {
				// Hedge position increased, indicating a successful hedge order was just opened. Switch to recovery mode, prepare to trigger its stop loss.
				c.simulationSubMode = "recovering_hedge"
			} else if math.Abs(currentHedgeAmt) < math.Abs(prevHedgeAmt) {
				// Hedge position decreased, indicating a successful stop loss was just triggered. Switch to attack mode, to open the next layer of hedge.
				c.simulationSubMode = "attacking"
			}
			// If position remains the same, keep the current mode.

			// --- 4. Execute callbacks outside the lock ---
			c.mu.Unlock() // Key: After updating all internal states, but before executing any callbacks, fully release the lock

			if c.onOrderUpdate != nil {
				for _, order := range filledOrders {
					// Deadlock fix: Asynchronously call the callback in a separate goroutine
					orderIDStr := strconv.FormatInt(order.OrderID, 10)
					statusStr := string(order.Status)
					filledPrice, _ := strconv.ParseFloat(order.AvgPrice, 64)
					filledQty, _ := strconv.ParseFloat(order.ExecutedQty, 64)

					// Use closure to ensure correct variable values are passed
					go func(id, status string, price, qty float64) {
						c.onOrderUpdate(id, status, price, qty)
					}(orderIDStr, statusStr, filledPrice, filledQty)
				}
			}
		}
	}
}

// runSimplePriceUpdate_noLock simulates a simple sine wave price trend. Must be called while holding the lock.
func (c *MockClient) runSimplePriceUpdate_noLock() {
	c.simulationTime += 0.1
	priceOffset := c.simAmplitude * math.Sin(c.simulationTime)
	newPrice := c.simInitialPrice + priceOffset
	c.currentPrice["XRPUSDT"] = newPrice
	c.broadcastPriceUpdate_noLock(newPrice)
}

// updatePosition_noLock is an internal helper function used to update positions when matching orders.
// It does not hold the lock, so it must be called with the lock held externally.
func (c *MockClient) updatePosition_noLock(order *Order) {
	// Fix: Add back the variable definitions that were mistakenly removed
	qty, _ := strconv.ParseFloat(order.OrigQty, 64)
	price, _ := strconv.ParseFloat(order.AvgPrice, 64) // Use AvgPrice as fill price

	positionSide := order.PositionSide
	if positionSide == "" {
		logs.Errorf("Order %d has no PositionSide, cannot update position", order.OrderID)
		return
	}

	// Final Fix: Handle the "BOTH" PositionSide for liquidation orders.
	// Translate "BOTH" into the actual side that needs to be reduced.
	if positionSide == "BOTH" {
		if order.Side == Sell {
			positionSide = "LONG" // A sell order closes a long position
		} else { // order.Side == Buy
			positionSide = "SHORT" // A buy order closes a short position
		}
		logs.Debugf("[Mock-Position] Liquidation order (Side: %s) detected, target position corrected to: %s", order.Side, positionSide)
	}

	pos := c.findPosition_noLock(order.Symbol, string(positionSide))
	if pos == nil {
		// Create a new position
		pos = &PositionInfo{
			Symbol:       order.Symbol,
			PositionSide: string(positionSide),
			PositionAmt:  0,
			EntryPrice:   0,
		}
		c.positions[fmt.Sprintf("%s_%s", order.Symbol, string(positionSide))] = pos
	}

	// Define the effect of the trading pair position
	var positionEffect float64
	if order.Side == Buy {
		positionEffect = qty
	} else { // Sell
		positionEffect = -qty
	}

	// Get current position status
	currentAmt := pos.PositionAmt
	currentAvgPrice := pos.EntryPrice
	newAmt := currentAmt + positionEffect

	// Determine if it's a new position or a closing position
	// (absolute value of newAmt > absolute value of currentAmt) -> New position
	// (absolute value of newAmt < absolute value of currentAmt) -> Closing position
	// (newAmt and currentAmt have opposite signs) -> Reverse new position
	if (math.Abs(newAmt) > math.Abs(currentAmt)) || (newAmt*currentAmt < 0) {
		// --- New position/Reverse new position logic ---
		// Use weighted average to calculate new average position price
		var currentValue float64
		// If it's a reverse new position, the value of the previous position should be considered 0
		if newAmt*currentAmt < 0 {
			currentValue = 0
		} else {
			currentValue = currentAmt * currentAvgPrice
		}
		tradeValue := positionEffect * price
		if newAmt != 0 {
			pos.EntryPrice = (currentValue + tradeValue) / newAmt
		} else {
			pos.EntryPrice = 0
		}
		pos.PositionAmt = newAmt
	} else {
		// --- Closing position logic ---
		pos.PositionAmt = newAmt
		// If the position is completely closed, reset the average price
		if newAmt == 0 {
			pos.EntryPrice = 0
		}
		// If it's only partially closed, the average price remains unchanged
	}

	// Key fix: Ensure findPosition_noLock always updates unrealized profit and notional value before returning
	c.updateUnrealizedProfitAndNotional_noLock(pos)

	logs.Debugf("[Mock] Order filled: %s %s %s on %s side, Qty: %.4f, Price: %.4f",
		order.Side, order.Type, order.Symbol, order.PositionSide, qty, price)

	// For clearer logging, we always try to get both types of positions
	// **Deadlock fix**: Must call internal findPosition_noLock instead of GetPositionInfo which locks
	longPos := c.findPosition_noLock("XRPUSDT", "LONG")
	shortPos := c.findPosition_noLock("XRPUSDT", "SHORT")

	var longAmt, longPrice, shortAmt, shortPrice float64
	if longPos != nil {
		longAmt = longPos.PositionAmt
		longPrice = longPos.EntryPrice
	}
	if shortPos != nil {
		shortAmt = shortPos.PositionAmt
		shortPrice = shortPos.EntryPrice
	}

	logs.Debugf("[Mock] --> Current positions LONG: %.4f, avg: %.4f | SHORT: %.4f, avg: %.4f",
		longAmt, longPrice, shortAmt, shortPrice)

	logs.Debugf("Order internal state update: %s (%s) %d %.4f %s -> %s",
		order.PositionSide, order.Side, order.OrderID, price, "NEW", "FILLED")
}

// --- Helper functions for testing ---

// generateID_noLock generates a unique order ID.
func (c *MockClient) generateID_noLock() int64 {
	c.nextOrderID++
	return c.nextOrderID
}

// SetPrice sets the current price for a trading pair for testing.
func (c *MockClient) SetPrice(symbol string, price float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentPrice[symbol] = price
}

// SetInitialPosition sets the initial position for testing.
func (c *MockClient) SetInitialPosition(symbol, positionSide string, amount float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s_%s", symbol, strings.ToUpper(positionSide))
	c.positions[key] = &PositionInfo{
		Symbol:       symbol,
		PositionSide: positionSide,
		PositionAmt:  amount,
	}
	logs.Debugf("[Mock] Setting initial position for %s %s: %.4f", symbol, positionSide, amount)
}

// GetOpenOrders returns all open orders.
func (c *MockClient) GetOpenOrders() ([]Order, error) {
	c.mu.RLock()         // Use read lock
	defer c.mu.RUnlock() // Use read lock
	var orders []Order
	for _, order := range c.openOrders {
		if order.Status == New {
			orders = append(orders, *order)
		}
	}
	return orders, nil
}

// broadcastPriceUpdate_noLock broadcasts price updates to all subscribers. Must be called while holding the lock.
func (c *MockClient) broadcastPriceUpdate_noLock(price float64) {
	for sub := range c.subscribers {
		select {
		case sub <- price:
		default:
			// Don't block if the subscriber is not ready
		}
	}
}

// getCurrentPositions is an internal helper to get a snapshot of current positions.
// It does not acquire a lock.
func (c *MockClient) getCurrentPositions() (longPosAmt, longEntryPrice, shortPosAmt, shortEntryPrice float64) {
	longPos := c.findPosition_noLock("XRPUSDT", "LONG")
	shortPos := c.findPosition_noLock("XRPUSDT", "SHORT")
	if longPos != nil {
		longPosAmt = longPos.PositionAmt
		longEntryPrice = longPos.EntryPrice
	}
	if shortPos != nil {
		shortPosAmt = shortPos.PositionAmt
		shortEntryPrice = shortPos.EntryPrice
	}
	return
}

// forceSettlePendingMarketOrders_noLock_sync is a synchronous, internal-only function
// to force-fill any in-flight market orders. This is critical for the final liquidation
// to correctly calculate the net position after all individual hedge stop-losses have been cleared.
// It does NOT send order updates back, as the system is already in a terminal state.
func (c *MockClient) forceSettlePendingMarketOrders_noLock_sync() {
	var ordersToForceFill []*Order
	newOpenOrders := make(map[string]*Order)

	for id, o := range c.openOrders {
		if o.Type == Market && o.ReduceOnly {
			ordersToForceFill = append(ordersToForceFill, o)
		} else {
			newOpenOrders[id] = o
		}
	}

	if len(ordersToForceFill) == 0 {
		return
	}

	logs.Debugf("[Mock-Sync Liquidation] Detected %d concurrent market closing orders, forcing priority execution...", len(ordersToForceFill))

	// Synchronously fill these orders
	for _, orderToFill := range ordersToForceFill {
		// We simulate a fill price with some slippage.
		slippage := 0.005 // 0.5% slippage
		priceDirection := 1.0
		if c.stressTestPositionSide == "long" { // price is dropping
			priceDirection = -1.0
		}
		fillPrice := c.currentPrice[orderToFill.Symbol] * (1 + priceDirection*slippage)

		orderToFill.Status = Filled
		orderToFill.Price = fmt.Sprintf("%.4f", fillPrice) // Market orders get a fill price
		orderToFill.AvgPrice = orderToFill.Price
		orderToFill.ExecutedQty = orderToFill.OrigQty

		// Reuse the existing position update logic
		c.updatePosition_noLock(orderToFill)

		// Move to closed orders
		orderIDStr := strconv.FormatInt(orderToFill.OrderID, 10)
		c.closedOrders[orderIDStr] = orderToFill

		logs.Infof("[Mock-Force Liquidation] Synchronous order filled: %s %s at %.4f, Qty: %s, OrderID: %d", orderToFill.Side, orderToFill.Symbol, fillPrice, orderToFill.OrigQty, orderToFill.OrderID)
	}

	c.openOrders = newOpenOrders
	logs.Debugf("[Mock-Sync Liquidation] All pending market orders processed.")
}

func (c *MockClient) Liquidate(symbol string) error {
	// This function is a stub in the mock client.
	// The actual liquidation logic is triggered by a special market order
	// sent to the PlaceOrder function to simulate the real-world scenario.
	logs.Debugf("[Mock] Liquidate function called, but in current simulation it's a stub. Actual liquidation is triggered by special orders in PlaceOrder.")
	return nil
}
