package router

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/IrineSistiana/mosproxy/internal/netlist"
)

type rule struct {
	cfg       RuleConfig
	domainSet *DomainSet              // maybe nil
	clientIp  *netlist.List[struct{}] // maybe nil
	upstream  Upstream                // maybe nil
	respIpSet *IpSet                  // maybe nil
}

func (r *Router) loadRule(cfg RuleConfig) (*rule, error) {
	ru := &rule{
		cfg: cfg,
	}
	if len(cfg.Domain) > 0 {
		m := r.domainSets[cfg.Domain]
		if m == nil {
			return nil, fmt.Errorf("cannot find domain set tag [%s]", cfg.Domain)
		}
		ru.domainSet = m
	}

	if len(cfg.Forward) > 0 {
		uw := r.upstreams[cfg.Forward]
		if uw != nil {
			ru.upstream = uw
		} else {
			lb := r.loadBalancers[cfg.Forward]
			if lb != nil {
				ru.upstream = lb
			} else {
				return nil, fmt.Errorf("unknown forward target [%s]", cfg.Forward)
			}
		}
	}

	if len(cfg.RespIP) > 0 {
		ipSet := r.ipSets[cfg.RespIP]
		if ipSet == nil {
			return nil, fmt.Errorf("cannot find ip set tag [%s]", cfg.RespIP)
		}
		ru.respIpSet = ipSet
	}

	if len(cfg.ClientIP) > 0 {
		lb := netlist.NewBuilder[struct{}](0)
		for _, s := range cfg.ClientIP {
			var (
				start, end netip.Addr
				err        error
			)
			s1, s2, ok := strings.Cut(s, "-")
			if ok { // range s1-s2
				start, err = netip.ParseAddr(s1)
				if err != nil {
					return nil, fmt.Errorf("invalid start addr [%s], %w", s1, err)
				}
				end, err = netip.ParseAddr(s2)
				if err != nil {
					return nil, fmt.Errorf("invalid end addr [%s], %w", s2, err)
				}
			} else if strings.ContainsRune(s, '/') { //cidr
				p, err := netip.ParsePrefix(s)
				if err != nil {
					return nil, fmt.Errorf("invalid cidr addr [%s], %w", s, err)
				}
				start = p.Masked().Addr()
				end = lastIP(p)
			} else { // single ip
				addr, err := netip.ParseAddr(s)
				if err != nil {
					return nil, fmt.Errorf("invalid addr [%s], %w", s, err)
				}
				start = addr
				end = addr
			}

			ok = lb.Add(start, end, struct{}{})
			if !ok {
				return nil, fmt.Errorf("invalid addr range [%s-%s]", start, end)
			}
		}

		l, err := lb.Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build client addr index, %w", err)
		}
		ru.clientIp = l
	}
	return ru, nil
}

func (ru *rule) match(q *QueryCtx) bool {
	ok := ru._match(q)
	if ru.cfg.Reverse {
		return !ok
	}
	return ok
}

func (ru *rule) _match(q *QueryCtx) bool {
	if ru.domainSet != nil {
		ok := ru.domainSet.Match(q.Question.Name)
		if !ok {
			return false
		}
	}

	if len(ru.cfg.Server) > 0 && ru.cfg.Server != q.ServerTag {
		return false
	}

	if len(ru.cfg.ServerName) > 0 && ru.cfg.ServerName != string(q.ServerName) {
		return false
	}

	if p := ru.cfg.Path; len(p) > 0 {
		if strings.HasSuffix(p, "/") {
			if len(q.Path) < len(p) || string(q.Path[0:len(p)]) != p {
				return false
			}
		} else {
			if p != string(q.Path) {
				return false
			}
		}
	}

	if ru.clientIp != nil {
		if !q.RemoteAddr.IsValid() {
			return false
		}
		_, ok := ru.clientIp.LookupAddr(q.RemoteAddr.Addr().Unmap())
		if !ok {
			return false
		}
	}
	return true
}
