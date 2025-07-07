package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

// LogLevel represents different log levels
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// CLI integration variables (moved from helpers)
var (
	CLILogLevel      string
	LogLevelMsg      = "Set the log verbosity. Supported values are: debug, info, warn, and error."
	CmdLogger        logr.Logger
	CLIColoredOutput bool
	ColoredOutputMsg = "Enable colored log messages."
)

// AdharLogger is our enhanced logger with beautiful formatting
type AdharLogger struct {
	*logrus.Logger
	mu              sync.RWMutex
	progressSpinner *ProgressSpinner
	isJSON          bool
	noColor         bool
}

// ProgressSpinner handles progress indicators
type ProgressSpinner struct {
	mu       sync.Mutex
	active   bool
	message  string
	frames   []string
	current  int
	stopChan chan bool
}

// Global logger instance
var (
	globalLogger *AdharLogger
	once         sync.Once
)

// Color and emoji constants for better UX
var (
	// Emojis for different log levels and operations
	EmojiInfo     = "💡"
	EmojiSuccess  = "✅"
	EmojiWarning  = "⚠️"
	EmojiError    = "❌"
	EmojiDebug    = "🔍"
	EmojiProgress = "⏳"
	EmojiStarted  = "🚀"
	EmojiFinished = "🎉"
	EmojiCluster  = "🏗️"
	EmojiProvider = "☁️"
	EmojiNetwork  = "🔗"
	EmojiSecurity = "🔒"
	EmojiConfig   = "⚙️"
	EmojiManifest = "📋"
	EmojiValidate = "🔎"
	EmojiCleanup  = "🧹"

	// Colors for different components
	ColorTimestamp = color.New(color.FgHiBlack)
	ColorLevel     = color.New(color.FgWhite, color.Bold)
	ColorInfo      = color.New(color.FgCyan)
	ColorSuccess   = color.New(color.FgGreen, color.Bold)
	ColorWarning   = color.New(color.FgYellow)
	ColorError     = color.New(color.FgRed, color.Bold)
	ColorDebug     = color.New(color.FgMagenta)
	ColorField     = color.New(color.FgHiBlue)
	ColorValue     = color.New(color.FgWhite)
	ColorProvider  = color.New(color.FgCyan, color.Bold)
	ColorCluster   = color.New(color.FgGreen)
)

// GetLogger returns the global logger instance
func GetLogger() *AdharLogger {
	once.Do(func() {
		globalLogger = NewLogger()
	})
	return globalLogger
}

// NewLogger creates a new enhanced logger
func NewLogger() *AdharLogger {
	baseLogger := logrus.New()

	logger := &AdharLogger{
		Logger: baseLogger,
		progressSpinner: &ProgressSpinner{
			frames:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
			stopChan: make(chan bool),
		},
		noColor: os.Getenv("NO_COLOR") != "" || os.Getenv("ADHAR_NO_COLOR") != "",
	}

	// Configure the base logger
	logger.configureLogger()

	return logger
}

// configureLogger sets up the base logger configuration
func (l *AdharLogger) configureLogger() {
	// Create logs directory if it doesn't exist
	logsDir := ".adhar/logs"
	if err := os.MkdirAll(logsDir, 0755); err == nil {
		// Set up file logging with rotation
		l.Logger.SetOutput(&lumberjack.Logger{
			Filename:   filepath.Join(logsDir, "adhar.log"),
			MaxSize:    50, // MB
			MaxBackups: 5,  // Keep 5 old files
			MaxAge:     30, // Days
			Compress:   true,
		})
	}

	// Set default level
	l.Logger.SetLevel(logrus.InfoLevel)

	// Use our custom formatter
	l.Logger.SetFormatter(&AdharFormatter{
		noColor: l.noColor,
	})
}

// AdharFormatter is our custom formatter with beautiful output
type AdharFormatter struct {
	noColor bool
}

