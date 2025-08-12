package logs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"auto_bian_go_1/config"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

// FileHook is a custom Logrus hook for writing logs to a rotated file.
// The type is now capitalized (FileHook) to avoid name conflicts.
type FileHook struct {
	formatter logrus.Formatter
	writer    io.Writer
}

// newFileHook creates a new hook for file logging.
func newFileHook(writer io.Writer, formatter logrus.Formatter) *FileHook {
	return &FileHook{
		writer:    writer,
		formatter: formatter,
	}
}

// Levels returns all log levels, so the hook is fired for all log entries.
func (h *FileHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

// Fire formats and writes the log entry to the file.
func (h *FileHook) Fire(entry *logrus.Entry) error {
	formattedBytes, err := h.formatter.Format(entry)
	if err != nil {
		return err
	}
	_, err = h.writer.Write(formattedBytes)
	return err
}

var (
	log              *logrus.Logger
	fileHookInstance *FileHook // This holds the hook instance so we can close its writer later.
)

// Init initializes the logging system.
func Init(cfg *config.LogConfig, logFilePath string) error {
	// 1. Create and configure OUR primary logger instance.
	log = logrus.New()
	parsedLevel, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		parsedLevel = logrus.InfoLevel
	}
	log.SetLevel(parsedLevel)

	// Configure CONSOLE output for our logger.
	log.SetFormatter(&logrus.TextFormatter{
		ForceColors:            true,
		FullTimestamp:          true,
		TimestampFormat:        "2006-01-02 15:04:05",
		DisableLevelTruncation: true,
		PadLevelText:           true,
	})
	log.SetOutput(os.Stdout)

	// 2. --- CRITICAL: Silence the global logrus instance ---
	// This ensures that any accidental direct calls to logrus.Info() or from
	// third-party libraries do not produce any output.
	logrus.SetOutput(io.Discard)
	logrus.StandardLogger().Hooks = make(logrus.LevelHooks)

	// 3. Configure FILE output for our logger instance via a hook.
	logDir := filepath.Dir(logFilePath)
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}

	// Use a separate, plain formatter for file logs.
	fileFormatter := &logrus.TextFormatter{
		DisableColors:   true,
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	}

	// Create the hook and add it ONLY to our logger instance.
	fileHookInstance = newFileHook(lumberjackLogger, fileFormatter)
	log.AddHook(fileHookInstance)
	// We no longer add the hook to the global instance.

	Infof("Logging system initialized.")
	return nil
}

// Close closes the file hook's underlying writer.
func Close() {
	if fileHookInstance != nil {
		if closer, ok := fileHookInstance.writer.(io.Closer); ok {
			closer.Close()
		}
	}
	Info("Logging system closed.")
}

// Wrapper functions to expose the logger.
func Debug(args ...interface{})                 { log.Debug(args...) }
func Debugf(format string, args ...interface{}) { log.Debugf(format, args...) }
func Info(args ...interface{})                  { log.Info(args...) }
func Infof(format string, args ...interface{})  { log.Infof(format, args...) }
func Warn(args ...interface{})                  { log.Warn(args...) }
func Warnf(format string, args ...interface{})  { log.Warnf(format, args...) }
func Error(args ...interface{})                 { log.Error(args...) }
func Errorf(format string, args ...interface{}) { log.Errorf(format, args...) }
func Fatal(args ...interface{})                 { log.Fatal(args...) }
func Fatalf(format string, args ...interface{}) { log.Fatalf(format, args...) }
