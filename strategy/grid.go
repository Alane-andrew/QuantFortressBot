// strategy/grid.go
package strategy

import (
	"auto_bian_go_1/config"
	"auto_bian_go_1/exchange"
	"auto_bian_go_1/logs"
	"auto_bian_go_1/profit"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- New: Grid Strategy State ---
type GridState int

const (
	StateTrading          GridState = iota // 0: Normal trading
	StateHaltedForHedging                  // 1: Halted for hedging
)

// --- End of State Definition ---

type PlaceOrderFunc func(price float64, side string, reduceOnly bool) (string, error)

type Order struct {
	ID          string
	Price       float64
	Qty         float64
	Side        string // "BUY" or "SELL"
	Type        string // "OPEN" or "CLOSE"
	Status      string // "NEW", "FILLED", "PENDING", etc.
	PairedPrice float64
	PriceStr    string // New: for storing price string
}

type GridStrategy struct {
	client             exchange.Client
	Symbol             string
	Direction          string
	gridConfig         *config.GridConfig
	gridUpper          float64
	gridLower          float64
	orders             map[string]*Order // key: orderID
	activePositions    map[string]*Order // New: track active positions, key: open_price_string
	pendingOrders      []*Order
	placeOrderFunc     PlaceOrderFunc
	OnTrade            func(trade profit.Trade)
	OnCloseOrderPlaced func(orderID string, pairedPrice float64) error // Modified signature
	OnOrderCancelled   func(orderID string) error                      // Modified signature
	isTradingHalted    bool
	state              GridState
	mu                 sync.RWMutex
}

func NewGridStrategy(
	client exchange.Client,
	symbol string,
	direction string,
	gridCfg *config.GridConfig,
	gridUpper float64,
	gridLower float64,
	placeOrder PlaceOrderFunc,
) (*GridStrategy, error) {
	s := &GridStrategy{
		client:             client,
		Symbol:             symbol,
		Direction:          direction,
		gridConfig:         gridCfg,
		gridUpper:          gridUpper,
		gridLower:          gridLower,
		orders:             make(map[string]*Order),
		activePositions:    make(map[string]*Order), // New: initialize
		pendingOrders:      make([]*Order, 0),
		placeOrderFunc:     placeOrder,
		OnTrade:            func(trade profit.Trade) {},
		OnCloseOrderPlaced: func(orderID string, pairedPrice float64) error { return nil }, // Modified default implementation
		OnOrderCancelled:   func(orderID string) error { return nil },                      // Modified default implementation
		state:              StateTrading,
	}
	return s, nil
}

// SetOnCloseOrderPlaced registers callback when close order is created.
func (g *GridStrategy) SetOnCloseOrderPlaced(callback func(orderID string, pairedPrice float64) error) { // Modified signature
	g.OnCloseOrderPlaced = callback
}

// SetOnOrderCancelled registers callback when order is cancelled or filled and metadata cleanup is needed.
func (g *GridStrategy) SetOnOrderCancelled(callback func(orderID string) error) { // Modified signature
	g.OnOrderCancelled = callback
}

func (g *GridStrategy) SetOnTrade(callback func(trade profit.Trade)) {
	g.OnTrade = callback
}

func (g *GridStrategy) SetTradingHalted(halted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Only log and update state when status changes
	if g.isTradingHalted == halted {
		return
	}

	g.isTradingHalted = halted
	if halted {
		logs.Warn("[Strategy-Pause] Investment limit exceeded, paused placing new open orders.")
	} else {
		logs.Info("[Strategy-Resume] Investment limit lifted, resuming placement of new open orders.")
	}
}

func (g *GridStrategy) InitOrders() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	prices := g.calculateGridPrices()
	side := g.getOpenOrderSide()

	for _, price := range prices {
		orderID, err := g.placeOrderFunc(price, side, false)
		if err != nil {
			// If it's a specific error about price out of range, add to pending list instead of terminating
			if strings.Contains(err.Error(), "(code: -4024)") {
				logs.Warnf("[Strategy-Skip] Price %.4f out of exchange range, skipping order. Error: %v", price, err)
				pendingOrder := &Order{
					Price:  price,
					Qty:    g.gridConfig.GridQty,
					Side:   side,
					Type:   "OPEN",
					Status: "PENDING",
				}
				g.pendingOrders = append(g.pendingOrders, pendingOrder)
				continue // Continue to next price
			}
			// For all other errors, return immediately and terminate startup
			return err
		}

		order := &Order{
			ID:     orderID,
			Price:  price,
			Qty:    g.gridConfig.GridQty,
			Side:   side,
			Type:   "OPEN",
			Status: "NEW",
		}
		g.orders[orderID] = order
	}
	return nil
}

