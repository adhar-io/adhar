package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LogLevel represents the logging level
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

// String returns the string representation of the log level
func (l LogLevel) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	case FATAL:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// AdharLogger represents the main logger instance
type AdharLogger struct {
	Logger    *log.Logger
	Level     LogLevel
	Fields    map[string]interface{}
	Output    io.Writer
	Formatter LogFormatter
}

// LogFormatter defines the interface for log formatting
type LogFormatter interface {
	Format(level LogLevel, message string, fields map[string]interface{}) string
}

// DefaultFormatter provides default log formatting
type DefaultFormatter struct{}

// Format formats the log message with timestamp, level, and fields
func (f *DefaultFormatter) Format(level LogLevel, message string, fields map[string]interface{}) string {
	timestamp := time.Now().Format("2006-01-02T15:04:05.000Z07:00")

	var fieldStr string
	if len(fields) > 0 {
		var pairs []string
		for k, v := range fields {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
		}
		fieldStr = " " + strings.Join(pairs, " ")
	}

	return fmt.Sprintf("[%s] %s: %s%s", timestamp, level.String(), message, fieldStr)
}

// JSONFormatter provides JSON log formatting
type JSONFormatter struct{}

// Format formats the log message as JSON
func (f *JSONFormatter) Format(level LogLevel, message string, fields map[string]interface{}) string {
	// Simple JSON formatting - in production, use a proper JSON library
	json := fmt.Sprintf(`{"timestamp":"%s","level":"%s","message":"%s"`,
		time.Now().Format("2006-01-02T15:04:05.000Z07:00"),
		level.String(),
		message)

	if len(fields) > 0 {
		for k, v := range fields {
			json += fmt.Sprintf(`,"%s":"%v"`, k, v)
		}
	}
	json += "}"
	return json
}

// Global logger instance
var globalLogger *AdharLogger

// Emoji constants for better visual feedback
const (
	EmojiInfo     = "ℹ️"
	EmojiSuccess  = "✅"
	EmojiWarning  = "⚠️"
	EmojiError    = "❌"
	EmojiDebug    = "🔍"
	EmojiSecurity = "🔒"
	EmojiNetwork  = "🌐"
	EmojiCluster  = "🏗️"
	EmojiProvider = "☁️"
)

// Init initializes the global logger
func Init(config *LoggerConfig) {
	globalLogger = NewLogger(config)
}

// LoggerConfig holds logger configuration
type LoggerConfig struct {
	Level      LogLevel `json:"level"`
	Output     string   `json:"output"`     // "stdout", "stderr", or file path
	Format     string   `json:"format"`     // "text" or "json"
	MaxSize    int      `json:"maxSize"`    // Maximum size in MB before rotation
	MaxBackups int      `json:"maxBackups"` // Maximum number of old log files
	MaxAge     int      `json:"maxAge"`     // Maximum number of days to retain old log files
	Compress   bool     `json:"compress"`   // Whether to compress rotated log files
}

// DefaultConfig returns a default logger configuration
func DefaultConfig() *LoggerConfig {
	return &LoggerConfig{
		Level:      INFO,
		Output:     "stdout",
		Format:     "text",
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}
}

// NewLogger creates a new logger instance
func NewLogger(config *LoggerConfig) *AdharLogger {
	if config == nil {
		config = DefaultConfig()
	}

	// Set up output writer
	var output io.Writer
	switch config.Output {
	case "stdout":
		output = os.Stdout
	case "stderr":
		output = os.Stderr
	default:
		// File output with rotation
		output = &lumberjack.Logger{
			Filename:   config.Output,
			MaxSize:    config.MaxSize,
			MaxBackups: config.MaxBackups,
			MaxAge:     config.MaxAge,
			Compress:   config.Compress,
		}
	}

	// Set up formatter
	var formatter LogFormatter
	switch config.Format {
	case "json":
		formatter = &JSONFormatter{}
	default:
		formatter = &DefaultFormatter{}
	}

	logger := &AdharLogger{
		Logger:    log.New(output, "", 0),
		Level:     config.Level,
		Fields:    make(map[string]interface{}),
		Output:    output,
		Formatter: formatter,
	}

	return logger
}

// GetLogger returns the global logger instance
func GetLogger() *AdharLogger {
	if globalLogger == nil {
		globalLogger = NewLogger(DefaultConfig())
	}
	return globalLogger
}

// WithField adds a field to the logger context
func (l *AdharLogger) WithField(key string, value interface{}) *AdharLogger {
	newLogger := &AdharLogger{
		Logger:    l.Logger,
		Level:     l.Level,
		Fields:    make(map[string]interface{}),
		Output:    l.Output,
		Formatter: l.Formatter,
	}

	// Copy existing fields
	for k, v := range l.Fields {
		newLogger.Fields[k] = v
	}

	// Add new field
	newLogger.Fields[key] = value
	return newLogger
}

