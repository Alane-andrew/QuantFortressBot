package main

import (
	"auto_bian_go_1/config"
	"auto_bian_go_1/logs"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/joho/godotenv"
)

var (
	orchestrator *Orchestrator
	runOnce      sync.Once
)

func main() {
	// --- New: Read config file path through command line flags ---
	configPath := flag.String("config", "config/config.yaml", "Path to the config.yaml file")
	flag.Parse()
	// --- End ---

	// Load .env file
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Note: .env file not found, will continue using system environment variables.")
	}

	// Load main configuration file
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Fatal error: Unable to load config file '%s': %v\n", *configPath, err)
		return
	}

	// Load environment variables (API keys, etc.)
	envCfg := config.LoadEnvConfig()

	// --- Modified: Use directories from config ---
	symbolUpper := strings.ToUpper(cfg.Symbol)
	logFilename := fmt.Sprintf("%s/%s_bot.log", cfg.Normal.LogDirectory, symbolUpper)
	stateFilename := fmt.Sprintf("%s/%s_state.json", cfg.Normal.StateDirectory, symbolUpper)
	// --- End ---

	// Initialize logging system
	if err := logs.Init(cfg.Logs, logFilename); err != nil {
		fmt.Printf("Fatal error: Failed to initialize logging system: %v\n", err)
		return
	}
	defer logs.Close()

	logs.Infof("Configuration loaded successfully, logs will be written to: %s", logFilename)

	// Create and start the main business orchestrator
	// State manager is now created internally by Orchestrator
	orchestrator, err := NewOrchestrator(cfg, envCfg, stateFilename)
	if err != nil {
		logs.Fatalf("Failed to initialize Orchestrator: %v", err)
	}
	orchestrator.Start()

	// Wait for and handle program termination signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// Execute graceful shutdown
	orchestrator.Stop()
}