// Format formats the log entry with colors and emojis
func (f *AdharFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("15:04:05")

	var emoji, levelColor, messageColor string
	if !f.noColor {
		switch entry.Level {
		case logrus.DebugLevel:
			emoji = EmojiDebug
			levelColor = ColorDebug.Sprint("DEBUG")
			messageColor = ColorDebug.Sprint(entry.Message)
		case logrus.InfoLevel:
			emoji = EmojiInfo
			levelColor = ColorInfo.Sprint("INFO ")
			messageColor = ColorInfo.Sprint(entry.Message)
		case logrus.WarnLevel:
			emoji = EmojiWarning
			levelColor = ColorWarning.Sprint("WARN ")
			messageColor = ColorWarning.Sprint(entry.Message)
		case logrus.ErrorLevel:
			emoji = EmojiError
			levelColor = ColorError.Sprint("ERROR")
			messageColor = ColorError.Sprint(entry.Message)
		default:
			emoji = EmojiInfo
			levelColor = ColorInfo.Sprint("INFO ")
			messageColor = ColorInfo.Sprint(entry.Message)
		}

		timestamp = ColorTimestamp.Sprint(timestamp)
	} else {
		// No color mode
		switch entry.Level {
		case logrus.DebugLevel:
			levelColor = "DEBUG"
		case logrus.InfoLevel:
			levelColor = "INFO "
		case logrus.WarnLevel:
			levelColor = "WARN "
		case logrus.ErrorLevel:
			levelColor = "ERROR"
		default:
			levelColor = "INFO "
		}
		messageColor = entry.Message
	}

	// Build the log line
	var result strings.Builder
	result.WriteString(fmt.Sprintf("%s %s [%s] %s",
		emoji, timestamp, levelColor, messageColor))

	// Add fields if present
	if len(entry.Data) > 0 {
		result.WriteString(" ")
		f.formatFields(&result, entry.Data)
	}

	result.WriteString("\n")
	return []byte(result.String()), nil
}

// formatFields formats log fields with colors
func (f *AdharFormatter) formatFields(result *strings.Builder, fields logrus.Fields) {
	var parts []string

	for key, value := range fields {
		var fieldStr string
		if !f.noColor {
			fieldStr = fmt.Sprintf("%s=%s",
				ColorField.Sprint(key),
				ColorValue.Sprint(fmt.Sprintf("%v", value)))
		} else {
			fieldStr = fmt.Sprintf("%s=%v", key, value)
		}
		parts = append(parts, fieldStr)
	}

	result.WriteString(strings.Join(parts, " "))
}

// Enhanced logging methods with context
func (l *AdharLogger) WithProvider(provider string) *logrus.Entry {
	return l.WithField("provider", provider)
}

func (l *AdharLogger) WithCluster(cluster string) *logrus.Entry {
	return l.WithField("cluster", cluster)
}

func (l *AdharLogger) WithEnvironment(env string) *logrus.Entry {
	return l.WithField("environment", env)
}

func (l *AdharLogger) WithService(service string) *logrus.Entry {
	return l.WithField("service", service)
}

func (l *AdharLogger) WithOperation(operation string) *logrus.Entry {
	return l.WithField("operation", operation)
}

// Specialized logging methods for common Adhar operations
func (l *AdharLogger) StartOperation(operation, details string) {
	if !l.noColor {
		msg := fmt.Sprintf("%s Starting %s", EmojiStarted, operation)
		if details != "" {
			msg += fmt.Sprintf(": %s", details)
		}
		l.Info(msg)
	} else {
		l.WithField("operation", operation).Info("Starting operation: " + details)
	}
}

func (l *AdharLogger) FinishOperation(operation, details string) {
	if !l.noColor {
		msg := fmt.Sprintf("%s Completed %s", EmojiFinished, operation)
		if details != "" {
			msg += fmt.Sprintf(": %s", details)
		}
		l.Info(msg)
	} else {
		l.WithField("operation", operation).Info("Completed operation: " + details)
	}
}

func (l *AdharLogger) ProvisioningInfo(provider, action, target string) {
	emoji := EmojiProvider
	switch action {
	case "creating", "provisioning":
		emoji = EmojiCluster
	case "validating":
		emoji = EmojiValidate
	case "configuring":
		emoji = EmojiConfig
	case "installing":
		emoji = EmojiManifest
	}

	l.WithProvider(provider).Info(fmt.Sprintf("%s %s %s", emoji, action, target))
}

func (l *AdharLogger) SecurityInfo(action, details string) {
	l.Info(fmt.Sprintf("%s %s: %s", EmojiSecurity, action, details))
}

func (l *AdharLogger) NetworkInfo(action, details string) {
	l.Info(fmt.Sprintf("%s %s: %s", EmojiNetwork, action, details))
}

func (l *AdharLogger) ValidationInfo(item, status string) {
	emoji := EmojiValidate
	if status == "passed" || status == "ok" || status == "ready" {
		emoji = EmojiSuccess
	} else if status == "warning" {
		emoji = EmojiWarning
	} else if status == "failed" || status == "error" {
		emoji = EmojiError
	}

	l.Info(fmt.Sprintf("%s Validation %s: %s", emoji, item, status))
}

func (l *AdharLogger) ManifestInfo(action, manifest string) {
	l.WithField("manifest", manifest).Info(fmt.Sprintf("%s %s manifest", EmojiManifest, action))
}