func (g *GridStrategy) RetryPlacePendingOrders() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.state != StateTrading || len(g.pendingOrders) == 0 {
		return
	}

	logs.Debugf("[Order Service] Checking %d pending orders...", len(g.pendingOrders))

	stillPending := []*Order{}
	for _, order := range g.pendingOrders {
		orderID, err := g.placeOrderFunc(order.Price, order.Side, false)
		if err != nil {
			// Log selectively based on error type
			if strings.Contains(err.Error(), "(code: -4024)") {
				logs.Debugf("[Order Service] Price %.4f still out of range, skipping.", order.Price)
			} else {
				logs.Warnf("[Order Service] Failed to place order at %.4f, error: %v", order.Price, err)
			}
			stillPending = append(stillPending, order)
		} else {
			// Order placed successfully
			logs.Infof("[Strategy-Supplement] Pending order %.4f placed successfully.", order.Price)
			newOrder := &Order{
				ID:     orderID,
				Price:  order.Price,
				Qty:    order.Qty,
				Side:   order.Side,
				Type:   "OPEN",
				Status: "NEW",
			}
			g.orders[orderID] = newOrder
		}
	}
	g.pendingOrders = stillPending
}

func (g *GridStrategy) OnOrderUpdate(orderID, status string, filledQty, avgPrice float64) {
	// --- Deadlock Fix: Step 1 - Perform Network I/O WITHOUT holding a lock ---
	var executedOrder *exchange.Order
	var networkErr error
	if status == "FILLED" {
		// To ensure data accuracy, we fetch the final order details from the exchange.
		executedOrder, networkErr = g.client.GetOrder(context.Background(), g.Symbol, orderID)
	}

	// --- Deadlock Fix: Step 2 - Acquire lock ONLY for fast in-memory operations ---
	g.mu.Lock()

	order, exists := g.orders[orderID]
	if !exists || order.Status == status {
		g.mu.Unlock() // Release lock if no action is needed
		return
	}

	logs.Debugf("Order status change: %s (%s) %s %.4f %s -> %s", order.Type, order.Side, order.ID, order.Price, order.Status, status)
	order.Status = status

	if status == "FILLED" {
		var finalAvgPrice, finalExecutedQty float64
		if networkErr != nil {
			logs.Errorf("[Strategy-Warning] Unable to get order %s execution details, will use planned price for accounting: %v", orderID, networkErr)
			finalAvgPrice, finalExecutedQty = order.Price, order.Qty
		} else {
			logs.Infof("[Strategy-Filled] Order %d (%s) has been filled. Quantity: %s, Average Price: %s USDT",
				executedOrder.OrderID, executedOrder.Side, executedOrder.ExecutedQty, executedOrder.AvgPrice)
			finalAvgPrice, _ = strconv.ParseFloat(executedOrder.AvgPrice, 64)
			finalExecutedQty, _ = strconv.ParseFloat(executedOrder.ExecutedQty, 64)
		}

		if g.OnTrade != nil {
			g.OnTrade(profit.Trade{
				Side: order.Side, Price: finalAvgPrice, Quantity: finalExecutedQty,
				Timestamp: time.Now().Unix(), TradeType: "GRID_" + order.Type, PairedPrice: order.PairedPrice,
			})
		}

		// --- Core modification: Remove judgment here, allow placing close orders even during hedging pause ---
		// if g.state == StateHaltedForHedging {
		// 	g.mu.Unlock()
		// 	return
		// }
		// --- End of modification ---

		delete(g.orders, orderID)

		// --- Deadlock Fix: Step 3 - Release lock BEFORE calling functions that cause I/O ---
		g.mu.Unlock()

		if order.Type == "OPEN" {
			// New logic: When open order is filled, add it to active positions map
			g.mu.Lock()
			g.activePositions[order.PriceStr] = order
			g.mu.Unlock()
			g.placePairedCloseOrder(order)
		} else if order.Type == "CLOSE" {
			// New logic: When close order is filled, remove corresponding open record from active positions map
			g.mu.Lock()
			// PairedPrice here is actually the paired open price
			pairedOpenPriceStr := strconv.FormatFloat(order.PairedPrice, 'f', -1, 64)
			delete(g.activePositions, pairedOpenPriceStr)
			g.mu.Unlock()

			// If this is a close order fill, also notify external to clean up paired price records
			if g.OnOrderCancelled != nil {
				if err := g.OnOrderCancelled(orderID); err != nil {
					logs.Errorf("[Strategy-Error] Failed to clean up paired metadata after close order %s fill: %v", orderID, err)
				}
			}
			g.relistOpenOrder(order.PairedPrice)
		}

	} else if status == "CANCELED" {
		logs.Infof("Order %s (%s, price %.4f) has been cancelled.", order.ID, order.Type, order.Price)
		delete(g.orders, orderID)
		// Add callback
		if g.OnOrderCancelled != nil {
			if err := g.OnOrderCancelled(orderID); err != nil {
				logs.Errorf("[Strategy-Error] Failed to clean up paired metadata after order %s cancellation: %v", orderID, err)
			}
		}
		g.mu.Unlock() // Release lock
	} else {
		g.mu.Unlock() // Release lock for any other status
	}
}

