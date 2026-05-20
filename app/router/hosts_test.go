package router

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func makeHostsForTest(t *testing.T, entries []string) *Hosts {
	t.Helper()
	records, err := parseHostsEntries(entries)
	require.NoError(t, err)
	return &Hosts{
		entries: records,
		ttl:     60,
	}
}

func makeQueryCtxForTest(t *testing.T, name string, typ dnsmsg.Type) *QueryCtx {
	t.Helper()
	q := NewQueryCtx()
	require.NoError(t, q.Question.Name.Parse(name))
	q.Question.Class = dnsmsg.ClassINET
	q.Question.Type = typ
	return q
}

func TestHostsResponse(t *testing.T) {
	r := require.New(t)
	h := makeHostsForTest(t, []string{
		"dns.google 8.8.8.8 8.8.4.4 2001:4860:4860::8888 8.8.8.8",
		"dns.google 2001:4860:4860::8844",
		"example.com 1.1.1.1",
	})

	q := makeQueryCtxForTest(t, "DNS.Google.", dnsmsg.TypeA)
	defer ReleaseQueryCtx(q)
	resp, matched := h.Response(q)
	r.True(matched)
	defer dnsmsg.ReleaseMsg(resp)
	r.Len(resp.Answers, 2)
	r.Equal(dnsmsg.RCodeSuccess, resp.RCode)
	r.Equal(netip.MustParseAddr("8.8.8.8").As4(), resp.Answers[0].(*dnsmsg.A).A)
	r.Equal(uint32(60), resp.Answers[0].Hdr().TTL)
	r.Equal(netip.MustParseAddr("8.8.4.4").As4(), resp.Answers[1].(*dnsmsg.A).A)

	q6 := makeQueryCtxForTest(t, "dns.google.", dnsmsg.TypeAAAA)
	defer ReleaseQueryCtx(q6)
	resp6, matched := h.Response(q6)
	r.True(matched)
	defer dnsmsg.ReleaseMsg(resp6)
	r.Len(resp6.Answers, 2)
	r.Equal(netip.MustParseAddr("2001:4860:4860::8888").As16(), resp6.Answers[0].(*dnsmsg.AAAA).AAAA)
	r.Equal(netip.MustParseAddr("2001:4860:4860::8844").As16(), resp6.Answers[1].(*dnsmsg.AAAA).AAAA)
}

func TestHostsResponseTypeMismatchReturnsEmptySuccess(t *testing.T) {
	r := require.New(t)
	h := makeHostsForTest(t, []string{"example.com 1.1.1.1"})

	q := makeQueryCtxForTest(t, "example.com.", dnsmsg.TypeAAAA)
	defer ReleaseQueryCtx(q)
	resp, matched := h.Response(q)
	r.True(matched)
	defer dnsmsg.ReleaseMsg(resp)
	r.Equal(dnsmsg.RCodeSuccess, resp.RCode)
	r.Empty(resp.Answers)
	r.Len(resp.Authorities, 1)
	soa := resp.Authorities[0].(*dnsmsg.SOA)
	r.Equal(dnsmsg.TypeSOA, soa.Type)
	r.Equal(uint32(60), soa.TTL)
	r.Equal(uint32(60), soa.MinTTL)
}

func TestHostsResponseMiss(t *testing.T) {
	r := require.New(t)
	h := makeHostsForTest(t, []string{"example.com 1.1.1.1"})

	q := makeQueryCtxForTest(t, "notfound.example.", dnsmsg.TypeA)
	defer ReleaseQueryCtx(q)
	resp, matched := h.Response(q)
	r.False(matched)
	r.Nil(resp)

	qMX := makeQueryCtxForTest(t, "example.com.", dnsmsg.TypeMX)
	defer ReleaseQueryCtx(qMX)
	resp, matched = h.Response(qMX)
	r.False(matched)
	r.Nil(resp)
}

func TestHostsHotReload(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	fp := filepath.Join(dir, "hosts.txt")
	r.NoError(os.WriteFile(fp, []byte("example.com 1.1.1.1\n"), 0644))

	logger := zerolog.Nop()
	loader := NewFileLoader(fp, parseHostsFile, &logger, nil)
	_, err := loader.Init()
	r.NoError(err)
	h := &Hosts{
		ttl:             300,
		FileLoaderGroup: FileLoaderGroup[hostsRecords]{loader},
	}

	q := makeQueryCtxForTest(t, "example.com.", dnsmsg.TypeA)
	defer ReleaseQueryCtx(q)
	resp, matched := h.Response(q)
	r.True(matched)
	r.Equal(netip.MustParseAddr("1.1.1.1").As4(), resp.Answers[0].(*dnsmsg.A).A)
	dnsmsg.ReleaseMsg(resp)

	r.NoError(os.WriteFile(fp, []byte("example.com 2.2.2.2\n"), 0644))
	r.True(h.LoadAndStage())
	h.Commit()

	resp, matched = h.Response(q)
	r.True(matched)
	defer dnsmsg.ReleaseMsg(resp)
	r.Equal(netip.MustParseAddr("2.2.2.2").As4(), resp.Answers[0].(*dnsmsg.A).A)
}

func TestBuiltInHandlerHostsTypeMismatchStopsRuleChain(t *testing.T) {
	r := require.New(t)
	h := makeHostsForTest(t, []string{"example.com 1.1.1.1"})
	router := &Router{
		rules: []*rule{
			{hosts: h},
			{cfg: RuleConfig{Reject: uint16(dnsmsg.RCodeNameError)}},
		},
	}

	q := makeQueryCtxForTest(t, "example.com.", dnsmsg.TypeAAAA)
	defer ReleaseQueryCtx(q)
	router.BuiltInHandler(context.Background(), q)
	r.NotNil(q.Resp())
	r.Equal(dnsmsg.RCodeSuccess, q.Resp().RCode)
	r.Empty(q.Resp().Answers)
	r.Len(q.Resp().Authorities, 1)
}

func TestBuiltInHandlerHostsMissContinuesRuleChain(t *testing.T) {
	r := require.New(t)
	h := makeHostsForTest(t, []string{"example.com 1.1.1.1"})
	router := &Router{
		rules: []*rule{
			{hosts: h},
			{cfg: RuleConfig{Reject: uint16(dnsmsg.RCodeNameError)}},
		},
	}

	q := makeQueryCtxForTest(t, "notfound.example.", dnsmsg.TypeA)
	defer ReleaseQueryCtx(q)
	router.BuiltInHandler(context.Background(), q)
	r.NotNil(q.Resp())
	r.Equal(dnsmsg.RCodeNameError, q.Resp().RCode)
}

func TestLoadRuleRejectsHostsAndForwardInSameRule(t *testing.T) {
	r := require.New(t)
	router := &Router{}
	_, err := router.loadRule(RuleConfig{Hosts: "local", Forward: "default"})
	r.Error(err)
	r.Contains(err.Error(), "hosts and forward cannot be configured in the same rule")
}