func (l *AdharLogger) CleanupInfo(resource string) {
	l.WithField("resource", resource).Info(fmt.Sprintf("%s Cleaning up %s", EmojiCleanup, resource))
}

// Progress methods
func (l *AdharLogger) StartProgress(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.progressSpinner.active {
		l.StopProgress()
	}

	l.progressSpinner.message = message
	l.progressSpinner.active = true
	l.progressSpinner.current = 0

	go l.runSpinner()
}

func (l *AdharLogger) StopProgress() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.progressSpinner.active {
		return
	}

	l.progressSpinner.active = false
	select {
	case l.progressSpinner.stopChan <- true:
	default:
	}

	// Clear the spinner line
	fmt.Print("\r\033[K")
}

func (l *AdharLogger) runSpinner() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-l.progressSpinner.stopChan:
			return
		case <-ticker.C:
			l.progressSpinner.mu.Lock()
			if !l.progressSpinner.active {
				l.progressSpinner.mu.Unlock()
				return
			}

			frame := l.progressSpinner.frames[l.progressSpinner.current]
			l.progressSpinner.current = (l.progressSpinner.current + 1) % len(l.progressSpinner.frames)

			if !l.noColor {
				fmt.Printf("\r%s %s %s",
					EmojiProgress,
					ColorInfo.Sprint(frame),
					l.progressSpinner.message)
			} else {
				fmt.Printf("\r[%s] %s", frame, l.progressSpinner.message)
			}
			l.progressSpinner.mu.Unlock()
		}
	}
}

// SetLevel sets the log level
func (l *AdharLogger) SetLevel(level LogLevel) {
	switch level {
	case LogLevelDebug:
		l.Logger.SetLevel(logrus.DebugLevel)
	case LogLevelInfo:
		l.Logger.SetLevel(logrus.InfoLevel)
	case LogLevelWarn:
		l.Logger.SetLevel(logrus.WarnLevel)
	case LogLevelError:
		l.Logger.SetLevel(logrus.ErrorLevel)
	}
}

// SetOutput sets both console and file output
func (l *AdharLogger) SetOutput(writers ...io.Writer) {
	if len(writers) == 1 {
		l.Logger.SetOutput(writers[0])
	} else if len(writers) > 1 {
		l.Logger.SetOutput(io.MultiWriter(writers...))
	}
}

// EnableJSONMode enables JSON formatting for machine-readable logs
func (l *AdharLogger) EnableJSONMode() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.isJSON = true
	l.Logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
		PrettyPrint:     false,
	})
}

// DisableColor disables color output
func (l *AdharLogger) DisableColor() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.noColor = true
	l.Logger.SetFormatter(&AdharFormatter{noColor: true})
}

// Context-aware logging helpers
func (l *AdharLogger) WithContext(ctx context.Context) *logrus.Entry {
	entry := l.WithField("timestamp", time.Now())

	// Add context values if they exist
	if value := ctx.Value("operation"); value != nil {
		entry = entry.WithField("operation", value)
	}
	if value := ctx.Value("provider"); value != nil {
		entry = entry.WithField("provider", value)
	}
	if value := ctx.Value("cluster"); value != nil {
		entry = entry.WithField("cluster", value)
	}

	return entry
}

// Helper functions for common patterns
func Success(message string, fields ...logrus.Fields) {
	entry := GetLogger().WithField("status", "success")
	if len(fields) > 0 {
		entry = entry.WithFields(fields[0])
	}
	entry.Info(fmt.Sprintf("%s %s", EmojiSuccess, message))
}

func Warning(message string, fields ...logrus.Fields) {
	entry := GetLogger().WithField("status", "warning")
	if len(fields) > 0 {
		entry = entry.WithFields(fields[0])
	}
	entry.Warn(fmt.Sprintf("%s %s", EmojiWarning, message))
}

func Error(message string, err error, fields ...logrus.Fields) {
	entry := GetLogger().WithField("status", "error")
	if err != nil {
		entry = entry.WithError(err)
	}
	if len(fields) > 0 {
		entry = entry.WithFields(fields[0])
	}
	entry.Error(fmt.Sprintf("%s %s", EmojiError, message))
}

func Debug(message string, fields ...logrus.Fields) {
	if len(fields) > 0 {
		GetLogger().WithFields(fields[0]).Debug(fmt.Sprintf("%s %s", EmojiDebug, message))
	} else {
		GetLogger().Debug(fmt.Sprintf("%s %s", EmojiDebug, message))
	}
}

func Info(message string, fields ...logrus.Fields) {
	if len(fields) > 0 {
		GetLogger().WithFields(fields[0]).Info(fmt.Sprintf("%s %s", EmojiInfo, message))
	} else {
		GetLogger().Info(fmt.Sprintf("%s %s", EmojiInfo, message))
	}
}