// WithFields adds multiple fields to the logger context
func (l *AdharLogger) WithFields(fields map[string]interface{}) *AdharLogger {
	newLogger := &AdharLogger{
		Logger:    l.Logger,
		Level:     l.Level,
		Fields:    make(map[string]interface{}),
		Output:    l.Output,
		Formatter: l.Formatter,
	}

	// Copy existing fields
	for k, v := range l.Fields {
		newLogger.Fields[k] = v
	}

	// Add new fields
	for k, v := range fields {
		newLogger.Fields[k] = v
	}

	return newLogger
}

// logMessage logs a message at the specified level
func (l *AdharLogger) logMessage(level LogLevel, message string) {
	if level < l.Level {
		return
	}

	formattedMessage := l.Formatter.Format(level, message, l.Fields)
	l.Logger.Println(formattedMessage)
}

// Debug logs a debug message
func (l *AdharLogger) Debug(message string) {
	l.logMessage(DEBUG, message)
}

// Info logs an info message
func (l *AdharLogger) Info(message string) {
	l.logMessage(INFO, message)
}

// Warn logs a warning message
func (l *AdharLogger) Warn(message string) {
	l.logMessage(WARN, message)
}

// Error logs an error message
func (l *AdharLogger) Error(message string) {
	l.logMessage(ERROR, message)
}

// Fatal logs a fatal message and exits
func (l *AdharLogger) Fatal(message string) {
	l.logMessage(FATAL, message)
	os.Exit(1)
}

// Debugf logs a formatted debug message
func (l *AdharLogger) Debugf(format string, args ...interface{}) {
	l.Debug(fmt.Sprintf(format, args...))
}

// Infof logs a formatted info message
func (l *AdharLogger) Infof(format string, args ...interface{}) {
	l.Info(fmt.Sprintf(format, args...))
}

// Warnf logs a formatted warning message
func (l *AdharLogger) Warnf(format string, args ...interface{}) {
	l.Warn(fmt.Sprintf(format, args...))
}

// Errorf logs a formatted error message
func (l *AdharLogger) Errorf(format string, args ...interface{}) {
	l.Error(fmt.Sprintf(format, args...))
}

// Fatalf logs a formatted fatal message and exits
func (l *AdharLogger) Fatalf(format string, args ...interface{}) {
	l.Fatal(fmt.Sprintf(format, args...))
}

// Convenience functions for global logger
func Debug(message string) {
	GetLogger().Debug(message)
}

func Info(message string) {
	GetLogger().Info(message)
}

func Warn(message string) {
	GetLogger().Warn(message)
}

func Error(message string, err error, fields map[string]interface{}) {
	logger := GetLogger()
	if err != nil {
		if fields == nil {
			fields = make(map[string]interface{})
		}
		fields["error"] = err.Error()
	}
	if len(fields) > 0 {
		logger.WithFields(fields).Error(message)
	} else {
		logger.Error(message)
	}
}

func Fatal(message string) {
	GetLogger().Fatal(message)
}

func Debugf(format string, args ...interface{}) {
	GetLogger().Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	GetLogger().Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	GetLogger().Warnf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	GetLogger().Errorf(format, args...)
}

func Fatalf(format string, args ...interface{}) {
	GetLogger().Fatalf(format, args...)
}

// WithField adds a field to the global logger
func WithField(key string, value interface{}) *AdharLogger {
	return GetLogger().WithField(key, value)
}

// WithFields adds multiple fields to the global logger
func WithFields(fields map[string]interface{}) *AdharLogger {
	return GetLogger().WithFields(fields)
}

// Provider-specific loggers
func ForProvider(provider string) *AdharLogger {
	return GetLogger().WithField("provider", provider)
}

func ForCluster(cluster string) *AdharLogger {
	return GetLogger().WithField("cluster", cluster)
}

func ForEnvironment(env string) *AdharLogger {
	return GetLogger().WithField("environment", env)
}

// Security logging
func SecurityInfo(action, details string) {
	GetLogger().Info(fmt.Sprintf("%s %s: %s", EmojiSecurity, action, details))
}

func SecurityWarn(action, details string) {
	GetLogger().Warn(fmt.Sprintf("%s %s: %s", EmojiSecurity, action, details))
}

func SecurityError(action, details string) {
	GetLogger().Error(fmt.Sprintf("%s %s: %s", EmojiSecurity, action, details))
}

// Network logging
func NetworkInfo(action, details string) {
	GetLogger().Info(fmt.Sprintf("%s %s: %s", EmojiNetwork, action, details))
}

func NetworkWarn(action, details string) {
	GetLogger().Warn(fmt.Sprintf("%s %s: %s", EmojiNetwork, action, details))
}

func NetworkError(action, details string) {
	GetLogger().Error(fmt.Sprintf("%s %s: %s", EmojiNetwork, action, details))
}

// Cluster logging
func ClusterInfo(action, details string) {
	GetLogger().Info(fmt.Sprintf("%s %s: %s", EmojiCluster, action, details))
}

func ClusterWarn(action, details string) {
	GetLogger().Warn(fmt.Sprintf("%s %s: %s", EmojiCluster, action, details))
}

