// config/config.go
package config

import (
	"fmt"
	"io/ioutil"
	"os"

	"gopkg.in/yaml.v2"
)

// GridConfig holds configuration for the grid strategy.
type GridConfig struct {
	Leverage int     `yaml:"leverage"`
	GridNum  int     `yaml:"grid_num"`
	GridQty  float64 `yaml:"grid_qty"`
}

// BHLSConfig holds the configuration for the Batch Hedging and Layered Stop-loss strategy.
type BHLSConfig struct {
	StrategyMaxLoss       float64   `yaml:"strategy_max_loss"`
	HedgeStartPct         float64   `yaml:"hedge_start_pct"`
	HedgeStepPct          float64   `yaml:"hedge_step_pct"`
	MaxHedgeLevels        int       `yaml:"max_hedge_levels"`
	HedgeStopLossPct      float64   `yaml:"hedge_stop_loss_pct"`
	HedgeLevelPercentages []float64 `yaml:"hedge_level_percentages"`
}

// LogConfig holds the configuration for logging.
type LogConfig struct {
	LogLevel   string `yaml:"log_level"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAgeDays int    `yaml:"max_age_days"`
	Compress   bool   `yaml:"compress"`
}

// NormalConfig holds all general, non-strategy-specific configuration.
type NormalConfig struct {
	HTTPTimeoutSeconds       int    `yaml:"http_timeout_seconds"`
	RecvWindowSeconds        int    `yaml:"recv_window_seconds"`
	MonitorIntervalSeconds   int    `yaml:"monitor_interval_seconds"`
	HeartbeatIntervalMinutes int    `yaml:"heartbeat_interval_minutes"`
	TimeSyncIntervalMinutes  int    `yaml:"time_sync_interval_minutes"`
	LogDirectory             string `yaml:"log_directory"`
	StateDirectory           string `yaml:"state_directory"`
}

// StrategyConfig is a generic container for a single strategy's configuration.
// We use an interface{} for Config to allow for different strategy structures.
type StrategyConfig struct {
	Name    string      `yaml:"name"`
	Enabled bool        `yaml:"enabled"`
	Config  interface{} `yaml:"config"`
}

// Config is the top-level configuration structure.
type Config struct {
	Symbol              string        `yaml:"symbol"`
	PositionSide        string        `yaml:"position_side"`
	GridUpper           float64       `yaml:"grid_upper"`
	GridLower           float64       `yaml:"grid_lower"`
	Grid                *GridConfig   `yaml:"grid"`
	BHLS                *BHLSConfig   `yaml:"bhls"`
	UseSimulation       bool          `yaml:"use_simulation"`
	Normal              *NormalConfig `yaml:"normal_config"`
	MarginType          string        `yaml:"margin_type"`
	TotalInvestmentUSDT float64       `yaml:"total_investment_usdt"`
	Logs                *LogConfig    `yaml:"logs"` // New: Log configuration
}

// NewConfig creates a new Config struct with essential allocations but no magic numbers.
// All critical strategy parameters MUST be provided in the config.yaml file.
func NewConfig() *Config {
	return &Config{
		// Set only safe, non-strategy defaults
		PositionSide:  "short",
		UseSimulation: false,
		// Allocate memory for nested structs, but their fields will be zero-valued.
		// Validation will ensure they are populated from the YAML file.
		Logs:   &LogConfig{},
		Grid:   &GridConfig{},
		BHLS:   &BHLSConfig{},
		Normal: &NormalConfig{},
	}
}

// LoadConfig loads configuration from a given path, applies defaults, and validates it.
func LoadConfig(path string) (*Config, error) {
	cfg := NewConfig()

	data, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Error: Config file config.yaml not found at %s. Program cannot run without a config file", path)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// A temporary struct to unmarshal the raw strategy configs
	var rawCfg struct {
		Symbol              string           `yaml:"symbol"`
		PositionSide        string           `yaml:"position_side"`
		MarginType          string           `yaml:"margin_type"`
		TotalInvestmentUSDT float64          `yaml:"total_investment_usdt"`
		GridUpper           float64          `yaml:"grid_upper"`
		GridLower           float64          `yaml:"grid_lower"`
		UseSimulation       bool             `yaml:"use_simulation"`
		Logs                *LogConfig       `yaml:"logs"`
		Normal              *NormalConfig    `yaml:"normal_config"`
		Strategies          []StrategyConfig `yaml:"strategies"`
	}

	if err := yaml.Unmarshal(data, &rawCfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	// Override top-level fields
	if rawCfg.Symbol != "" {
		cfg.Symbol = rawCfg.Symbol
	}
	if rawCfg.PositionSide != "" {
		cfg.PositionSide = rawCfg.PositionSide
	}
	if rawCfg.MarginType != "" {
		cfg.MarginType = rawCfg.MarginType
	}
	if rawCfg.TotalInvestmentUSDT > 0 {
		cfg.TotalInvestmentUSDT = rawCfg.TotalInvestmentUSDT
	}
	if rawCfg.GridUpper != 0 {
		cfg.GridUpper = rawCfg.GridUpper
	}
	if rawCfg.GridLower != 0 {
		cfg.GridLower = rawCfg.GridLower
	}
	cfg.UseSimulation = rawCfg.UseSimulation

	if rawCfg.Normal != nil {
		cfg.Normal = rawCfg.Normal
	}

	if rawCfg.Logs != nil {
		// Only override if the nested struct itself is provided
		cfg.Logs = rawCfg.Logs
	}

	// Unmarshal specific strategy configs based on their 'name'
	for _, s := range rawCfg.Strategies {
		if !s.Enabled {
			continue
		}

		configBytes, err := yaml.Marshal(s.Config)
		if err != nil {
			return nil, fmt.Errorf("failed to re-marshal strategy config '%s': %w", s.Name, err)
		}

		switch s.Name {
		case "grid":
			if err := yaml.Unmarshal(configBytes, cfg.Grid); err != nil {
				return nil, fmt.Errorf("failed to unmarshal grid config: %w", err)
			}
		case "bhls":
			if err := yaml.Unmarshal(configBytes, cfg.BHLS); err != nil {
				return nil, fmt.Errorf("failed to unmarshal bhls config: %w", err)
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

// Validate checks the logical consistency and completeness of the entire configuration.
func (c *Config) Validate() error {
	// Top-level validation
	if c.Symbol == "" {
		return fmt.Errorf("Critical config missing: 'symbol' must be explicitly specified in config.yaml")
	}
	if c.GridUpper <= 0 {
		return fmt.Errorf("Critical config missing: 'grid_upper' must be explicitly specified in config.yaml and be positive")
	}
	if c.GridLower <= 0 {
		return fmt.Errorf("Critical config missing: 'grid_lower' must be explicitly specified in config.yaml and be positive")
	}
	if c.GridUpper <= c.GridLower {
		return fmt.Errorf("Config error: grid_upper must be greater than grid_lower")
	}
	if c.PositionSide != "long" && c.PositionSide != "short" {
		return fmt.Errorf("Config error: position_side must be 'long' or 'short'")
	}
	if c.MarginType != "" && c.MarginType != "ISOLATED" && c.MarginType != "CROSSED" {
		return fmt.Errorf("Config error: margin_type if specified must be 'ISOLATED' or 'CROSSED'")
	}
	if c.TotalInvestmentUSDT < 0 {
		return fmt.Errorf("Config error: total_investment_usdt cannot be negative")
	}

	if c.Normal == nil {
		return fmt.Errorf("Critical config missing: 'normal_config' configuration block must be provided in config.yaml")
	}
	if c.Normal.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("Critical config missing: 'normal_config.http_timeout_seconds' must be explicitly specified in config.yaml and be positive")
	}
	if c.Normal.RecvWindowSeconds <= 0 {
		return fmt.Errorf("Critical config missing: 'normal_config.recv_window_seconds' must be explicitly specified in config.yaml and be positive")
	}
	if c.Normal.MonitorIntervalSeconds <= 0 {
		return fmt.Errorf("Critical config missing: 'normal_config.monitor_interval_seconds' must be explicitly specified in config.yaml and be positive")
	}
	if c.Normal.HeartbeatIntervalMinutes <= 0 {
		return fmt.Errorf("Critical config missing: 'normal_config.heartbeat_interval_minutes' must be explicitly specified in config.yaml and be positive")
	}
	if c.Normal.TimeSyncIntervalMinutes <= 0 {
		return fmt.Errorf("Critical config missing: 'normal_config.time_sync_interval_minutes' must be explicitly specified in config.yaml and be positive")
	}
	if c.Normal.LogDirectory == "" {
		return fmt.Errorf("Critical config missing: 'normal_config.log_directory' must be explicitly specified in config.yaml (e.g., 'logs')")
	}
	if c.Normal.StateDirectory == "" {
		return fmt.Errorf("Critical config missing: 'normal_config.state_directory' must be explicitly specified in config.yaml (e.g., 'state')")
	}

	// Logs validation
	if c.Logs == nil {
		return fmt.Errorf("Critical config missing: 'logs' configuration block must be provided in config.yaml")
	}
	if c.Logs.LogLevel == "" {
		return fmt.Errorf("Critical config missing: 'logs.log_level' must be explicitly specified in config.yaml (e.g., 'info', 'debug', 'warn', 'error')")
	}
	if c.Logs.MaxSizeMB <= 0 {
		return fmt.Errorf("Critical config missing: 'logs.max_size_mb' must be explicitly specified in config.yaml and be positive")
	}
	if c.Logs.MaxBackups <= 0 {
		return fmt.Errorf("Critical config missing: 'logs.max_backups' must be explicitly specified in config.yaml and be positive")
	}
	if c.Logs.MaxAgeDays <= 0 {
		return fmt.Errorf("Critical config missing: 'logs.max_age_days' must be explicitly specified in config.yaml and be positive")
	}

	// Grid validation
	if c.Grid.Leverage <= 0 {
		return fmt.Errorf("Critical config missing: 'grid.leverage' must be explicitly specified in the grid strategy section of config.yaml and be positive")
	}
	if c.Grid.GridNum <= 0 {
		return fmt.Errorf("Critical config missing: 'grid.grid_num' must be explicitly specified in the grid strategy section of config.yaml and be positive")
	}
	if c.TotalInvestmentUSDT <= 0 && c.Grid.GridQty <= 0 {
		return fmt.Errorf("Critical config missing: Either 'total_investment_usdt' or 'grid.grid_qty' must be explicitly specified in config.yaml and be positive")
	}

	// BHLS validation
	if c.BHLS.HedgeStartPct <= 0 {
		return fmt.Errorf("Critical config missing: 'bhls.hedge_start_pct' must be explicitly specified in the bhls strategy section of config.yaml and be positive")
	}
	if c.BHLS.HedgeStepPct <= 0 {
		return fmt.Errorf("Critical config missing: 'bhls.hedge_step_pct' must be explicitly specified in the bhls strategy section of config.yaml and be positive")
	}
	if c.BHLS.HedgeStopLossPct <= 0 {
		return fmt.Errorf("Critical config missing: 'bhls.hedge_stop_loss_pct' must be explicitly specified in the bhls strategy section of config.yaml and be positive")
	}
	if c.BHLS.MaxHedgeLevels <= 0 {
		return fmt.Errorf("Critical config missing: 'bhls.max_hedge_levels' must be explicitly specified in the bhls strategy section of config.yaml and be positive")
	}
	if len(c.BHLS.HedgeLevelPercentages) == 0 {
		return fmt.Errorf("Critical config missing: 'bhls.hedge_level_percentages' must be explicitly specified in the bhls strategy section of config.yaml")
	}
	if c.BHLS.HedgeStartPct <= c.BHLS.HedgeStopLossPct {
		return fmt.Errorf("Config error: bhls: hedge_start_pct (%.2f) must be greater than hedge_stop_loss_pct (%.2f)", c.BHLS.HedgeStartPct, c.BHLS.HedgeStopLossPct)
	}
	if len(c.BHLS.HedgeLevelPercentages) != c.BHLS.MaxHedgeLevels {
		return fmt.Errorf("Config error: bhls: hedge_level_percentages item count (%d) must equal max_hedge_levels (%d)", len(c.BHLS.HedgeLevelPercentages), c.BHLS.MaxHedgeLevels)
	}
	var totalPercentage float64
	for _, p := range c.BHLS.HedgeLevelPercentages {
		totalPercentage += p
	}
	if totalPercentage > 2.0 { // Allow total to exceed 1, but should not be too large
		return fmt.Errorf("Config error: bhls: hedge_level_percentages total (%.2f) is too high, should typically be around 1.0", totalPercentage)
	}

	return nil
}

type EnvConfig struct {
	ApiKey    string
	ApiSecret string
	BaseURL   string
}

func LoadEnvConfig() *EnvConfig {
	return &EnvConfig{
		ApiKey:    os.Getenv("BINANCE_API_KEY"),
		ApiSecret: os.Getenv("BINANCE_SECRET_KEY"),
		BaseURL:   os.Getenv("BINANCE_TESTNET_BASE_URL"),
	}
}
