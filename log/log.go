// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package log implements a simple logging package. It defines a type, Logger,
// with methods for formatting output. It also has a predefined 'standard'
// Logger accessible through helper functions Print[f|ln], Fatal[f|ln], and
// Panic[f|ln], which are easier to use than creating a Logger manually.
// That logger writes to standard error and prints the date and time
// of each logged message.
// Every log message is output on a separate line: if the message being
// printed does not end in a newline, the logger will add one.
// The Fatal functions call os.Exit(1) after writing the log message.
// The Panic functions call panic after writing the log message.
package log

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// A Logger represents an active logging object that generates lines of
// output to an io.Writer. Each logging operation makes a single call to
// the Writer's Write method. A Logger can be used simultaneously from
// multiple goroutines; it guarantees to serialize access to the Writer.
type Logger struct {
	mu sync.Mutex // ensures atomic writes; protects the following fields

	contentPrefix string // prefix to write at the beginning of the conntent.

	prefix string // prefix to write at beginning of each line
	suffix string //suffix to wirte at the end of each line.
	flag   int    // properties
	buf    []byte // for accumulating text to write

	// level log level
	level Level

	// destination for output
	out io.Writer

	// isTerminal whether log is output to terminal.
	isTerminal int

	callDepth int
}

// New creates a new Logger. The out variable sets the
// destination to which log data will be written.
// The prefix appears at the beginning of each generated log line.
// The flag argument defines the logging properties.
func New(out io.Writer, prefix string, suffix string, flag int, level Level, isTerminal int) *Logger {
	return &Logger{
		out:        out,
		prefix:     prefix,
		suffix:     suffix,
		flag:       flag,
		level:      level,
		isTerminal: isTerminal,
		callDepth:  2,
	}
}

// IncrOneCallDepth call depth add one
func (l *Logger) IncrOneCallDepth() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.callDepth++
}

// SetCallDepth set whether log output is terminal
func (l *Logger) SetCallDepth(callDepth int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.callDepth = callDepth
}

// SetIsTerminal set whether log output is terminal
func (l *Logger) SetIsTerminal(isTerminal int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.isTerminal = isTerminal
}

// SetOutput sets the output destination for the logger.
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out = w
}

// Cheap integer to fixed-width decimal ASCII. Give a negative width to avoid zero-padding.
func itoa(buf *[]byte, i int, wid int) {
	// Assemble decimal in reverse order.
	var b [20]byte
	bp := len(b) - 1
	for i >= 10 || wid > 1 {
		wid--
		q := i / 10
		b[bp] = byte('0' + i - q*10)
		bp--
		i = q
	}
	// i < 10
	b[bp] = byte('0' + i)
	*buf = append(*buf, b[bp:]...)
}

// formatHeader writes log header to buf in following order:
//   * l.prefix (if it's not blank),
//   * date and/or time (if corresponding flags are provided),
//   * file and line number (if corresponding flags are provided).
func (l *Logger) formatHeader(buf *[]byte, t time.Time, file string, line int, level Level) {

	*buf = append(*buf, l.prefix...)

	if l.flag&LLevel != 0 {
		levelString := ""
		if l.isTerminal == IsTerminal {
			levelString = fmt.Sprintf("\x1b[%dm%s\x1b[0m", level.Color(), level.String())
		} else {
			levelString = fmt.Sprintf("%s", level.String())
		}

		*buf = append(*buf, levelString...)
		*buf = append(*buf, ' ')
	}

	if l.flag&(Ldate|Ltime|Lmicroseconds) != 0 {
		if l.flag&LUTC != 0 {
			t = t.UTC()
		}
		if l.flag&Ldate != 0 {
			year, month, day := t.Date()
			itoa(buf, year, 4)
			*buf = append(*buf, '/')
			itoa(buf, int(month), 2)
			*buf = append(*buf, '/')
			itoa(buf, day, 2)
			*buf = append(*buf, ' ')
		}
		if l.flag&(Ltime|Lmicroseconds) != 0 {
			hour, min, sec := t.Clock()
			itoa(buf, hour, 2)
			*buf = append(*buf, ':')
			itoa(buf, min, 2)
			*buf = append(*buf, ':')
			itoa(buf, sec, 2)
			if l.flag&Lmicroseconds != 0 {
				*buf = append(*buf, '.')
				itoa(buf, t.Nanosecond()/1e3, 6)
			}
			*buf = append(*buf, ' ')
		}
	}
	if l.flag&(Lshortfile|Llongfile) != 0 {
		if l.flag&Lshortfile != 0 {
			short := file
			for i := len(file) - 1; i > 0; i-- {
				if file[i] == '/' {
					short = file[i+1:]
					break
				}
			}
			file = short
		}
		*buf = append(*buf, file...)
		*buf = append(*buf, ':')
		itoa(buf, line, -1)
		*buf = append(*buf, ": "...)
	}
}