func ClusterError(action, details string) {
	GetLogger().Error(fmt.Sprintf("%s %s: %s", EmojiCluster, action, details))
}

// Provider logging
func ProviderInfo(provider, action, details string) {
	GetLogger().WithField("provider", provider).Info(fmt.Sprintf("%s %s: %s", EmojiProvider, action, details))
}

func ProviderWarn(provider, action, details string) {
	GetLogger().WithField("provider", provider).Warn(fmt.Sprintf("%s %s: %s", EmojiProvider, action, details))
}

func ProviderError(provider, action, details string) {
	GetLogger().WithField("provider", provider).Error(fmt.Sprintf("%s %s: %s", EmojiProvider, action, details))
}

// Success logging
func Success(message string) {
	entry := GetLogger().WithField("status", "success")
	entry.Info(fmt.Sprintf("%s %s", EmojiSuccess, message))
}

// Warning logging
func Warning(message string) {
	entry := GetLogger().WithField("status", "warning")
	entry.Warn(fmt.Sprintf("%s %s", EmojiWarning, message))
}

// Error logging with context
func ErrorWithContext(message string, err error) {
	fields := map[string]interface{}{
		"error": err.Error(),
	}

	if pc, file, line, ok := runtime.Caller(1); ok {
		fields["file"] = filepath.Base(file)
		fields["line"] = line
		fields["function"] = runtime.FuncForPC(pc).Name()
	}

	GetLogger().WithFields(fields).Error(message)
}

// Debug logging with fields
func DebugWithFields(message string, fields map[string]interface{}) {
	if len(fields) > 0 {
		GetLogger().WithFields(fields).Debug(fmt.Sprintf("%s %s", EmojiDebug, message))
	} else {
		GetLogger().Debug(fmt.Sprintf("%s %s", EmojiDebug, message))
	}
}

// Info logging with fields
func InfoWithFields(message string, fields map[string]interface{}) {
	if len(fields) > 0 {
		GetLogger().WithFields(fields).Info(fmt.Sprintf("%s %s", EmojiInfo, message))
	} else {
		GetLogger().Info(fmt.Sprintf("%s %s", EmojiInfo, message))
	}
}

// SetOutput sets the output for the global logger
func SetOutput(output io.Writer) {
	GetLogger().SetOutput(output)
}

// SetLevel sets the log level for the global logger
func SetLevel(level LogLevel) {
	GetLogger().Level = level
}

// SetOutput sets the output writer for the logger
func (l *AdharLogger) SetOutput(output io.Writer) {
	l.Logger.SetOutput(output)
	l.Output = output
}

// SetLevel sets the log level for the logger
func (l *AdharLogger) SetLevel(level LogLevel) {
	l.Level = level
}

// Message types for tea-based CLI communication
type StepMsg string
type StatusMsg string
type ExtraOutputMsg string
type DoneMsg struct{}
type ElapsedTimeMsg string
type ErrorMsg struct {
	Err error
}

// CLI configuration variables
var (
	CLILogLevel      string = "info"
	CLIColoredOutput bool   = true
)

// CLI flag descriptions
const (
	LogLevelMsg      = "Set the log level (debug, info, warn, error, fatal)"
	ColoredOutputMsg = "Enable colored output in logs"
)

// LogLevel string constants
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
	LogLevelFatal = "fatal"
)

// SetLogLevel sets the global log level from a string
func SetLogLevel(levelStr string) error {
	level, err := parseLogLevel(levelStr)
	if err != nil {
		return err
	}
	SetLevel(level)
	return nil
}

// parseLogLevel converts a string to LogLevel
func parseLogLevel(levelStr string) (LogLevel, error) {
	switch strings.ToLower(levelStr) {
	case "debug":
		return DEBUG, nil
	case "info":
		return INFO, nil
	case "warn", "warning":
		return WARN, nil
	case "error":
		return ERROR, nil
	case "fatal":
		return FATAL, nil
	default:
		return INFO, fmt.Errorf("invalid log level: %s", levelStr)
	}
}

// SetupKubernetesLogging configures logging for Kubernetes operations
func SetupKubernetesLogging() error {
	// Set up structured logging for Kubernetes operations
	config := DefaultConfig()

	// Use the CLI log level if set
	if CLILogLevel != "" {
		level, err := parseLogLevel(CLILogLevel)
		if err == nil {
			config.Level = level
		}
	}

	// Initialize global logger
	Init(config)

	return nil
}

// Banner prints a formatted banner message
func Banner(title, subtitle string) {
	logger := GetLogger()
	logger.Info(fmt.Sprintf("=== %s ===", title))
	if subtitle != "" {
		logger.Info(subtitle)
	}
}

// StartOperation logs the start of an operation
func (l *AdharLogger) StartOperation(operation, details string) {
	l.Info(fmt.Sprintf("🚀 Starting %s: %s", operation, details))
}

// FinishOperation logs the completion of an operation
func (l *AdharLogger) FinishOperation(operation, details string) {
	l.Info(fmt.Sprintf("✅ Completed %s: %s", operation, details))
}
