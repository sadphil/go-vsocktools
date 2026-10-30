// Package logging is the logging layer shared by the vsock tools.
//
// Every line goes to a single writer (stderr by default) and has the form
//
//	2026/10/30 20:23:01.123456 vsock-inetd[1234]: INFO  [#3 cid=2:1035] accepted connection
//
// Loggers derived with With share the underlying writer and add a context
// prefix, so every message about one client carries the same tag.
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
)

// Level selects which messages are emitted.
type Level int

const (
	LevelWarn  Level = iota // warnings and errors only
	LevelInfo               // plus connection lifecycle messages
	LevelDebug              // plus every read/write with its size
)

// Logger writes leveled messages. It is safe for concurrent use.
type Logger struct {
	out    *log.Logger
	head   string // "program[pid]: "
	prefix string // context added by With
	level  Level
}

// New returns a Logger writing to w that emits messages up to level.
func New(w io.Writer, program string, level Level) *Logger {
	return &Logger{
		out:   log.New(w, "", log.LstdFlags|log.Lmicroseconds),
		head:  fmt.Sprintf("%s[%d]: ", program, os.Getpid()),
		level: level,
	}
}

// With returns a Logger that prefixes every message with "[<context>] ".
func (l *Logger) With(format string, args ...any) *Logger {
	c := *l
	c.prefix = l.prefix + "[" + fmt.Sprintf(format, args...) + "] "
	return &c
}

// Level returns the logger's level.
func (l *Logger) Level() Level { return l.level }

func (l *Logger) emit(level, format string, args []any) {
	l.out.Print(l.head + level + " " + l.prefix + fmt.Sprintf(format, args...))
}

// Debugf logs a message at LevelDebug.
func (l *Logger) Debugf(format string, args ...any) {
	if l.level >= LevelDebug {
		l.emit("DEBUG", format, args)
	}
}

// Infof logs an informational message at LevelInfo.
func (l *Logger) Infof(format string, args ...any) {
	if l.level >= LevelInfo {
		l.emit("INFO ", format, args)
	}
}

// Warnf logs a warning.
func (l *Logger) Warnf(format string, args ...any) { l.emit("WARN ", format, args) }

// Errorf logs an error.
func (l *Logger) Errorf(format string, args ...any) { l.emit("ERROR", format, args) }

// Fatalf logs an error and exits with status 1.
func (l *Logger) Fatalf(format string, args ...any) {
	l.emit("FATAL", format, args)
	os.Exit(1)
}
