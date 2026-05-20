package mlog

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	LogTypeField  = "log_type"
	LogTypeQuery  = "query"
	LogTypeDNSMsg = "dns_msg"
	LogTypeLog    = "log"

	consoleTimeFormat = "[2006-01-02 15:04:05 UTC-07]"
)

var (
	l   = initLogger()
	nop = zerolog.Nop()
)

func SetLvl(lvl zerolog.Level) {
	zerolog.SetGlobalLevel(lvl)
}

func initLogger() zerolog.Logger {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	var w io.Writer
	if ok, _ := strconv.ParseBool(os.Getenv("MOSPROXY_JSONLOGGER")); ok {
		w = lock(os.Stderr)
	} else {
		w = newConsoleWriter(os.Stdout, false)
	}

	l := zerolog.New(w).With().Timestamp().Logger()

	// Redirect std log
	redirectWriter := WriteToLogger(&l, "redirected std log", "data")
	log.SetFlags(0) // disable time/date
	log.SetPrefix("")
	log.SetOutput(redirectWriter)

	// quic warning, we don't need this.
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "1")
	return l
}

func SetOutput(w io.Writer) {
	cw := newConsoleWriter(w, true)
	l = zerolog.New(cw).With().Timestamp().Logger()
	log.SetOutput(WriteToLogger(&l, "redirected std log", "data"))
}

func newConsoleWriter(out io.Writer, noColor bool) zerolog.ConsoleWriter {
	return zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = out
		w.NoColor = noColor
		w.TimeFormat = consoleTimeFormat
		w.TimeLocation = time.Local
		w.FieldsExclude = []string{LogTypeField}
		w.FormatPrepare = prepareConsoleEvent
		w.FormatLevel = formatConsoleLevel
	})
}

func prepareConsoleEvent(evt map[string]interface{}) error {
	if _, ok := evt[zerolog.LevelFieldName]; ok {
		return nil
	}
	evt[zerolog.LevelFieldName] = formatNoLevelType(evt[LogTypeField])
	return nil
}

func formatNoLevelType(i interface{}) string {
	switch fmt.Sprint(i) {
	case LogTypeQuery:
		return "QUERY"
	case LogTypeDNSMsg:
		return "DNSMSG"
	case LogTypeLog:
		return "LOG"
	default:
		return "LOG"
	}
}

func formatConsoleLevel(i interface{}) string {
	switch v := i.(type) {
	case nil:
		return "LOG"
	case string:
		if v == "" {
			return "LOG"
		}
		return strings.ToUpper(v)
	default:
		return strings.ToUpper(fmt.Sprint(v))
	}
}

func Lock(w io.Writer) io.Writer {
	return lock(w)
}

func Nop() *zerolog.Logger {
	return &nop
}

func L() *zerolog.Logger {
	return &l
}

func WriteToLogger(to *zerolog.Logger, msg string, key string) io.Writer {
	return &logCatcher{
		logger: to.With().CallerWithSkipFrameCount(1).Logger(),
		msg:    msg,
		key:    key}
}

type logCatcher struct {
	logger zerolog.Logger
	msg    string
	key    string
}

func (w *logCatcher) Write(b []byte) (int, error) {
	b = bytes.TrimSpace(b) // trim \n from std logger
	w.logger.Log().Str(LogTypeField, LogTypeLog).Bytes(w.key, b).Msg(w.msg)
	return len(b), nil
}

type safeWriter struct {
	m sync.Mutex
	w io.Writer
}

func lock(w io.Writer) io.Writer {
	return &safeWriter{
		w: w,
	}
}

func (w *safeWriter) Write(b []byte) (int, error) {
	w.m.Lock()
	defer w.m.Unlock()
	return w.w.Write(b)
}
