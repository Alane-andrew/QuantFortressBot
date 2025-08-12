// orchestrator.go
package main

import (
	"auto_bian_go_1/config"
	"auto_bian_go_1/exchange"
	"auto_bian_go_1/investment"
	"auto_bian_go_1/logs"
	"auto_bian_go_1/monitor"
	"auto_bian_go_1/profit"
	"auto_bian_go_1/risk"
	"auto_bian_go_1/state"
	"auto_bian_go_1/strategy"
	"auto_bian_go_1/utils" // Added: import utils package
	"context"
	"fmt"
	"math"
	"os" // Added: import os package
	"strconv"
	"strings"
	"sync"
	"time" // Added: import time package
)

type Orchestrator struct {
	client            exchange.Client
	strategy          *strategy.GridStrategy
	riskManager       risk.RiskManager
	investmentManager *investment.Manager
	profitAccountant  *profit.Accountant
	hedgeAccountant   *profit.Accountant
	stateManager      state.StateManagerInterface
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	cfg               *config.Config
	stateFilePath     string // Added: state file path
}

func NewOrchestrator(cfg *config.Config, envCfg *config.EnvConfig, stateFilePath string) (*Orchestrator, error) {
	var client exchange.Client
	if cfg.UseSimulation {
		mockClient := exchange.NewMockClient()
		// Configure mock client here as needed
		client = mockClient
		mockClient.Start()
		logs.Warnf("<<<<<<<<<< WARNING: Running in simulation mode >>>>>>>>>>")
	} else {
		client = exchange.NewAPIClient(envCfg.ApiKey, envCfg.ApiSecret, envCfg.BaseURL, cfg.Normal.HTTPTimeoutSeconds, cfg.Normal.RecvWindowSeconds)
		// Ensure time synchronization before making any API calls
		if err := client.SyncTime(); err != nil {
			return nil, fmt.Errorf("failed to sync exchange time: %w", err)
		}
	}

	// ---- Core fix: Execute "cold start" check and initialize state manager before creating any dependency state modules ----

	// 1. Check if exchange is completely idle
	positionInfo, err := client.GetPositionInfo(context.Background(), cfg.Symbol, cfg.PositionSide)
	if err != nil {
		return nil, fmt.Errorf("failed to get main position info at startup: %w", err)
	}

	hedgeSide := "LONG"
	if strings.ToUpper(cfg.PositionSide) == "LONG" {
		hedgeSide = "SHORT"
	}
	hedgePositionInfo, err := client.GetPositionInfo(context.Background(), cfg.Symbol, hedgeSide)
	if err != nil {
		return nil, fmt.Errorf("failed to get hedge position info at startup: %w", err)
	}

	openOrders, err := client.GetOpenOrders()
	if err != nil {
		return nil, fmt.Errorf("failed to get open orders at startup: %w", err)
	}

	isTrulyIdle := positionInfo.PositionAmt == 0 && hedgePositionInfo.PositionAmt == 0 && len(openOrders) == 0

	// 2. If idle, remove old state file
	if isTrulyIdle {
		logs.Warnf("[Orchestrator] Detected no positions or pending orders on exchange. This is a fresh start.")
		logs.Warnf("[Orchestrator] Will ignore and remove old state file: %s", stateFilePath)
		if err := os.Remove(stateFilePath); err != nil {
			if !os.IsNotExist(err) {
				logs.Errorf("[Orchestrator] Failed to remove old state file (not 'file not exist' error): %v. Will continue to try loading.", err)
			}
		}
	}

	// 3. After checking, create state manager. At this point it will load old state or create new state based on file existence.
	stateManager, err := state.NewStateManager(stateFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize state manager: %w", err)
	}
	logs.Infof("State manager initialized successfully, state will be persisted to: %s", stateFilePath)

	// ---- State management initialization complete ----

	// Get symbol info early as it's needed in multiple places
	symbolInfo, ok := client.GetSymbolInfo(cfg.Symbol)
	if !ok {
		return nil, fmt.Errorf("unable to get symbol info for %s", cfg.Symbol)
	}

	// Remove redundant, imprecise calculation logic

	if cfg.MarginType != "" {
		logs.Infof("Setting margin mode for %s to: %s...", cfg.Symbol, cfg.MarginType)
		if err := client.SetMarginType(cfg.Symbol, cfg.MarginType); err != nil && !strings.Contains(err.Error(), "-4046") {
			return nil, fmt.Errorf("failed to set margin mode: %w", err)
		} else if err == nil {
			logs.Info("Margin mode set successfully.")
		} else {
			logs.Infof("Margin mode unchanged (already %s).", cfg.MarginType)
		}
	}

	placeOrder := func(price float64, side string, reduceOnly bool) (string, error) {
		// Step 1: Use PricePrecision for basic precision adjustment
		roundedPrice := utils.RoundToPrecision(price, symbolInfo.PricePrecision)

		// Step 2: Extract tick size from symbolInfo.Filters and apply precisely
		var tickSize float64
		for _, filter := range symbolInfo.Filters {
			if filterType, ok := filter["filterType"].(string); ok && filterType == "PRICE_FILTER" {
				if tickSizeStr, ok := filter["tickSize"].(string); ok {
					if parsedTickSize, err := strconv.ParseFloat(tickSizeStr, 64); err == nil && parsedTickSize > 0 {
						tickSize = parsedTickSize
						break
					}
				}
			}
		}

		// Step 3: Use tick size for final adjustment (completely dependent on Binance data)
		if tickSize > 0 {
			roundedPrice = utils.AdjustPriceToTickSize(roundedPrice, tickSize)
		} else {
			logs.Errorf("[Order Placement] Unable to get Binance tick size info, using precision-adjusted price: %.8f", roundedPrice)
		}

		// Step 4: Ensure quantity completely complies with Binance precision requirements
		// Get Binance quantity precision requirements
		var quantityPrecision int
		for _, filter := range symbolInfo.Filters {
			if filterType, ok := filter["filterType"].(string); ok && filterType == "LOT_SIZE" {
				if stepSizeStr, ok := filter["stepSize"].(string); ok {
					if stepSize, err := strconv.ParseFloat(stepSizeStr, 64); err == nil && stepSize > 0 {
						// Calculate precision based on stepSize
						quantityPrecision = 0
						for stepSize < 1 {
							stepSize *= 10
							quantityPrecision++
						}
						break
					}
				}
			}
		}

		// Use Binance precision requirements to adjust quantity
		roundedQty := utils.RoundToPrecision(cfg.Grid.GridQty, quantityPrecision)

		// Check if adjusted quantity is valid
		if roundedQty <= 0 {
			return "", fmt.Errorf("adjusted order quantity is %.8f, must be greater than 0. Please increase total investment or reduce grid count", roundedQty)
		}

		order := &exchange.Order{
			Symbol:       cfg.Symbol,
			Side:         exchange.OrderSide(side),
			PositionSide: exchange.PositionSide(strings.ToUpper(cfg.PositionSide)),
			Type:         exchange.Limit,
			Price:        strconv.FormatFloat(roundedPrice, 'f', symbolInfo.PricePrecision, 64),
			OrigQty:      strconv.FormatFloat(roundedQty, 'f', quantityPrecision, 64),
			ReduceOnly:   reduceOnly,
		}

		// Add timeout control to prevent network requests from blocking indefinitely
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		placedOrder, err := client.PlaceOrder(ctx, order)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(placedOrder.OrderID, 10), nil
	}

	riskManager := risk.NewBHLSManager(client, cfg.BHLS, cfg.Symbol, cfg.PositionSide, cfg.GridUpper, cfg.GridLower, cfg.TotalInvestmentUSDT, symbolInfo.QuantityPrecision)
	investmentManager := investment.NewManager(client, cfg.Symbol, cfg.PositionSide, cfg.TotalInvestmentUSDT)
	profitAccountant := profit.NewAccountant()
	hedgeAccountant := profit.NewAccountant()

	// --- Restore old logic: Dynamically calculate safe GridQty ---
	if cfg.TotalInvestmentUSDT > 0 && cfg.Grid.Leverage > 0 {
		// 1. Calculate all grid point prices (consistent with strategy/grid.go algorithm)
		gridNum := cfg.Grid.GridNum
		step := (cfg.GridUpper - cfg.GridLower) / float64(gridNum)
		totalPricePerUnit := 0.0
		for i := 0; i < gridNum; i++ {
			totalPricePerUnit += cfg.GridLower + step*float64(i)
		}

		// 2. Calculate the nominal value required to run the entire grid
		notionalValuePerUnit := totalPricePerUnit

		// 3. Backtrack to find the safe grid_qty based on total nominal value limit
		if notionalValuePerUnit > 0 {
			rawSafeQty := cfg.TotalInvestmentUSDT / notionalValuePerUnit

			// Use existing tools to adjust quantity based on exchange precision
			// We use floor here to ensure absolute safety
			multiplier := math.Pow(10, float64(symbolInfo.QuantityPrecision))
			safeQty := math.Floor(rawSafeQty*multiplier) / multiplier

			cfg.Grid.GridQty = safeQty // Override value in config

			if cfg.Grid.GridQty <= 0 {
				return nil, fmt.Errorf("safe order quantity calculated from total investment is 0, please increase total investment or reduce grid count/interval")
			}
		}
	}
	// --- Calculation complete ---

	strat, err := strategy.NewGridStrategy(client, cfg.Symbol, cfg.PositionSide, cfg.Grid, cfg.GridUpper, cfg.GridLower, placeOrder)
	if err != nil {
		return nil, fmt.Errorf("failed to create grid strategy: %w", err)
	}

	strat.SetOnTrade(profitAccountant.RecordTrade)
	strat.SetOnCloseOrderPlaced(stateManager.AddPairedPrice)
	strat.SetOnOrderCancelled(stateManager.RemovePairedPrice)

	ctx, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{
		client:            client,
		strategy:          strat,
		riskManager:       riskManager,
		investmentManager: investmentManager,
		profitAccountant:  profitAccountant,
		hedgeAccountant:   hedgeAccountant,
		stateManager:      stateManager,
		ctx:               ctx,
		cancel:            cancel,
		cfg:               cfg,
		stateFilePath:     stateFilePath, // New
	}

	if err := o.reconcileStateOnStartup(); err != nil {
		return nil, fmt.Errorf("failed to reconcile state on startup: %w", err)
	}

	return o, nil
}

