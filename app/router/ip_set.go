package router

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/netip"

	"github.com/IrineSistiana/mosproxy/internal/netlist"
	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
	"github.com/rs/zerolog"
)

func (r *Router) loadIpSet(cfg *IpSetConfig) error {
	if len(cfg.Tag) == 0 {
		return errors.New("missing tag")
	}
	if _, dup := r.ipSets[cfg.Tag]; dup {
		return fmt.Errorf("dup tag [%s]", cfg.Tag)
	}

	vInfo := func(e *zerolog.Event, v *netlist.List[struct{}]) {
		e.Int("ip_ranges", v.Len())
	}
	s := make(FileLoaderGroup[netlist.List[struct{}]], 0)
	for _, fp := range cfg.Files {
		logger := r.logger.With().Str("ip_set", cfg.Tag).Str("file", fp).Logger()
		loader := NewFileLoader(fp, parseCIDRFile, &logger, vInfo)
		_, err := loader.Init()
		if err != nil {
			return fmt.Errorf("failed to load ip set from file %s, %w", fp, err)
		}
		s = append(s, loader)
	}
	r.ipSets[cfg.Tag] = &IpSet{FileLoaderGroup: s}
	return nil
}

type IpSet struct {
	FileLoaderGroup[netlist.List[struct{}]]
}

func (g *IpSet) MatchAddr(addr netip.Addr) bool {
	for _, loader := range g.FileLoaderGroup {
		if _, ok := loader.V().LookupAddr(addr); ok {
			return true
		}
	}
	return false
}

// MatchMsg returns true if any A/AAAA answer record IP is in this set.
func (g *IpSet) MatchMsg(resp *dnsmsg.Msg) bool {
	if resp == nil {
		return false
	}
	for _, rr := range resp.Answers {
		switch rr := rr.(type) {
		case *dnsmsg.A:
			if g.MatchAddr(netip.AddrFrom4(rr.A)) {
				return true
			}
		case *dnsmsg.AAAA:
			if g.MatchAddr(netip.AddrFrom16(rr.AAAA).Unmap()) {
				return true
			}
		}
	}
	return false
}

func parseCIDRFile(b []byte) (*netlist.List[struct{}], error) {
	lb := netlist.NewBuilder[struct{}](0)
	scanner := bufio.NewScanner(bytes.NewReader(b))
	line := 0
	for scanner.Scan() {
		line++
		s := scanner.Bytes()

		if idx := bytes.IndexByte(s, '#'); idx >= 0 {
			s = s[:idx]
		}
		s = bytes.TrimSpace(s)
		if len(s) == 0 {
			continue
		}

		prefix, err := netip.ParsePrefix(string(s))
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid CIDR %q, %w", line, s, err)
		}
		start := prefix.Masked().Addr()
		end := lastIP(prefix)
		if !lb.Add(start, end, struct{}{}) {
			return nil, fmt.Errorf("line %d: invalid range for CIDR %q", line, s)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lb.Build()
}

// lastIP returns the last (highest) IP in a prefix.
func lastIP(p netip.Prefix) netip.Addr {
	if !p.IsValid() {
		return netip.Addr{}
	}
	a16 := p.Addr().As16()
	var off uint8
	var bits uint8 = 128
	if p.Addr().Is4() {
		off = 12
		bits = 32
	}
	for b := uint8(p.Bits()); b < bits; b++ {
		byteNum, bitInByte := b/8, 7-(b%8)
		a16[off+byteNum] |= 1 << uint(bitInByte)
	}
	if p.Addr().Is4() {
		return netip.AddrFrom16(a16).Unmap()
	}
	return netip.AddrFrom16(a16)
}