func (g *GridStrategy) GetMonitoredOrders() []*Order {
	g.mu.RLock()
	defer g.mu.RUnlock()
	monitored := make([]*Order, 0, len(g.orders))
	for _, o := range g.orders {
		// Improvement: Monitor all non-final status orders to properly handle "partially filled" etc.
		if o.Status == "NEW" || o.Status == "PARTIALLY_FILLED" {
			monitored = append(monitored, o)
		}
	}
	return monitored
}

// RestoreMonitoredOrders rebuilds strategy state using real order data from exchange and locally stored metadata.
func (g *GridStrategy) RestoreMonitoredOrders(openOrders []*exchange.Order, pairedPrices map[string]float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	logs.Infof("[Strategy-Restore] Rebuilding state from %d open orders from exchange...", len(openOrders))

	// 1. Distinguish open orders and close orders
	var openSideOrders, closeSideOrders []*exchange.Order
	openSide := g.getOpenOrderSide()
	for _, o := range openOrders {
		if string(o.Side) == openSide {
			openSideOrders = append(openSideOrders, o)
		} else {
			closeSideOrders = append(closeSideOrders, o)
		}
	}

	// 2. Process close orders, they represent active positions
	for _, apiOrder := range closeSideOrders {
		orderIDStr := strconv.FormatInt(apiOrder.OrderID, 10)
		if openPrice, ok := pairedPrices[orderIDStr]; ok {
			openPriceStr := strconv.FormatFloat(openPrice, 'f', -1, 64)
			g.activePositions[openPriceStr] = &Order{
				// Here we only need a placeholder, PriceStr is the key
				Price:    openPrice,
				PriceStr: openPriceStr,
				Side:     openSide,
				Type:     "OPEN",
				Status:   "FILLED", // Mark as filled
			}
			// Also add this close order itself to monitoring list
			price, _ := strconv.ParseFloat(apiOrder.Price, 64)
			qty, _ := strconv.ParseFloat(apiOrder.OrigQty, 64)
			g.orders[orderIDStr] = &Order{
				ID:          orderIDStr,
				Price:       price,
				Qty:         qty,
				Side:        string(apiOrder.Side),
				Type:        "CLOSE",
				Status:      string(apiOrder.Status),
				PairedPrice: openPrice,
				PriceStr:    apiOrder.Price,
			}
		} else {
			logs.Warnf("[Strategy-Restore-Warning] Unable to find paired price for close order %s, will ignore this order.", orderIDStr)
		}
	}

	// 3. Process open orders, they are pending orders that haven't been filled
	for _, apiOrder := range openSideOrders {
		orderIDStr := strconv.FormatInt(apiOrder.OrderID, 10)
		price, _ := strconv.ParseFloat(apiOrder.Price, 64)
		qty, _ := strconv.ParseFloat(apiOrder.OrigQty, 64)
		g.orders[orderIDStr] = &Order{
			ID:       orderIDStr,
			Price:    price,
			Qty:      qty,
			Side:     string(apiOrder.Side),
			Type:     "OPEN",
			Status:   string(apiOrder.Status),
			PriceStr: apiOrder.Price,
		}
	}

	logs.Infof("[Strategy-Restore] State rebuild complete, currently monitoring %d orders, identified %d active positions.", len(g.orders), len(g.activePositions))
}