func (o *Orchestrator) reconcileStateOnStartup() error {
	logs.Info("[Orchestrator] Starting state reconciliation on startup...")

	// 1. Get state data from state file (but do not restore immediately)
	appState := o.stateManager.GetFullState()

	// 2. Get open orders and position info from exchange as final reference (Ground Truth)
	openOrdersValues, err := o.client.GetOpenOrders()
	if err != nil {
		return fmt.Errorf("failed to get open orders at startup: %w", err)
	}
	// Convert map to slice
	openOrders := make([]*exchange.Order, 0, len(openOrdersValues))
	for _, order := range openOrdersValues {
		order := order // Create a copy of the loop variable to prevent pointer aliasing.
		openOrders = append(openOrders, &order)
	}
	logs.Infof("[Orchestrator] Retrieved %d open orders from exchange (all symbols).", len(openOrders))

	// 2.1. Filter out orders for the current symbol to avoid processing orders for other symbols
	var currentSymbolOrders []*exchange.Order
	for _, order := range openOrders {
		if order.Symbol == o.cfg.Symbol {
			currentSymbolOrders = append(currentSymbolOrders, order)
		}
	}

	if len(currentSymbolOrders) != len(openOrders) {
		logs.Infof("[Orchestrator] After filtering, number of orders for current symbol %s: %d (total orders: %d)",
			o.cfg.Symbol, len(currentSymbolOrders), len(openOrders))
	}

	positionInfo, err := o.client.GetPositionInfo(context.Background(), o.cfg.Symbol, o.cfg.PositionSide)
	if err != nil {
		return fmt.Errorf("failed to get position info at startup: %w", err)
	}
	logs.Infof("[Orchestrator] Retrieved position info, quantity: %f", positionInfo.PositionAmt)

	// 2.2. Use real hedge position from exchange to calibrate BHLS state for recovery
	hedgeSide := "LONG"
	if strings.ToUpper(o.cfg.PositionSide) == "LONG" {
		hedgeSide = "SHORT"
	}
	hedgePositionInfo, err := o.client.GetPositionInfo(context.Background(), o.cfg.Symbol, hedgeSide)
	if err != nil {
		logs.Errorf("[Orchestrator-Warning] Failed to get hedge position info at startup: %v. Risk control state may be inaccurate.", err)
	} else {
		// --- Final fix: At startup, hedge module state must be the only true source for the exchange ---
		if hedgePositionInfo.PositionAmt == 0 {
			// Exchange has no hedge position, regardless of what the state file says, it's based on the exchange.
			if appState.BHLS != nil && len(appState.BHLS.ActiveHedges) > 0 {
				logs.Warnf("[Orchestrator-Reconciliation] State file recorded %d active hedges, but exchange hedge position is 0. Will clear local hedge records to match exchange reality.", len(appState.BHLS.ActiveHedges))
				appState.BHLS.ActiveHedges = []state.HedgeTrade{} // Clear ghost records
				appState.BHLS.IsActive = false                    // Deactivate hedge activation state
			}
		} else {
			// Exchange actually has a hedge position, now we ensure local state is consistent with it.
			logs.Infof("[Orchestrator-Reconciliation] Detected exchange hedge position: %.4f. Will calibrate risk control module state based on this.", hedgePositionInfo.PositionAmt)
			if appState.BHLS == nil {
				appState.BHLS = &state.BHLSState{}
			}
			appState.BHLS.IsActive = true
			if len(appState.BHLS.ActiveHedges) == 0 {
				logs.Warnf("[Orchestrator-Reconciliation] Risk control state file has no hedge records, will create a virtual record based on exchange position.", hedgePositionInfo.PositionAmt)
				appState.BHLS.ActiveHedges = []state.HedgeTrade{
					{
						EntryPrice: hedgePositionInfo.EntryPrice,
						Amount:     math.Abs(hedgePositionInfo.PositionAmt),
						Level:      1, // Cannot know true level, default to 1
					},
				}
			} else {
				logs.Infof("[Orchestrator-Reconciliation] Risk control state file already has %d hedge records, will continue to use these records.", len(appState.BHLS.ActiveHedges))
			}
		}
	}

	// 3. Determine if it's a cold start
	// Key logic: Only consider it a fresh start if there are no open orders and no positions for the current symbol.
	// Note: Orders and positions for other symbols do not affect this judgment, only focus on the current symbol configured.
	isColdStart := len(currentSymbolOrders) == 0 && positionInfo.PositionAmt == 0

	// 4. Decide recovery strategy based on cold start status
	if isColdStart {
		logs.Info("[Orchestrator] No open orders or positions found, will initialize a new grid strategy.")
		// 4.1 Reset all states during cold start, do not restore any historical data
		o.riskManager.Restore(&state.BHLSState{})
		o.hedgeAccountant.Restore(0)
		o.profitAccountant.Restore(0)
		// 4.2 Initialize a new grid strategy
		if err := o.strategy.InitOrders(); err != nil {
			return fmt.Errorf("failed to initialize new grid strategy: %w", err)
		}
	} else {
		logs.Info("[Orchestrator] Active position or open order detected, will restore grid from exchange state.")
		// 4.1 Restore all states during non-cold start
		o.riskManager.Restore(appState.BHLS)
		o.hedgeAccountant.Restore(o.riskManager.GetCumulativePNL())
		o.profitAccountant.Restore(appState.Grid.RealizedProfit)
		// 4.2 Restore grid strategy
		o.strategy.RestoreMonitoredOrders(currentSymbolOrders, appState.Grid.PairedOrderPrices)
	}

	logs.Info("[Orchestrator] State file restored. PNL and risk control module state.")
	logs.Info("[Orchestrator] State reconciliation complete.")
	return nil
}

