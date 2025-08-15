<p align="center">
  <img src="assets/logoA_shield_grid_light_small.svg" width="220" alt="QuantFortressBot logo" />
</p>

# QuantFortressBot

A risk-focused **trading bot** for crypto perpetual futures featuring **layered hedging** and **progressive stop-loss** with black-swan protection.  
The BHLS core is delivered as **interface + black-box** (proprietary implementation is not published).

## ✨ Key Features
- **Layered Hedging (BHLS)** — Open hedges in levels once price breaches grid bounds; unwind via **progressive stop-loss**.
- **Black-Swan Protection** — Circuit breaker by cumulative PnL, with emergency liquidation & trading halt.
- **Closed-Loop Architecture** — Clear separation of concerns: strategy → orchestrator → execution → risk engine → state/monitor/profit.
- **Interface + Black-Box** — Public repo exposes the risk engine **interfaces & actions**; the BHLS implementation file stays private.
- **Production-Oriented** — Exchange adapters (Binance), precision handling, structured logs, monitoring loops, and state snapshots.

## 🧩 Architecture

<p align="center">
  <picture>
    <source srcset="assets/architecture_dark.svg" type="image/svg+xml">
<!--     <img src="assets/architecture.png" width="900" alt="QuantFortressBot Architecture"> -->
  </picture>
</p>


## 🧭 Project Structure
```
config/        # YAML configs (public example uses placeholders; real config is not committed)
exchange/      # Exchange client & adapters (orders, positions, symbol meta)
investment/    # Capital allocation & sizing helpers
logs/          # Logging setup & wrappers
monitor/       # Price/health monitors, triggers and observers
profit/        # PnL aggregation, profit-taking helpers
risk/          # Risk interfaces & action types (BHLS implementation is private)
state/         # Persisted snapshots (active hedges, breaker flags, cumulative PnL)
strategy/      # Grid/entry/exit logic and integration hooks
utils/         # Precision/math/time/rounding/validation helpers

main.go        # Program entry
orchestrator.go# Central coordinator: routes ticks/events to strategy & risk engines
```

## Module Status
- `risk/*` — Public interfaces & actions; BHLS implementation is private (NDA).
- `profit/grid_ttp_manager.go`, `profit/ttp_actions.go` — **Experimental / not enabled yet.**
  - These files contain the Trailing Take-Profit (TTP) scaffolding used to refine logic.
  - The feature is currently **off by default** and will be completed and officially enabled in future releases.

### Component Collaboration (high level)
1) **Strategy** produces intents (grid bounds, entries/exits).  
2) **Orchestrator** polls market & state, then calls **RiskManager.CheckAndManageRisk(price)**.  
3) **Risk** emits **actions** (e.g., `OpenHedgeAction`, `CloseHedgeAction`, `HaltGridAction`, `ResumeGridAction`, `LiquidateAndStopAction`).  
4) **Exchange** executes; **State** persists; **Monitor/Profit** aggregate telemetry; **Logs** record full trails.

## 🔒 Private Core (not in this repo)
**`risk/bhls_manager.go`** contains proprietary algorithms:
- Layer scheduling (start/step %) from grid boundaries  
- Per-level sizing from base exposure  
- Progressive stop-loss placement/updates  
- Cumulative PnL breaker governance  
- Exchange filter compliance (precision/limits)  
- Extreme-condition fallbacks

> Full source can be provided **under NDA** for interviews/partnerships.  
> Public repo exposes: `risk/manager.go` (interface), `risk/actions.go` (action types), and `risk/README.md` (integration & testing guide).

## ⚙ Configuration
This repository ships **two kinds of configs**:

- **Public trimmed example:** `config/config.yaml.example`  
  Minimal keys + **placeholder values** for demonstration only.

- **Full-key placeholder (for due diligence under NDA):** `config/config.yaml.placeholder`  
  Exactly matches the internal schema (all **key names preserved**), with every value masked.

> Real production `config.yaml` and secrets are **never** committed.

**Example (trimmed):**
```yaml
symbol: "<symbol>"
position_side: "<short|long>"
margin_type: "<CROSSED|ISOLATED>"
total_investment_usdt: <value>
grid_upper: <value>
grid_lower: <value>
use_simulation: <true|false>

normal_config:
  monitor_interval_seconds: <value>
  log_directory: "<logs>"
  state_directory: "<state>"

logs:
  log_level: "<debug|info|warn|error>"

strategies:
  - name: "grid"
    enabled: <true|false>
    config:
      leverage: <value>
      grid_num: <value>

  - name: "bhls"
    enabled: <true|false>
    config:
      strategy_max_loss: <value>
      hedge_start_pct: <value>
      hedge_step_pct: <value>
      max_hedge_levels: <value>
      hedge_level_percentages: "<csv>"  
      hedge_stop_loss_pct: <value>
```

### Getting Started
```bash
go mod tidy
cp config/config.yaml.example config/config.yaml   # fill your values or use NDA placeholder
go run main.go
```
- The public build compiles against the **RiskManager interface**.
- Without `risk/bhls_manager.go`, hedging behavior depends on your own implementation or a stub wired in `orchestrator.go`.

## 🤝 NDA & Collaboration
- Full BHLS source and the full-key placeholder config are available **under NDA** for technical due diligence or partnership.


## 📄 License
Open-source files are under MIT (or your choice).  
Proprietary implementations remain closed and are licensed separately.
