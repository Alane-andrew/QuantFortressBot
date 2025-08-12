// state/state.go
package state

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
)

// --- 1. Define Interface ---

// StateManagerInterface defines all capabilities of the state manager for upper-level modules (such as Orchestrator) to call.
// This interface-oriented design decouples upper-level modules from specific file storage implementations, facilitating testing and future extensions.
type StateManagerInterface interface {
	// GetFullState returns a deep copy of all current states for startup reconciliation use.
	GetFullState() AppState
	// AddPairedPrice records a newly created close order with its paired open price.
	AddPairedPrice(orderID string, pairedPrice float64) error
	// RemovePairedPrice removes the paired record after a close order is filled or cancelled.
	RemovePairedPrice(orderID string) error
	// UpdateGridRealizedPNL updates the realized profit of the grid strategy.
	UpdateGridRealizedPNL(pnl float64) error
	// UpdateBHLSState completely replaces old state with the latest risk control module state.
	UpdateBHLSState(bhlsState *BHLSState) error
	// UpdateBHLSRealizedPNL updates the realized profit of risk control hedging.
	UpdateBHLSRealizedPNL(pnl float64) error
}

// --- 2. Simplified Data Structure ---

// BHLSState remains unchanged as it is itself a type of metadata that needs to be persisted.
type BHLSState struct {
	IsActive             bool         `json:"is_active"`
	CumulativePNL        float64      `json:"cumulative_pnl"`
	RealizedProfit       float64      `json:"realized_profit"`
	ActiveHedges         []HedgeTrade `json:"active_hedges"`
	IsBreakerTipped      bool         `json:"is_breaker_tipped"`
	BaseHedgePositionAmt float64      `json:"base_hedge_position_amt"`
}

type HedgeTrade struct {
	EntryPrice    float64 `json:"entry_price"`
	Amount        float64 `json:"amount"`
	StopLossPrice float64 `json:"stop_loss_price"`
	Level         int     `json:"level"`
}

// GridState now only stores metadata for the grid strategy.
type GridState struct {
	RealizedProfit    float64            `json:"realized_profit"`
	PairedOrderPrices map[string]float64 `json:"paired_order_prices"` // key: close_order_id, value: open_price
}

// AppState is the top-level structure persisted to state.json.
type AppState struct {
	Grid *GridState `json:"grid"`
	BHLS *BHLSState `json:"bhls"`
}

// --- 3. Refactor StateManager Implementation ---

// StateManager is the concrete file implementation of StateManagerInterface.
type StateManager struct {
	mu       sync.RWMutex
	filePath string
	state    *AppState
}

// NewStateManager creates and initializes a new state manager.
// Its responsibility is to load existing state, or create a new empty state if it doesn't exist.
func NewStateManager(filePath string) (*StateManager, error) {
	sm := &StateManager{
		filePath: filePath,
		state: &AppState{ // Initialize a default empty state
			Grid: &GridState{
				PairedOrderPrices: make(map[string]float64),
			},
			BHLS: &BHLSState{
				ActiveHedges: make([]HedgeTrade, 0),
			},
		},
	}

	if err := sm.load(); err != nil {
		// If the error is "file not found", this is normal and we can continue with an empty state.
		if os.IsNotExist(err) {
			fmt.Printf("Info: State file not found at %s. Starting with a fresh state.\n", filePath)
			// Also ensure an empty state.json file is created so subsequent save operations can succeed
			if err := sm.save(); err != nil {
				return nil, fmt.Errorf("failed to create initial empty state file: %w", err)
			}
			return sm, nil
		}
		// For other types of loading errors, return failure.
		return nil, fmt.Errorf("failed to load initial state: %w", err)
	}

	return sm, nil
}

// save is a private method that performs atomic save while holding the lock.
func (sm *StateManager) save() error {
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state for saving: %w", err)
	}

	tmpFilePath := sm.filePath + ".tmp"
	if err := ioutil.WriteFile(tmpFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write to temporary state file: %w", err)
	}

	return os.Rename(tmpFilePath, sm.filePath)
}

// load is a private method that loads the file while holding the lock.
func (sm *StateManager) load() error {
	data, err := ioutil.ReadFile(sm.filePath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil // Empty file is considered valid, use default empty state
	}
	return json.Unmarshal(data, sm.state)
}

// --- 4. Implement Interface Methods ---

func (sm *StateManager) GetFullState() AppState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	// Return a deep copy to prevent external modification of internal state
	copiedState := *sm.state
	return copiedState
}

func (sm *StateManager) AddPairedPrice(orderID string, pairedPrice float64) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.Grid == nil {
		sm.state.Grid = &GridState{PairedOrderPrices: make(map[string]float64)}
	}
	sm.state.Grid.PairedOrderPrices[orderID] = pairedPrice
	return sm.save()
}

func (sm *StateManager) RemovePairedPrice(orderID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.Grid != nil {
		delete(sm.state.Grid.PairedOrderPrices, orderID)
	}
	return sm.save()
}

func (sm *StateManager) UpdateGridRealizedPNL(pnl float64) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.Grid == nil {
		sm.state.Grid = &GridState{PairedOrderPrices: make(map[string]float64)}
	}
	sm.state.Grid.RealizedProfit += pnl
	return sm.save()
}

func (sm *StateManager) UpdateBHLSState(bhlsState *BHLSState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.BHLS = bhlsState
	return sm.save()
}

func (sm *StateManager) UpdateBHLSRealizedPNL(pnl float64) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.BHLS == nil {
		sm.state.BHLS = &BHLSState{}
	}
	sm.state.BHLS.RealizedProfit += pnl
	return sm.save()
}