// Output writes the output for a logging event. The string s contains
// the text to print after the prefix specified by the flags of the
// Logger. A newline is appended if the last character of s is not
// already a newline. Calldepth is used to recover the PC and is
// provided for generality, although at the moment on all pre-defined
// paths it will be 2.
func (l *Logger) Output(calldepth int, s string, level Level) error {
	now := time.Now() // get this early.
	var file string
	var line int
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flag&(Lshortfile|Llongfile) != 0 {
		// Release lock while getting caller info - it's expensive.
		l.mu.Unlock()
		var ok bool
		_, file, line, ok = runtime.Caller(calldepth)
		if !ok {
			file = "???"
			line = 0
		}
		l.mu.Lock()
	}
	l.buf = l.buf[:0]
	l.formatHeader(&l.buf, now, file, line, level)

	if len(l.contentPrefix) > 0 {
		l.buf = append(l.buf, l.contentPrefix...)
	}

	sLen := len(s)
	if len(s) > 0 && s[len(s)-1] == '\n' {
		l.buf = append(l.buf, s[:len(s)-1]...)
		sLen--
	} else {
		l.buf = append(l.buf, s...)
	}

	if len(l.suffix) > 0 {
		if sLen < MaxContextLen {
			l.buf = append(l.buf, strings.Repeat(" ", MaxContextLen-sLen)...)
		}
		l.buf = append(l.buf, l.suffix...)
	}

	if len(l.buf) == 0 || l.buf[len(l.buf)-1] != '\n' {
		l.buf = append(l.buf, '\n')
	}

	_, err := l.out.Write(l.buf)
	return err
}

// Panic is equivalent to l.Print() followed by a call to panic().
func (l *Logger) Panic(v ...interface{}) {
	s := fmt.Sprint(v...)
	l.Output(l.callDepth, s, PanicLevel)
	panic(s)
}

// Panicf is equivalent to l.Printf() followed by a call to panic().
func (l *Logger) Panicf(format string, v ...interface{}) {
	s := fmt.Sprintf(format, v...)
	l.Output(l.callDepth, s, PanicLevel)
	panic(s)
}

// Panicln is equivalent to l.Println() followed by a call to panic().
func (l *Logger) Panicln(v ...interface{}) {
	s := fmt.Sprintln(v...)
	l.Output(l.callDepth, s, PanicLevel)
	panic(s)
}

// Fatal is equivalent to l.Print() followed by a call to os.Exit(1).
func (l *Logger) Fatal(v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprint(v...), FatalLevel)
	os.Exit(1)
}

// Fatalf is equivalent to l.Printf() followed by a call to os.Exit(1).
func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprintf(format, v...), FatalLevel)
	os.Exit(1)
}

// Fatalln is equivalent to l.Println() followed by a call to os.Exit(1).
func (l *Logger) Fatalln(v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprintln(v...), FatalLevel)
	os.Exit(1)
}