// Banner displays a beautiful banner with title and subtitle
func Banner(title, subtitle string) {
	if noColor := os.Getenv("NO_COLOR"); noColor != "" {
		fmt.Printf("=== %s ===\n%s\n\n", title, subtitle)
		return
	}

	// Create a beautiful banner
	titleLen := len(title)
	subtitleLen := len(subtitle)
	maxLen := titleLen
	if subtitleLen > maxLen {
		maxLen = subtitleLen
	}

	// Add padding
	width := maxLen + 4
	if width < 50 {
		width = 50
	}

	// Top border
	fmt.Printf("┌%s┐\n", strings.Repeat("─", width-2))

	// Title
	titlePadding := (width - 2 - titleLen) / 2
	titleLine := fmt.Sprintf("│%s%s%s│",
		strings.Repeat(" ", titlePadding),
		title,
		strings.Repeat(" ", width-2-titleLen-titlePadding))
	fmt.Printf("%s\n", ColorInfo.Sprint(titleLine))

	// Subtitle
	subtitlePadding := (width - 2 - subtitleLen) / 2
	subtitleLine := fmt.Sprintf("│%s%s%s│",
		strings.Repeat(" ", subtitlePadding),
		subtitle,
		strings.Repeat(" ", width-2-subtitleLen-subtitlePadding))
	fmt.Printf("%s\n", ColorField.Sprint(subtitleLine))

	// Bottom border
	fmt.Printf("└%s┘\n\n", strings.Repeat("─", width-2))
}

// Message types for Bubble Tea UI (used in down command)
type StepMsg string
type StatusMsg string
type ExtraOutputMsg string
type DoneMsg struct{}
type ElapsedTimeMsg string
type ErrorMsg struct {
	Err error
}

// Legacy compatibility - maintain the old interface while using new logger
var (
	Logger        = GetLogger().Logger
	ColoredOutput = struct {
		Info    func(format string, a ...interface{})
		Warn    func(format string, a ...interface{})
		Error   func(format string, a ...interface{})
		Success func(format string, a ...interface{})
	}{
		Info:    ColorInfo.PrintfFunc(),
		Warn:    ColorWarning.PrintfFunc(),
		Error:   ColorError.PrintfFunc(),
		Success: ColorSuccess.PrintfFunc(),
	}
)

// SetLogLevel for backward compatibility
func SetLogLevel(level string) error {
	var logLevel LogLevel
	switch strings.ToLower(level) {
	case "debug":
		logLevel = LogLevelDebug
	case "info":
		logLevel = LogLevelInfo
	case "warn", "warning":
		logLevel = LogLevelWarn
	case "error":
		logLevel = LogLevelError
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}

	GetLogger().SetLevel(logLevel)
	return nil
}

// WithFields for backward compatibility
func WithFields(fields logrus.Fields) *logrus.Entry {
	return GetLogger().WithFields(fields)
}

// LogToFile for backward compatibility
func LogToFile(filename string) {
	GetLogger().SetOutput(&lumberjack.Logger{
		Filename:   filename,
		MaxSize:    10,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	})
}

// SetupKubernetesLogging sets up logging for controller-runtime and klog
func SetupKubernetesLogging() error {
	l, err := getSlogLevel(CLILogLevel)
	if err != nil {
		return err
	}

	// Get the enhanced Adhar logger
	adharLogger := GetLogger()

	// Set the log level on the enhanced logger
	switch l {
	case slog.LevelDebug:
		adharLogger.SetLevel(LogLevelDebug)
	case slog.LevelInfo:
		adharLogger.SetLevel(LogLevelInfo)
	case slog.LevelWarn:
		adharLogger.SetLevel(LogLevelWarn)
	case slog.LevelError:
		adharLogger.SetLevel(LogLevelError)
	}

	// Configure colored output
	if !CLIColoredOutput {
		adharLogger.DisableColor()
	}

	// Create slog handlers for controller-runtime and klog
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
	kslogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: getKlogLevel(l)}))

	logger := logr.FromSlogHandler(slogger.Handler())
	klogger := logr.FromSlogHandler(kslogger.Handler())

	klog.SetLogger(klogger)
	ctrl.SetLogger(logger)
	CmdLogger = logger
	return nil
}

// getSlogLevel converts string level to slog.Level
func getSlogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelDebug, fmt.Errorf("%s is not a valid log level", s)
	}
}

// getKlogLevel adjusts log level for klog (end users don't need verbose klog messages)
func getKlogLevel(l slog.Level) slog.Level {
	if l < slog.LevelInfo {
		return l
	}
	return slog.LevelError
}
