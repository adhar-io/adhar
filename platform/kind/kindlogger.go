package kind

import (
	"fmt"
	"strings" // Added import

	"github.com/go-logr/logr"
	kindlog "sigs.k8s.io/kind/pkg/log"
)

// this is a wrapper of logr.Logger made specifically for kind' logger.
// this is needed because kind's implementation is internal.
// https://github.com/kubernetes-sigs/kind/blob/1a8f0473a0785e0975e26739524513e8ee696be3/pkg/log/types.go
type kindLogger struct {
	cliLogger *logr.Logger
}

func (l *kindLogger) Warn(message string) {
	l.cliLogger.Info(message)
}

func (l *kindLogger) Warnf(message string, args ...interface{}) {
	l.cliLogger.Info(fmt.Sprintf(message, args...))
}

func (l *kindLogger) Error(message string) {
	l.cliLogger.Error(fmt.Errorf("%s", message), "")
}

func (l *kindLogger) Errorf(message string, args ...interface{}) {
	msg := fmt.Sprintf(message, args...)
	l.cliLogger.Error(fmt.Errorf("%s", msg), "")
}

func (l *kindLogger) V(level kindlog.Level) kindlog.InfoLogger {
	return newKindInfoLogger(l.cliLogger, int(level))
}

// KindLoggerFromLogr is a wrapper of logr.Logger made specifically for kind's InfoLogger.
// https://github.com/kubernetes-sigs/kind/blob/1a8f0473a0785e0975e26739524513e8ee696be3/pkg/log/types.go
func KindLoggerFromLogr(logrLogger *logr.Logger) *kindLogger {
	return &kindLogger{
		cliLogger: logrLogger,
	}
}

func newKindInfoLogger(logrLogger *logr.Logger, level int) *kindInfoLogger {
	return &kindInfoLogger{
		cliLogger: logrLogger,
		level:     level, // Don't push log level down - respect the original level
	}
}

type kindInfoLogger struct {
	cliLogger *logr.Logger
	level     int
}

func (k *kindInfoLogger) Info(message string) {
	// For level 0 (normal info), use Info() directly. For higher levels, use V()
	if k.level == 0 {
		k.cliLogger.Info(strings.TrimSpace(message))
	} else {
		k.cliLogger.V(k.level).Info(strings.TrimSpace(message))
	}
}

func (k *kindInfoLogger) Infof(message string, args ...interface{}) {
	// For level 0 (normal info), use Info() directly. For higher levels, use V()
	if k.level == 0 {
		k.cliLogger.Info(strings.TrimSpace(fmt.Sprintf(message, args...)))
	} else {
		k.cliLogger.V(k.level).Info(strings.TrimSpace(fmt.Sprintf(message, args...)))
	}
}

func (k *kindInfoLogger) Enabled() bool {
	return k.cliLogger.Enabled()
}