// Error is the same as Errorf
func (l *Logger) Error(format string, v ...interface{}) {
	if l.level >= ErrorLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), ErrorLevel)
	}
}

// Errorf calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Errorf(format string, v ...interface{}) {
	if l.level >= ErrorLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), ErrorLevel)
	}
}

// Errorln debug level log
func (l *Logger) Errorln(v ...interface{}) {
	if l.level >= ErrorLevel {
		l.Output(l.callDepth, fmt.Sprintln(v...), ErrorLevel)
	}
}

// Warn is the same as Warnf
func (l *Logger) Warn(format string, v ...interface{}) {
	if l.level >= WarnLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), WarnLevel)
	}
}

// Warnf calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Warnf(format string, v ...interface{}) {
	if l.level >= WarnLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), WarnLevel)
	}
}

// Warnln debug level log
func (l *Logger) Warnln(v ...interface{}) {
	if l.level >= WarnLevel {
		l.Output(l.callDepth, fmt.Sprintln(v...), WarnLevel)
	}
}

// Info is the same as Infof
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level >= InfoLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), InfoLevel)
	}
}

// Infof calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Infof(format string, v ...interface{}) {
	if l.level >= InfoLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), InfoLevel)
	}
}

// Infoln debug level log
func (l *Logger) Infoln(v ...interface{}) {
	if l.level >= InfoLevel {
		l.Output(l.callDepth, fmt.Sprintln(v...), InfoLevel)
	}
}

// Debug is the same as Debugf
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level >= DebugLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), DebugLevel)
	}
}

// Debugf calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Debugf(format string, v ...interface{}) {
	if l.level >= DebugLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), DebugLevel)
	}
}

// Debugln debug level log
func (l *Logger) Debugln(v ...interface{}) {
	if l.level >= DebugLevel {
		l.Output(l.callDepth, fmt.Sprintln(v...), DebugLevel)
	}
}

// Verbose is the same as Verbosef
func (l *Logger) Verbose(format string, v ...interface{}) {
	if l.level >= VerboseLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), VerboseLevel)
	}
}

// Verbosef calls Output to print to the standard logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Verbosef(format string, v ...interface{}) {
	if l.level >= VerboseLevel {
		l.Output(l.callDepth, fmt.Sprintf(format, v...), VerboseLevel)
	}
}

// Verboseln verbose level log
func (l *Logger) Verboseln(v ...interface{}) {
	if l.level >= VerboseLevel {
		l.Output(l.callDepth, fmt.Sprintln(v...), VerboseLevel)
	}
}

// Print calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Print(v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprint(v...), l.level)
}

// Printf calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Printf(format string, v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprintf(format, v...), l.level)
}

// Println calls l.Output to print to the logger.
// Arguments are handled in the manner of fmt.Println.
func (l *Logger) Println(v ...interface{}) {
	l.Output(l.callDepth, fmt.Sprintln(v...), l.level)
}

// Level returns the log level.
func (l *Logger) Level() Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetLevel sets the log level.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Flags returns the output flags for the logger.
func (l *Logger) Flags() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flag
}

// SetFlags sets the output flags for the logger.
func (l *Logger) SetFlags(flag int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flag = flag
}

// Prefix returns the output prefix for the logger.
func (l *Logger) Prefix() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prefix
}

// SetPrefix sets the output prefix for the logger.
func (l *Logger) SetPrefix(prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prefix = prefix
}

// ContentPrefix returns the output content prefix for the logger.
func (l *Logger) ContentPrefix() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.contentPrefix
}

// SetContentPrefix sets the output content prefix for the logger.
func (l *Logger) SetContentPrefix(prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.contentPrefix = prefix
}

// Suffix returns the output suffix for the logger.
func (l *Logger) Suffix() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.suffix
}

// SetSuffix sets the output suffix for the logger.
func (l *Logger) SetSuffix(suffix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.suffix = suffix
}

// MultiLogger use to output log to console and file at the same time.
type MultiLogger struct {
	logger []Logger
}
