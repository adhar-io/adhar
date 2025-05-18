package logger

import (
	"time"

	"github.com/fatih/color"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Centralized type definitions to avoid redeclaration issues.
type StatusMsg string
type ErrorMsg struct{ Err error }
type DoneMsg struct{}
type ElapsedTimeMsg time.Time
type StepMsg string
type ExtraOutputMsg string

// Logger is the standard logger instance for the platform
var Logger = logrus.New()

// ColoredOutput provides a set of functions for colored terminal output
var ColoredOutput = struct {
	Info    func(format string, a ...interface{})
	Warn    func(format string, a ...interface{})
	Error   func(format string, a ...interface{})
	Success func(format string, a ...interface{})
}{
	Info:    color.New(color.FgCyan).PrintfFunc(),
	Warn:    color.New(color.FgYellow).PrintfFunc(),
	Error:   color.New(color.FgRed).PrintfFunc(),
	Success: color.New(color.FgGreen).PrintfFunc(),
}

func init() {
	// Set the output to a rotating log file
	Logger.SetOutput(&lumberjack.Logger{
		Filename:   "platform.log",
		MaxSize:    10,   // Max megabytes before log is rotated
		MaxBackups: 3,    // Max number of old log files to keep
		MaxAge:     28,   // Max number of days to retain old log files
		Compress:   true, // Compress the old log files
	})

	// Set the default log level to Info
	Logger.SetLevel(logrus.InfoLevel)

	// Set the log format to a readable text format with timestamps
	Logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.RFC3339,
	})
}

// SetLogLevel allows dynamic adjustment of the log level
func SetLogLevel(level string) error {
	parsedLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return err
	}
	Logger.SetLevel(parsedLevel)
	return nil
}

// WithFields creates a new log entry with additional fields
func WithFields(fields logrus.Fields) *logrus.Entry {
	return Logger.WithFields(fields)
}

// LogToFile switches the logger output to a specified file
func LogToFile(filename string) {
	Logger.SetOutput(&lumberjack.Logger{
		Filename:   filename,
		MaxSize:    10,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	})
}
