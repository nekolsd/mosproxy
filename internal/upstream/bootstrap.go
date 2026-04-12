package upstream

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/IrineSistiana/mosproxy/internal/upstream/transport"
	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
)

const (
	bootstrapMinTTL     = 30 * time.Second
	bootstrapDefaultTTL = 5 * time.Minute
)

type bootstrapResolver struct {
	upstream transport.Transport
	mu       sync.Mutex
	cache    map[string]*resolveResult
	inflight map[string]*inflightQuery
}

type resolveResult struct {
	ips      []netip.Addr
	expireAt time.Time
}

type inflightQuery struct {
	done chan struct{}
	ips  []netip.Addr
	ttl  time.Duration
	err  error
}

func newBootstrapResolver(u transport.Transport) *bootstrapResolver {
	return &bootstrapResolver{
		upstream: u,
		cache:    make(map[string]*resolveResult),
		inflight: make(map[string]*inflightQuery),
	}
}

func (r *bootstrapResolver) resolveDialAddr(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = ""
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return addr, nil
	}
	ip, err := r.resolve(ctx, host)
	if err != nil {
		return "", fmt.Errorf("bootstrap resolve %s: %w", host, err)
	}
	if len(port) > 0 {
		return net.JoinHostPort(ip.String(), port), nil
	}
	return ip.String(), nil
}

func (r *bootstrapResolver) resolve(ctx context.Context, host string) (netip.Addr, error) {
	r.mu.Lock()
	if res, ok := r.cache[host]; ok && time.Now().Before(res.expireAt) {
		ips := res.ips
		r.mu.Unlock()
		return ips[rand.IntN(len(ips))], nil
	}

	if q, ok := r.inflight[host]; ok {
		r.mu.Unlock()
		select {
		case <-q.done:
		case <-ctx.Done():
			return netip.Addr{}, context.Cause(ctx)
		}
		if q.err != nil {
			return netip.Addr{}, q.err
		}
		return q.ips[rand.IntN(len(q.ips))], nil
	}

	q := &inflightQuery{done: make(chan struct{})}
	r.inflight[host] = q
	r.mu.Unlock()

	ips, ttl, err := r.queryUpstream(ctx, host)

	if err == nil && len(ips) == 0 {
		err = fmt.Errorf("no address found for %s", host)
	}
	q.ips = ips
	q.ttl = ttl
	q.err = err
	close(q.done)

	r.mu.Lock()
	delete(r.inflight, host)
	if err == nil {
		if ttl < bootstrapMinTTL {
			ttl = bootstrapMinTTL
		}
		r.cache[host] = &resolveResult{
			ips:      ips,
			expireAt: time.Now().Add(ttl),
		}
	}
	r.mu.Unlock()

	if err != nil {
		return netip.Addr{}, err
	}
	return ips[rand.IntN(len(ips))], nil
}

func (r *bootstrapResolver) queryUpstream(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
	fqdn := host
	if !isDotTerminated(fqdn) {
		fqdn += "."
	}

	type result struct {
		ips []netip.Addr
		ttl uint32
		err error
	}

	ch4 := make(chan result, 1)
	ch6 := make(chan result, 1)

	queryFn := func(qtype dnsmsg.Type, ch chan<- result) {
		m := dnsmsg.NewMsg()
		m.Header.RecursionDesired = true
		q := dnsmsg.NewQuestion()
		q.Name.Parse(fqdn)
		q.Class = dnsmsg.ClassINET
		q.Type = qtype
		m.Questions = append(m.Questions, q)

		resp, err := r.upstream.ExchangeContext(ctx, m)
		dnsmsg.ReleaseMsg(m)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer dnsmsg.ReleaseMsg(resp)

		var ips []netip.Addr
		var minTTL uint32 = 0xFFFFFFFF
		for _, rr := range resp.Answers {
			switch a := rr.(type) {
			case *dnsmsg.A:
				ips = append(ips, netip.AddrFrom4(a.A))
				if a.Hdr().TTL < minTTL {
					minTTL = a.Hdr().TTL
				}
			case *dnsmsg.AAAA:
				ips = append(ips, netip.AddrFrom16(a.AAAA).Unmap())
				if a.Hdr().TTL < minTTL {
					minTTL = a.Hdr().TTL
				}
			}
		}
		if minTTL == 0xFFFFFFFF {
			minTTL = uint32(bootstrapDefaultTTL.Seconds())
		}
		ch <- result{ips: ips, ttl: minTTL}
	}

	go queryFn(dnsmsg.TypeA, ch4)
	go queryFn(dnsmsg.TypeAAAA, ch6)

	r4 := <-ch4
	r6 := <-ch6

	var ips []netip.Addr
	var minTTL uint32 = 0xFFFFFFFF
	for _, r := range []result{r4, r6} {
		if r.err == nil && len(r.ips) > 0 {
			ips = append(ips, r.ips...)
			if r.ttl < minTTL {
				minTTL = r.ttl
			}
		}
	}

	if len(ips) == 0 {
		if r4.err != nil {
			return nil, 0, r4.err
		}
		return nil, 0, r6.err
	}
	if minTTL == 0xFFFFFFFF {
		minTTL = uint32(bootstrapDefaultTTL.Seconds())
	}
	return ips, time.Duration(minTTL) * time.Second, nil
}

func isDotTerminated(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '.'
}
