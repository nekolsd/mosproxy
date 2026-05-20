package router

import (
	"bytes"
	"testing"

	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
	"github.com/miekg/dns"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDebugLogMsgIncludesUpstream(t *testing.T) {
	r := require.New(t)

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	router := &Router{logger: &logger}

	q := NewQueryCtx()
	r.NoError(q.Question.Name.Parse("example.com."))
	q.Question.Class = dnsmsg.ClassINET
	q.Question.Type = dnsmsg.TypeA
	defer ReleaseQueryCtx(q)

	dnsM := new(dns.Msg)
	dnsM.SetQuestion("example.com.", dns.TypeA)
	wire, err := dnsM.Pack()
	r.NoError(err)

	m, err := dnsmsg.UnpackMsg(wire)
	r.NoError(err)
	defer dnsmsg.ReleaseMsg(m)

	router.debugLogMsg(q, m, "google", "sending query to upstream")

	logLine := buf.String()
	r.Contains(logLine, `"log_type":"dns_msg"`)
	r.Contains(logLine, `"upstream":"google"`)
	r.Contains(logLine, `"message":"sending query to upstream"`)
}
