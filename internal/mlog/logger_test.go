package mlog

import (
	"bytes"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestConsoleWriterFormat(t *testing.T) {
	oldTimeFieldFormat := zerolog.TimeFieldFormat
	oldTimestampFunc := zerolog.TimestampFunc
	defer func() {
		zerolog.TimeFieldFormat = oldTimeFieldFormat
		zerolog.TimestampFunc = oldTimestampFunc
	}()

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerolog.TimestampFunc = func() time.Time {
		return time.Date(2026, 5, 20, 4, 31, 17, 0, time.UTC)
	}

	var buf bytes.Buffer
	cw := newConsoleWriter(&buf, true)
	cw.TimeLocation = time.UTC
	logger := zerolog.New(cw).With().Timestamp().Logger()

	logger.Error().Msg("boom")
	require.Equal(t, "[2026-05-20 04:31:17 UTC+00] ERROR boom\n", buf.String())

	buf.Reset()
	logger.Log().Str(LogTypeField, LogTypeQuery).Msg("query log")
	require.Equal(t, "[2026-05-20 04:31:17 UTC+00] QUERY query log\n", buf.String())

	buf.Reset()
	logger.Log().Str(LogTypeField, LogTypeDNSMsg).Msg("sending query to upstream")
	require.Equal(t, "[2026-05-20 04:31:17 UTC+00] DNSMSG sending query to upstream\n", buf.String())

	buf.Reset()
	logger.Log().Msg("redirected std log")
	require.Equal(t, "[2026-05-20 04:31:17 UTC+00] LOG redirected std log\n", buf.String())
}