func (o *Orchestrator) Start() {
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		monitor.Start(
			o.client,
			o.strategy,
			o.riskManager,
			o.investmentManager,
			nil,
			o.profitAccountant,
			o.hedgeAccountant,
			o.stateManager,
			o.cfg, // Pass the entire config object
			o.ctx.Done(),
		)
	}()
	logs.Infof("Strategy %s started, press Ctrl+C to exit.", o.cfg.Symbol)
}

func (o *Orchestrator) Stop() {
	logs.Info("Received close signal, starting graceful shutdown...")

	// Stop monitor to prevent it from executing new operations during shutdown
	// o.monitor.Stop()
	// logs.Info("Monitor stopped.")

	// Gracefully cancel all open orders
	// if err := o.strategy.CancelAllOrders(); err != nil {
	// 	logs.Errorf("Failed to cancel orders during shutdown: %v", err)
	// } else {
	// 	logs.Info("All open orders cancelled.")
	// }

	// Print final PnL summary
	o.printFinalSummary()

	// **Key fix**: Synchronize the latest state of modules with the state manager before saving

	// 1. Get the latest hedge PNL from hedgeAccountant and synchronize with riskManager
	finalHedgePNL := o.hedgeAccountant.GetRealizedPNL()
	o.riskManager.SetCumulativePNL(finalHedgePNL)

	// 2. Synchronize and save the full state of the risk control module
	finalBHLSState := o.riskManager.GetState()
	if err := o.stateManager.UpdateBHLSState(finalBHLSState); err != nil {
		logs.Errorf("Failed to save final risk control state: %v", err)
	} else {
		logs.Infof("[Orchestrator] Synchronized and saved final risk control state. PNL: %.4f, ActiveHedges: %d",
			finalBHLSState.CumulativePNL, len(finalBHLSState.ActiveHedges))
	}

	// 3. New: Synchronize and save grid strategy realized profit
	finalGridPNL := o.profitAccountant.GetRealizedPNL()
	if err := o.stateManager.UpdateGridRealizedPNL(finalGridPNL); err != nil {
		logs.Errorf("Failed to save final grid profit: %v", err)
	} else {
		logs.Infof("[Orchestrator] Synchronized and saved final grid profit: %.4f", finalGridPNL)
	}

	// Send cancellation signal to all goroutines
	o.cancel()
	// Wait for all goroutines to complete
	o.wg.Wait()
	logs.Info("All services stopped successfully.")
}