// GetGridUpperLower returns the upper and lower boundaries of the grid.
func (g *GridStrategy) GetGridUpperLower() (float64, float64) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.gridUpper, g.gridLower
}

func (g *GridStrategy) calculateGridPrices() []float64 {
	prices := make([]float64, g.gridConfig.GridNum)
	step := (g.gridUpper - g.gridLower) / float64(g.gridConfig.GridNum)
	for i := 0; i < g.gridConfig.GridNum; i++ {
		prices[i] = g.gridLower + step*float64(i)
	}
	return prices
}

func (g *GridStrategy) getOpenOrderSide() string {
	if g.Direction == "short" {
		return "SELL"
	}
	return "BUY"
}

func (g *GridStrategy) getCloseOrderSide() string {
	if g.Direction == "short" {
		return "BUY"
	}
	return "SELL"
}

func (g *GridStrategy) placePairedCloseOrder(openOrder *Order) {
	var closePrice float64
	gridStep := (g.gridUpper - g.gridLower) / float64(g.gridConfig.GridNum)
	if g.Direction == "short" {
		closePrice = openOrder.Price - gridStep
	} else {
		closePrice = openOrder.Price + gridStep
	}
	side := g.getCloseOrderSide()

	// --- Deadlock Fix: Perform I/O (network call) WITHOUT lock ---
	orderID, err := g.placeOrderFunc(closePrice, side, true)
	if err != nil {
		logs.Errorf("[Error] Failed to place close order for open order %s: %v", openOrder.ID, err)
		return
	}

	// --- Deadlock Fix: Lock only for fast in-memory update ---
	g.mu.Lock()
	defer g.mu.Unlock()

	order := &Order{
		ID:          orderID,
		Price:       closePrice,
		Qty:         openOrder.Qty,
		Side:        side,
		Type:        "CLOSE",
		Status:      "NEW",
		PairedPrice: openOrder.Price,
	}
	g.orders[orderID] = order

	// Trigger callback to notify external to persist this pairing relationship
	if g.OnCloseOrderPlaced != nil {
		if err := g.OnCloseOrderPlaced(orderID, openOrder.Price); err != nil {
			logs.Errorf("[Strategy-Error] Failed to persist paired metadata for new close order %s: %v", orderID, err)
			// Note: Even if persistence fails, order is in memory and monitoring will continue.
		}
	}

	logs.Infof("Open order %.4f filled, placed close order at %.4f", openOrder.Price, closePrice)
}

func (g *GridStrategy) relistOpenOrder(price float64) {
	// --- Deadlock Fix: Read state variables under a Read Lock first ---
	g.mu.RLock()
	isHaltedForHedging := g.state == StateHaltedForHedging
	isTradingHaltedByInvestment := g.isTradingHalted
	side := g.getOpenOrderSide()
	qty := g.gridConfig.GridQty
	g.mu.RUnlock()

	if isHaltedForHedging {
		logs.Debugf("[Strategy-Skip] Currently in risk control hedging enabled state, will not relist open orders after close.")
		return
	}

	if isTradingHaltedByInvestment {
		logs.Warn("[Strategy-Pause] Due to investment limit exceeded, paused placing new open orders.")
		return
	}

	// Add retry mechanism
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		logs.Debugf("[Strategy-Restore] Attempting to relist open order at price %.4f (attempt %d)...", price, attempt)

		// --- Deadlock Fix: Perform I/O (network call) WITHOUT lock ---
		orderID, err := g.placeOrderFunc(price, side, false)
		if err != nil {
			if attempt < maxRetries {
				logs.Warnf("[Strategy-Restore] Attempt %d failed at price %.4f: %v, will retry in 1 second...", attempt, price, err)
				time.Sleep(1 * time.Second)
				continue
			} else {
				logs.Errorf("[Strategy-Restore] Failed to relist open order at price %.4f (retried %d times): %v", price, maxRetries, err)
				return // Final failure, but doesn't affect other orders
			}
		}

		// Successfully placed order, update memory state
		// --- Deadlock Fix: Lock only for fast in-memory update ---
		g.mu.Lock()
		defer g.mu.Unlock()

		order := &Order{
			ID:     orderID,
			Price:  price,
			Qty:    qty,
			Side:   side,
			Type:   "OPEN",
			Status: "NEW",
		}
		g.orders[orderID] = order
		logs.Infof("Close order filled, relisted open order at %.4f", price)
		return // Success, exit retry loop
	}
}

