package router

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
	"github.com/rs/zerolog"
)

const defaultHostsTTL = 300

type hostRecord struct {
	IPv4 []netip.Addr
	IPv6 []netip.Addr
}

type hostsRecords map[string]*hostRecord

func (r *Router) loadHosts(cfg *HostsConfig) error {
	if len(cfg.Tag) == 0 {
		return errors.New("missing tag")
	}
	if _, dup := r.hosts[cfg.Tag]; dup {
		return fmt.Errorf("dup tag [%s]", cfg.Tag)
	}

	inline, err := parseHostsEntries(cfg.Entries)
	if err != nil {
		return fmt.Errorf("failed to load inline hosts entries, %w", err)
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultHostsTTL
	}

	vInfo := func(e *zerolog.Event, v *hostsRecords) {
		e.Int("hosts", len(*v))
	}
	s := make(FileLoaderGroup[hostsRecords], 0, len(cfg.Files))
	for _, fp := range cfg.Files {
		logger := r.logger.With().Str("hosts", cfg.Tag).Str("file", fp).Logger()
		loader := NewFileLoader(fp, parseHostsFile, &logger, vInfo)
		_, err := loader.Init()
		if err != nil {
			return fmt.Errorf("failed to load hosts from file %s, %w", fp, err)
		}
		s = append(s, loader)
	}

	r.hosts[cfg.Tag] = &Hosts{
		entries:         inline,
		ttl:             uint32(ttl),
		FileLoaderGroup: s,
	}
	return nil
}

type Hosts struct {
	entries hostsRecords
	ttl     uint32
	FileLoaderGroup[hostsRecords]
}

func (h *Hosts) Response(q *QueryCtx) (*dnsmsg.Msg, bool) {
	question := &q.Question
	if question.Class != dnsmsg.ClassINET || (question.Type != dnsmsg.TypeA && question.Type != dnsmsg.TypeAAAA) {
		return nil, false
	}

	rec, matched := h.lookup(question.Name)
	if !matched {
		return nil, false
	}

	resp := dnsmsg.NewMsg()
	resp.RCode = dnsmsg.RCodeSuccess
	resp.Questions = append(resp.Questions, question.Copy())

	switch question.Type {
	case dnsmsg.TypeA:
		for _, ip := range rec.IPv4 {
			a := dnsmsg.NewA()
			a.Name.CopyFrom(question.Name)
			a.Type = dnsmsg.TypeA
			a.Class = dnsmsg.ClassINET
			a.TTL = h.ttl
			a.A = ip.As4()
			resp.Answers = append(resp.Answers, a)
		}
	case dnsmsg.TypeAAAA:
		for _, ip := range rec.IPv6 {
			aaaa := dnsmsg.NewAAAA()
			aaaa.Name.CopyFrom(question.Name)
			aaaa.Type = dnsmsg.TypeAAAA
			aaaa.Class = dnsmsg.ClassINET
			aaaa.TTL = h.ttl
			aaaa.AAAA = ip.As16()
			resp.Answers = append(resp.Answers, aaaa)
		}
	}

	if len(resp.Answers) == 0 {
		resp.Authorities = append(resp.Authorities, makeHostsSOA(question.Name, h.ttl))
	}
	return resp, true
}

func (h *Hosts) lookup(name dnsmsg.Name) (hostRecord, bool) {
	key := lowerNameKey(name)
	var out hostRecord
	matched := false
	merge := func(records hostsRecords) {
		if rec := records[key]; rec != nil {
			matched = true
			mergeHostRecord(&out, rec)
		}
	}

	merge(h.entries)
	for _, loader := range h.FileLoaderGroup {
		if records := loader.V(); records != nil {
			merge(*records)
		}
	}
	return out, matched
}

func parseHostsEntries(entries []string) (hostsRecords, error) {
	return parseHostsText([]byte(strings.Join(entries, "\n")))
}

func parseHostsFile(b []byte) (*hostsRecords, error) {
	records, err := parseHostsText(b)
	if err != nil {
		return nil, err
	}
	return &records, nil
}

func parseHostsText(b []byte) (hostsRecords, error) {
	records := make(hostsRecords)
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
		if err := parseHostsLine(records, string(s)); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func parseHostsLine(records hostsRecords, s string) error {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return fmt.Errorf("missing IP address in %q", s)
	}

	var name dnsmsg.Name
	if err := name.Parse(fields[0]); err != nil {
		return fmt.Errorf("invalid domain %q, %w", fields[0], err)
	}
	name.ToLower()
	key := string(name.Data())

	rec := records[key]
	if rec == nil {
		rec = new(hostRecord)
		records[key] = rec
	}
	for _, ipStr := range fields[1:] {
		ip, err := netip.ParseAddr(ipStr)
		if err != nil {
			return fmt.Errorf("invalid IP address %q, %w", ipStr, err)
		}
		if ip.Is4() {
			appendUniqueAddr(&rec.IPv4, ip)
		} else {
			appendUniqueAddr(&rec.IPv6, ip)
		}
	}
	return nil
}

func appendUniqueAddr(dst *[]netip.Addr, addr netip.Addr) {
	for _, existing := range *dst {
		if existing == addr {
			return
		}
	}
	*dst = append(*dst, addr)
}

func mergeHostRecord(dst *hostRecord, src *hostRecord) {
	for _, ip := range src.IPv4 {
		appendUniqueAddr(&dst.IPv4, ip)
	}
	for _, ip := range src.IPv6 {
		appendUniqueAddr(&dst.IPv6, ip)
	}
}

func lowerNameKey(name dnsmsg.Name) string {
	b := append([]byte(nil), name.Data()...)
	for i := 0; i < len(b); {
		l := int(b[i])
		if l == 0 {
			break
		}
		for j := 1; j <= l && i+j < len(b); j++ {
			c := b[i+j]
			if 'A' <= c && c <= 'Z' {
				b[i+j] = c + ('a' - 'A')
			}
		}
		i += 1 + l
	}
	return string(b)
}

func makeHostsSOA(name dnsmsg.Name, ttl uint32) *dnsmsg.SOA {
	soa := dnsmsg.NewSOA()
	soa.Name.CopyFrom(name)
	soa.Type = dnsmsg.TypeSOA
	soa.Class = dnsmsg.ClassINET
	soa.TTL = ttl
	_ = soa.NS.Parse("fake-ns.mosproxy.fake.root.")
	_ = soa.MBox.Parse("fake-mbox.mosproxy.fake.root.")
	soa.Serial = 2021110400
	soa.Refresh = 1800
	soa.Retry = 900
	soa.Expire = 604800
	soa.MinTTL = ttl
	return soa
}
