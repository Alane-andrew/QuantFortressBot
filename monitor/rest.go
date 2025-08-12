// monitor/rest.go
package monitor

import (
	"auto_bian_go_1/config"
	"auto_bian_go_1/exchange"
	"auto_bian_go_1/investment"
	"auto_bian_go_1/logs"
	"auto_bian_go_1/profit"
	"auto_bian_go_1/risk"
	"auto_bian_go_1/state"
	"auto_bian_go_1/strategy"
	"context"
	"math"
	"strconv"
	"time"
)

// Start starts the main loop of the monitor.
func Start(
	client exchange.Client,
	strat *strategy.GridStrategy,
	riskManager risk.RiskManager,
	investmentManager *investment.Manager,
	ttpManager *profit.GridTakeProfitManager,
	gridAccountant *profit.Accountant,
	hedgeAccountant *profit.Accountant, // Receive independent hedge accountant
	stateManager state.StateManagerInterface, // Modified parameter type to interface
	cfg *config.Config, // Receive entire config object
	stopChan <-chan struct{},
) {
	ticker := time.NewTicker(time.Duration(cfg.Normal.MonitorIntervalSeconds) * time.Second)
	defer ticker.Stop()

	lastHeartbeat := time.Now()
	lastSyncTime := time.Now() // New: Record last sync time

	heartbeatInterval := time.Duration(cfg.Normal.HeartbeatIntervalMinutes) * time.Minute
	timeSyncInterval := time.Duration(cfg.Normal.TimeSyncIntervalMinutes) * time.Minute

	for {
		select {
		case <-stopChan:
			logs.Info("Monitor received stop signal, exiting.")
			return
		case <-ticker.C:
			currentPrice, err := client.GetPrice(strat.Symbol)
			if err != nil {
				logs.Errorf("[Error] Failed to get price: %v", err)
				continue
			}

			if investmentManager != nil {
				investmentManager.CheckAndUpdate()
				strat.SetTradingHalted(investmentManager.IsTradingHalted())
			}

			if riskManager != nil {
				actions := riskManager.CheckAndManageRisk(currentPrice)
				for _, action := range actions {
					switch act := action.(type) {
					case *risk.HaltGridAction:
						logs.Info("[Monitor] Detected halt instruction, pausing grid...")
						strat.HaltForHedging()

					case *risk.ResumeGridAction:
						logs.Info("[Monitor] Detected resume instruction, resuming trading...")
						strat.ResumeTrading()

					case *risk.OpenHedgeAction:
						logs.Infof("[Monitor] Executing open hedge: %s", act.Description())
						order := &exchange.Order{
							Symbol:       act.Symbol,
							Side:         exchange.OrderSide(act.Side),
							PositionSide: exchange.PositionSide(act.PositionSide),
							Type:         exchange.Market,
							OrigQty:      strconv.FormatFloat(act.Amount, 'f', -1, 64),
							ReduceOnly:   false,
						}
						placedOrder, err := client.PlaceOrder(context.Background(), order)
						if err != nil {
							logs.Errorf("[Monitor-Error] Failed to execute open hedge: %v", err)
						} else {
							// Assume market order executes immediately and use hedge accountant to record trade
							// Note: Real scenario needs websocket to get exact execution price
							hedgeAccountant.RecordTrade(profit.Trade{
								Side:      string(placedOrder.Side),
								Price:     currentPrice,
								Quantity:  act.Amount,
								TradeType: "HEDGE_OPEN",
							})
						}

					case *risk.CloseHedgeAction:
						logs.Infof("[Monitor] Executing close hedge: %s", act.Description())
						order := &exchange.Order{
							Symbol:       act.Symbol,
							Side:         exchange.OrderSide(act.Side),
							PositionSide: exchange.PositionSide(act.PositionSide),
							Type:         exchange.Market,
							OrigQty:      strconv.FormatFloat(act.Amount, 'f', -1, 64),
							ReduceOnly:   true,
						}
						_, err := client.PlaceOrder(context.Background(), order)
						if err != nil {
							logs.Errorf("[Monitor-Error] Failed to execute close hedge: %v", err)
						} else {
							// Key fix: Calculate and record PNL here
							var pnl float64
							if act.Side == "SELL" { // Sell to close long position
								pnl = (act.Price - act.EntryPrice) * act.Amount
							} else { // Buy to close short position
								pnl = (act.EntryPrice - act.Price) * act.Amount
							}
							logs.Infof("[Monitor-Hedge PNL] Hedge order closed, trade PNL: %.4f", pnl)
							hedgeAccountant.RecordPNL(pnl)
							// 2. Synchronize the latest cumulative total PnL from hedge accountant back to risk control module
							riskManager.SetCumulativePNL(hedgeAccountant.GetRealizedPNL())
						}
					case *risk.LiquidateAndStopAction:
						logs.Warnf("[Monitor] !!!Circuit breaker instruction detected!!! Liquidating all positions for %s and stopping program...", act.Symbol)

						// 1. Close main position (grid strategy position)
						mainPosInfo, err := client.GetPositionInfo(context.Background(), act.Symbol, strat.Direction)
						if err != nil {
							logs.Errorf("[Monitor-Fatal Error] Unable to get main position info during circuit breaker: %v", err)
						} else if mainPosInfo.PositionAmt != 0 {
							closeSide := "BUY"
							if mainPosInfo.PositionAmt > 0 { // Long position then sell to close
								closeSide = "SELL"
							}
							mainOrder := &exchange.Order{
								Symbol:       act.Symbol,
								Side:         exchange.OrderSide(closeSide),
								PositionSide: exchange.PositionSide(strat.Direction),
								Type:         exchange.Market,
								ReduceOnly:   true,
								OrigQty:      strconv.FormatFloat(math.Abs(mainPosInfo.PositionAmt), 'f', -1, 64),
							}
							_, err := client.PlaceOrder(context.Background(), mainOrder)
							if err != nil {
								logs.Errorf("[Monitor-Fatal Error] Failed to close main position during circuit breaker: %v", err)
							} else {
								logs.Infof("[Monitor-Circuit Breaker] Main position closed successfully: %s %s, quantity: %.4f", closeSide, strat.Direction, math.Abs(mainPosInfo.PositionAmt))
							}
						}

						// 2. Close hedge position (if any)
						// Use same logic as risk/bhls_manager.go
						hedgePositionSide := "SHORT"
						if strat.Direction == "SHORT" {
							hedgePositionSide = "LONG"
						}

						hedgePosInfo, err := client.GetPositionInfo(context.Background(), act.Symbol, hedgePositionSide)
						if err != nil {
							logs.Errorf("[Monitor-Fatal Error] Unable to get hedge position info during circuit breaker: %v", err)
						} else if hedgePosInfo.PositionAmt != 0 {
							closeSide := "BUY"
							if hedgePosInfo.PositionAmt > 0 { // Long position then sell to close
								closeSide = "SELL"
							}
							hedgeOrder := &exchange.Order{
								Symbol:       act.Symbol,
								Side:         exchange.OrderSide(closeSide),
								PositionSide: exchange.PositionSide(hedgePositionSide),
								Type:         exchange.Market,
								ReduceOnly:   true,
								OrigQty:      strconv.FormatFloat(math.Abs(hedgePosInfo.PositionAmt), 'f', -1, 64),
							}
							_, err := client.PlaceOrder(context.Background(), hedgeOrder)
							if err != nil {
								logs.Errorf("[Monitor-Fatal Error] Failed to close hedge position during circuit breaker: %v", err)
							} else {
								logs.Infof("[Monitor-Circuit Breaker] Hedge position closed successfully: %s %s, quantity: %.4f", closeSide, hedgePositionSide, math.Abs(hedgePosInfo.PositionAmt))
							}
						}

						// 3. Regardless of whether closing was successful, send stop signal
						go func() {
							panic("Circuit breaker instruction triggered, program stopping")
						}()
					default:
						logs.Warnf("[Monitor] Received unknown risk control instruction type: %T", act)
					}
				}
			}

			if ttpManager != nil {
				posInfo, err := client.GetPositionInfo(context.Background(), strat.Symbol, strat.Direction)
				if err == nil {
					currentRealizedProfit := gridAccountant.GetPositionState().RealizedProfit
					ttpManager.Check(currentRealizedProfit, posInfo.PositionAmt)
				}
			}

			// Poll and update status of monitored orders
			for _, order := range strat.GetMonitoredOrders() {
				placedOrder, err := client.GetOrder(context.Background(), strat.Symbol, order.ID)
				if err == nil && placedOrder != nil && string(placedOrder.Status) != "" {
					if order.Status != string(placedOrder.Status) {
						filledPrice, _ := strconv.ParseFloat(placedOrder.AvgPrice, 64)
						filledQty, _ := strconv.ParseFloat(placedOrder.ExecutedQty, 64)

						// Just notify strategy module, strategy module will trigger state persistence through callbacks
						strat.OnOrderUpdate(order.ID, string(placedOrder.Status), filledPrice, filledQty)
					}
				}
			}

			// After all other checks, execute order retry logic
			strat.RetryPlacePendingOrders()

			if time.Since(lastHeartbeat) >= heartbeatInterval {
				logs.Info("[Heartbeat] Monitor service still running...")
				lastHeartbeat = time.Now()
			}

			// Regular exchange time synchronization
			if time.Since(lastSyncTime) >= timeSyncInterval {
				logs.Info("[Monitor] Executing regular time synchronization...")
				if err := client.SyncTime(); err != nil {
					logs.Errorf("[Monitor-Error] Regular time synchronization failed: %v", err)
				}
				lastSyncTime = time.Now()
			}
		}
	}
}
