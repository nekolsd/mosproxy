package router

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/IrineSistiana/mosproxy/internal/netlist"
	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func buildTestIpSet(t *testing.T, content string) *IpSet {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, "cidrs.txt")
	require.NoError(t, os.WriteFile(fp, []byte(content), 0644))

	logger := zerolog.Nop()
	loader := NewFileLoader(fp, parseCIDRFile, &logger, nil)
	_, err := loader.Init()
	require.NoError(t, err)

	return &IpSet{
		FileLoaderGroup: FileLoaderGroup[netlist.List[struct{}]]{loader},
	}
}

func TestParseCIDRFile(t *testing.T) {
	r := require.New(t)

	l, err := parseCIDRFile([]byte("# comment\n23.0.0.0/8\n104.64.0.0/10\n2600:1400::/24\n\n"))
	r.NoError(err)
	r.Equal(3, l.Len())

	_, ok := l.LookupAddr(netip.MustParseAddr("23.1.2.3"))
	r.True(ok)
	_, ok = l.LookupAddr(netip.MustParseAddr("1.1.1.1"))
	r.False(ok)
}

func TestParseCIDRFileInvalid(t *testing.T) {
	_, err := parseCIDRFile([]byte("not-a-cidr\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid CIDR")
}

func TestLastIP(t *testing.T) {
	r := require.New(t)
	r.Equal(netip.MustParseAddr("192.168.1.255"), lastIP(netip.MustParsePrefix("192.168.1.0/24")))
	r.Equal(netip.MustParseAddr("10.255.255.255"), lastIP(netip.MustParsePrefix("10.0.0.0/8")))
	r.Equal(netip.MustParseAddr("1.2.3.4"), lastIP(netip.MustParsePrefix("1.2.3.4/32")))
}

func TestIpSetMatchMsg(t *testing.T) {
	r := require.New(t)
	ipSet := buildTestIpSet(t, "23.0.0.0/8\n104.64.0.0/10\n2600:1400::/24\n")

	makeA := func(ip [4]byte) *dnsmsg.Msg {
		m := dnsmsg.NewMsg()
		a := &dnsmsg.A{}
		a.A = ip
		m.Answers = append(m.Answers, a)
		return m
	}
	makeAAAA := func(ip [16]byte) *dnsmsg.Msg {
		m := dnsmsg.NewMsg()
		aaaa := &dnsmsg.AAAA{}
		aaaa.AAAA = ip
		m.Answers = append(m.Answers, aaaa)
		return m
	}

	// Hit: 23.1.2.3 is in 23.0.0.0/8
	r.True(ipSet.MatchMsg(makeA([4]byte{23, 1, 2, 3})))
	// Hit: 104.90.0.1 is in 104.64.0.0/10
	r.True(ipSet.MatchMsg(makeA([4]byte{104, 90, 0, 1})))
	// Miss
	r.False(ipSet.MatchMsg(makeA([4]byte{1, 1, 1, 1})))
	r.False(ipSet.MatchMsg(makeA([4]byte{8, 8, 8, 8})))

	// Hit: IPv6 2600:1400::1
	var ip6 [16]byte
	ip6[0] = 0x26
	ip6[1] = 0x00
	ip6[2] = 0x14
	ip6[3] = 0x00
	ip6[15] = 0x01
	r.True(ipSet.MatchMsg(makeAAAA(ip6)))

	// Miss: IPv6 2001:db8::1
	var ip6miss [16]byte
	ip6miss[0] = 0x20
	ip6miss[1] = 0x01
	ip6miss[2] = 0x0d
	ip6miss[3] = 0xb8
	ip6miss[15] = 0x01
	r.False(ipSet.MatchMsg(makeAAAA(ip6miss)))

	// Empty / nil
	r.False(ipSet.MatchMsg(dnsmsg.NewMsg()))
	r.False(ipSet.MatchMsg(nil))
}

func TestIpSetHotReload(t *testing.T) {
	r := require.New(t)

	dir := t.TempDir()
	fp := filepath.Join(dir, "cidrs.txt")
	r.NoError(os.WriteFile(fp, []byte("23.0.0.0/8\n"), 0644))

	logger := zerolog.Nop()
	loader := NewFileLoader(fp, parseCIDRFile, &logger, nil)
	_, err := loader.Init()
	r.NoError(err)
	ipSet := &IpSet{FileLoaderGroup: FileLoaderGroup[netlist.List[struct{}]]{loader}}

	r.True(ipSet.MatchAddr(netip.MustParseAddr("23.1.2.3")))
	r.False(ipSet.MatchAddr(netip.MustParseAddr("104.90.0.1")))

	// Update file and reload
	r.NoError(os.WriteFile(fp, []byte("104.64.0.0/10\n"), 0644))
	r.True(ipSet.LoadAndStage())
	ipSet.Commit()

	r.False(ipSet.MatchAddr(netip.MustParseAddr("23.1.2.3")))
	r.True(ipSet.MatchAddr(netip.MustParseAddr("104.90.0.1")))
}
