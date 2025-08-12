# Risk Module — Interface + Black-Box

This folder publishes the **risk engine contract** and **action types** consumed by the orchestrator.  
The proprietary **BHLS** implementation is intentionally **not** part of the public repository.

## 📦 Public API

- `manager.go` — risk engine contract
```go
type RiskManager interface {
    // Called periodically with the current market price; returns a batch of actions
    // such as opening/closing hedge, halting/resuming grid, or triggering breaker.
    CheckAndManageRisk(currentPrice float64) []interface{}

    // State & telemetry
    Restore(st *state.BHLSState)
    GetState() *state.BHLSState
    SetCumulativePNL(pnl float64)
    GetCumulativePNL() float64
    GetActiveHedgeCount() int
    IsBreakerTipped() bool
}
```

- `actions.go` — canonical actions:
  - `OpenHedgeAction` / `CloseHedgeAction`
  - `HaltGridAction` / `ResumeGridAction`
  - `LiquidateAndStopAction`
  - (Optionally) `NoOpAction` to signal explicit “do nothing”.

These actions are orchestrated to pause/resume grid, open/close hedges, or trigger circuit breaker.

## 🔒 Private (not in this repo)
- `bhls_manager.go` — BHLS = layered **B**atched **H**edging + **L**ayered **S**top-loss:
  - Layer scheduling (start/step %) from grid bounds
  - Per-level sizing from base exposure
  - Progressive stop-loss placement/updates
  - Cumulative PnL breaker logic
  - Exchange filter compliance (precision/limits)
  - Extreme-condition fallbacks

**Access under NDA** for interviews/partnerships.

## 🔌 Implement your own RiskManager
1. Create `my_risk_manager.go` under `risk/`.  
2. Implement the `RiskManager` methods.  
3. Wire it in `orchestrator.go` (factory/DI).

This allows experimenting with alternative policies while keeping the trading core stable.

## 🧪 Testing
- **Unit tests**: assert emitted actions under designed price paths (inside/outside grid, step offsets, SL hits, breaker thresholds).
- **Persistence**: verify `GetState/Restore` for resumption after restarts.
- **Exchange rules**: cover precision rounding, min notional, and filter compliance.

## 📩 Contact
For BHLS source evaluation or collaboration, please reach out for an **NDA**.  
Contact: <your email / Telegram / LinkedIn>