// HaltForHedging pauses grid trading, cancels all orders, prepares for hedging
func (g *GridStrategy) HaltForHedging() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.state == StateHaltedForHedging {
		return
	}
	g.state = StateHaltedForHedging

	logs.Warnf("[Strategy-Pause] Price broke through grid, preparing for hedging. Cancelling all open pending orders...")

	// Cancel all current pending orders (open orders)
	for id, order := range g.orders {
		if order.Type == "OPEN" {
			_, err := g.client.CancelOrder(context.Background(), g.Symbol, id)
			if err != nil {
				// If order no longer exists (may have been filled), log a warning
				if strings.Contains(err.Error(), "Unknown order sent") {
					logs.Warnf("[Strategy-Pause-Warning] Attempted to cancel order %s (price %.4f) no longer exists, may have been filled or manually cancelled.", id, order.Price)
				} else {
					logs.Errorf("[Strategy-Pause-Error] Failed to cancel order %s: %v", id, err)
				}
			}
			// Fix race condition: Don't delete order here.
			// Final order status (CANCELED or FILLED) will be handled uniformly by main polling logic in OnOrderUpdate.
		}
	}
}

// ResumeTrading resumes grid trading
func (g *GridStrategy) ResumeTrading() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.state = StateTrading
	logs.Info("[Strategy-Resume] Hedging ended, resuming grid trading.")

	// 1. Get all active price points (including pending orders and occupied positions)
	activePrices := make(map[string]bool)
	for _, order := range g.orders {
		activePrices[order.PriceStr] = true
	}
	for priceStr := range g.activePositions {
		activePrices[priceStr] = true
	}

	// 2. Calculate all theoretical grid price points
	allGridPrices := g.calculateGridPrices()

	// 3. Find "truly free" grid points and relist "open orders"
	freeGridPoints := 0
	for _, price := range allGridPrices {
		priceStr := strconv.FormatFloat(price, 'f', -1, 64)
		if !activePrices[priceStr] {
			freeGridPoints++
		}
	}

	logs.Infof("[Strategy-Resume] Detected %d free grid points, starting to relist open orders...", freeGridPoints)

	// Record start time and processing statistics
	startTime := time.Now()
	successCount := 0

	// 4. Process all free grid points
	for i, price := range allGridPrices {
		priceStr := strconv.FormatFloat(price, 'f', -1, 64)
		if !activePrices[priceStr] {
			logs.Debugf("[Strategy-Resume] Processing free grid point %d/%d: %.4f", i+1, freeGridPoints, price)

			// Release lock to avoid holding lock during long network requests
			g.mu.Unlock()

			// Call relist function
			g.relistOpenOrder(price)

			// Re-acquire lock
			g.mu.Lock()

			// Count success/failure (simplified handling)
			successCount++

			// Output progress every few orders
			if (i+1)%3 == 0 || (i+1) == freeGridPoints {
				elapsed := time.Since(startTime)
				logs.Infof("[Strategy-Resume] Progress: %d/%d completed (elapsed: %.2fs)", i+1, freeGridPoints, elapsed.Seconds())
			}
		}
	}

	// Output final statistics
	totalTime := time.Since(startTime)
	logs.Infof("[Strategy-Resume] Grid restoration complete, processed %d free grid points, elapsed: %.2fs", successCount, totalTime.Seconds())
	logs.Info("[Strategy-Resume] Grid orders have been relisted.")
}