func (o *Orchestrator) printFinalSummary() {
	logs.Info("\n--- Final PnL Summary ---")
	gridPNL := o.profitAccountant.GetPositionState().RealizedProfit
	logs.Infof("Grid strategy realized profit: %.4f USDT", gridPNL)
	hedgePNL := o.hedgeAccountant.GetRealizedPNL()
	logs.Infof("Risk control hedge realized profit: %.4f USDT", hedgePNL)

	gridPos, err := o.client.GetPositionInfo(context.Background(), o.cfg.Symbol, o.cfg.PositionSide)
	if err != nil {
		logs.Errorf("Failed to get grid position info: %v", err)
		gridPos = &exchange.PositionInfo{}
	} else {
		logs.Infof("Grid strategy position: %.4f %s (Unrealized PnL: %.4f USDT)", gridPos.PositionAmt, o.cfg.Symbol, gridPos.UnrealizedProfit)
	}

	hedgeSide := "LONG"
	if strings.ToUpper(o.cfg.PositionSide) == "LONG" {
		hedgeSide = "SHORT"
	}
	hedgePos, err := o.client.GetPositionInfo(context.Background(), o.cfg.Symbol, hedgeSide)
	if err != nil {
		logs.Errorf("Failed to get hedge position info: %v", err)
		hedgePos = &exchange.PositionInfo{}
	} else {
		logs.Infof("Risk control hedge position: %.4f %s (Unrealized PnL: %.4f USDT)", hedgePos.PositionAmt, o.cfg.Symbol, hedgePos.UnrealizedProfit)
	}

	totalUnrealizedPNL := gridPos.UnrealizedProfit + hedgePos.UnrealizedProfit
	finalTotalPNL := gridPNL + hedgePNL + totalUnrealizedPNL
	logs.Info("--------------------")
	logs.Infof("Final total PnL: %.4f USDT", finalTotalPNL)
	logs.Info("--------------------")
}

// --- IMonitorable interface implementation ---

func (o *Orchestrator) GetStrategy() *strategy.GridStrategy {
	return o.strategy
}

func (o *Orchestrator) GetRiskManager() risk.RiskManager {
	return o.riskManager
}

func (o *Orchestrator) GetTTPManager() *profit.GridTakeProfitManager {
	return nil // TTPManager is removed, return nil
}

func (o *Orchestrator) GetInvestmentManager() *investment.Manager {
	return o.investmentManager
}

func (o *Orchestrator) GetProfitAccountant() *profit.Accountant {
	return o.profitAccountant
}
