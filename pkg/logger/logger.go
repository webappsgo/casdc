// Package logger provides structured logging functionality for CASDC
// with configurable log levels and output destinations
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	// DEBUG provides detailed information for troubleshooting
	DEBUG LogLevel = iota
	// INFO provides informational messages about normal operations
	INFO
	// WARN indicates potentially harmful situations
	WARN
	// ERROR indicates error events that might still allow the application to continue
	ERROR
	// FATAL indicates severe errors that will cause the application to abort
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

// Logger provides structured logging with multiple output destinations
type Logger struct {
	mu          sync.RWMutex
	level       LogLevel
	debugMode   bool
	outputs     []io.Writer
	fileOutput  *os.File
	errorOutput *os.File
	useColor    bool
	useEmoji    bool
}

// New creates a new logger instance with the specified debug mode
func New(debug bool) *Logger {
	logger := &Logger{
		level:       INFO,
		debugMode:   debug,
		outputs:     []io.Writer{os.Stdout},
		useColor:    isTerminal(),
		useEmoji:    isTerminal(),
	}

	if debug {
		logger.level = DEBUG
	}

	return logger
}

// NewWithConfig creates a new logger with specific configuration
func NewWithConfig(level string, logFile string, errorFile string) (*Logger, error) {
	logger := &Logger{
		outputs:  []io.Writer{os.Stdout},
		useColor: isTerminal(),
		useEmoji: isTerminal(),
	}

	// Set log level from string
	switch strings.ToLower(level) {
	case "debug":
		logger.level = DEBUG
		logger.debugMode = true
	case "info":
		logger.level = INFO
	case "warn":
		logger.level = WARN
	case "error":
		logger.level = ERROR
	case "fatal":
		logger.level = FATAL
	default:
		logger.level = WARN
	}

	// Open log file if specified
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		logger.fileOutput = file
		logger.outputs = append(logger.outputs, file)
	}

	// Open error log file if specified
	if errorFile != "" {
		file, err := os.OpenFile(errorFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open error log file: %w", err)
		}
		logger.errorOutput = file
	}

	return logger, nil
}

// SetLevel sets the minimum log level for output
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// SetOutput adds an output destination for log messages
func (l *Logger) AddOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outputs = append(l.outputs, w)
}

// DisableEmoji disables emoji in log output
func (l *Logger) DisableEmoji() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.useEmoji = false
}

// DisableColor disables color in log output
func (l *Logger) DisableColor() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.useColor = false
}

// Close closes any open file handles
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.fileOutput != nil {
		l.fileOutput.Close()
	}
	if l.errorOutput != nil {
		l.errorOutput.Close()
	}
}

// log writes a log message at the specified level
func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if level < l.level {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)

	// Get caller information for debug mode
	caller := ""
	if l.debugMode && level <= DEBUG {
		_, file, line, ok := runtime.Caller(2)
		if ok {
			parts := strings.Split(file, "/")
			if len(parts) > 0 {
				file = parts[len(parts)-1]
			}
			caller = fmt.Sprintf(" [%s:%d]", file, line)
		}
	}

	// Format the log entry based on output type
	var logEntry string
	if l.useColor && l.useEmoji && level >= INFO {
		logEntry = fmt.Sprintf("%s %s%s %s\n",
			timestamp,
			l.getLevelEmoji(level),
			caller,
			message)
	} else {
		logEntry = fmt.Sprintf("%s [%s]%s %s\n",
			timestamp,
			level.String(),
			caller,
			message)
	}

	// Write to all outputs
	for _, output := range l.outputs {
		io.WriteString(output, logEntry)
	}

	// Also write to error output for ERROR and FATAL levels
	if level >= ERROR && l.errorOutput != nil {
		io.WriteString(l.errorOutput, logEntry)
	}

	// Exit on FATAL
	if level == FATAL {
		os.Exit(1)
	}
}

// getLevelEmoji returns an emoji representation for the log level
func (l *Logger) getLevelEmoji(level LogLevel) string {
	switch level {
	case DEBUG:
		return "🐛"
	case INFO:
		return "ℹ️ "
	case WARN:
		return "⚠️ "
	case ERROR:
		return "❌"
	case FATAL:
		return "💀"
	default:
		return "📝"
	}
}

// Debug logs a debug message
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(DEBUG, format, args...)
}

// Info logs an informational message
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(INFO, format, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(WARN, format, args...)
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(ERROR, format, args...)
}

// Fatal logs a fatal error message and exits the program
func (l *Logger) Fatal(format string, args ...interface{}) {
	l.log(FATAL, format, args...)
}

// isTerminal checks if output is a terminal
func isTerminal() bool {
	fileInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// StandardLogger returns a standard library log.Logger that writes to this logger
func (l *Logger) StandardLogger(level LogLevel) *log.Logger {
	return log.New(logWriter{logger: l, level: level}, "", 0)
}

// logWriter adapts our logger to io.Writer interface
type logWriter struct {
	logger *Logger
	level  LogLevel
}

// Write implements io.Writer interface
func (lw logWriter) Write(p []byte) (n int, err error) {
	message := strings.TrimRight(string(p), "\n")
	lw.logger.log(lw.level, message)
	return len(p), nil
}