package router

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/IrineSistiana/mosproxy/internal/pool"
	"github.com/IrineSistiana/mosproxy/internal/utils"
	"github.com/IrineSistiana/mosproxy/pkg/dnsmsg"
)

// always set q.Resp
func (r *Router) serverEntryHandler(q *QueryCtx) {
	// Set info about ECS
	// Priority: client addr < forward client ecs < overwrite
	if r.opt.ECS.Enabled {
		if q.RemoteAddr.IsValid() {
			addr := q.RemoteAddr.Addr().Unmap()
			if addr.Is4() {
				q.ECS2Upstream = netip.PrefixFrom(addr, 24)
			} else {
				q.ECS2Upstream = netip.PrefixFrom(addr, 48)
			}
		}
		if r.opt.ECS.Forward && q.ClientECS.IsValid() { //
			q.ECS2Upstream = q.ClientECS
		}

		// Get zone
		if r.ecsZone != nil {
			q.ECSZone, _ = r.ecsZone.Mark(q.ECS2Upstream.Addr())

			// No zone, assume it is local, don not send ecs to upstream
			if len(q.ECSZone) == 0 {
				q.ECS2Upstream = netip.Prefix{}
			}
		}

		// Overwrite ecs
		if r.ecsZoneOverwrite != nil {
			p, ok := r.ecsZoneOverwrite.Get(q.ECSZone)
			if ok {
				q.ECS2Upstream = p
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.ctx, queryTimeout)
	defer cancel()

	if len(r.middlewares) > 0 {
		r.middlewares[0].Handle(ctx, q)
	} else {
		r.BuiltInHandler(ctx, q)
	}

	if q.Resp() == nil {
		SetEmptyRespMQ(q, dnsmsg.RCodeRefused)
	}
	if r.opt.Log.Queries {
		r.logAccess(q)
	}
}

var cacheKeyPool = pool.NewBytesPool()

// router main handle func.
func (r *Router) BuiltInHandler(ctx context.Context, q *QueryCtx) {
	ckb := cacheKeyPool.Get()
	defer cacheKeyPool.Release(ckb)

	for _, rule := range r.rules {
		if !rule.match(q) {
			continue
		}
		if rejectRCode := rule.cfg.Reject; rejectRCode > 0 {
			SetEmptyRespMQ(q, dnsmsg.RCode(rejectRCode))
			return
		}
		if rule.hosts != nil {
			resp, matched := rule.hosts.Response(q)
			if matched {
				q.SetRespFrom(resp, "hosts")
				return
			}
			continue
		}
		upstream := rule.upstream
		if upstream == nil {
			SetEmptyRespMQ(q, dnsmsg.RCodeRefused)
			return
		}

		ckb.B = ckb.B[:0]
		ckb.B = r.appendCacheKey(ckb.B, q, rule.cfg.Forward)
		resp, t := r.cache.Get(ctx, ckb.B)
		if resp != nil {
			if rule.respIpSet != nil && rule.respIpSet.MatchMsg(resp) {
				dnsmsg.ReleaseMsg(resp)
				if rule.respIpUpstream != nil {
					r.handleRespIpFallback(ctx, ckb, q, rule)
					return
				}
				continue
			}
			if r.needPrefetch(t) {
				r.AsyncSingleFlightPrefetch(ckb.B, q, upstream, rule)
			}
			r.queryCacheHitTotal.Inc()
			q.SetRespFrom(resp, "cache")
			return
		}

		if ctxDone(ctx) {
			SetEmptyRespMQ(q, dnsmsg.RCodeServerFailure)
			return
		}

		err := r.forward(ctx, q, upstream)
		if err != nil {
			SetEmptyRespMQ(q, dnsmsg.RCodeServerFailure)
			return
		}

		if rule.respIpSet != nil && rule.respIpSet.MatchMsg(q.Resp()) {
			r.cache.Store(ckb.B, q.Resp())
			q.SetResp(nil)
			if rule.respIpUpstream != nil {
				r.handleRespIpFallback(ctx, ckb, q, rule)
				return
			}
			continue
		}

		r.cache.Store(ckb.B, q.Resp())
		return
	}

	SetEmptyRespMQ(q, dnsmsg.RCodeRefused)
}

// handleRespIpFallback handles the case where resp_ip matched and a fallback
// upstream (resp_ip_forward) is configured. It queries the fallback upstream
// (with its own cache key) and stores the result.
func (r *Router) handleRespIpFallback(ctx context.Context, ckb *pool.Bytes, q *QueryCtx, rule *rule) {
	ckb.B = ckb.B[:0]
	ckb.B = r.appendCacheKey(ckb.B, q, rule.cfg.RespIPForward)
	resp, t := r.cache.Get(ctx, ckb.B)
	if resp != nil {
		if r.needPrefetch(t) {
			r.AsyncSingleFlightPrefetch(ckb.B, q, rule.respIpUpstream, nil)
		}
		r.queryCacheHitTotal.Inc()
		q.SetRespFrom(resp, "cache")
		return
	}

	if ctxDone(ctx) {
		SetEmptyRespMQ(q, dnsmsg.RCodeServerFailure)
		return
	}

	err := r.forward(ctx, q, rule.respIpUpstream)
	if err != nil {
		SetEmptyRespMQ(q, dnsmsg.RCodeServerFailure)
		return
	}
	r.cache.Store(ckb.B, q.Resp())
}

// Prefetching q in other goroutine.
// If a query with same key is currently prefetching, do nothing.
func (r *Router) AsyncSingleFlightPrefetch(key []byte, q *QueryCtx, u Upstream, rule *rule) {
	if len(key) == 0 {
		return
	}
	sk, ok := r.prefetchSf.Reserve(key)
	if !ok {
		return
	}
	qCopy := q.Copy()
	go func() {
		defer ReleaseQueryCtx(qCopy)
		defer r.prefetchSf.Done(sk)
		r.DoPrefetch(utils.Str2BytesUnsafe(sk), qCopy, u, rule)
	}()
}

// Send q to u, and save response under key.
// If the rule has resp_ip configured and the response matches, the result is
// discarded (or forwarded via the fallback upstream if resp_ip_forward is set).
func (r *Router) DoPrefetch(key []byte, q *QueryCtx, u Upstream, rule *rule) {
	if len(key) == 0 {
		return
	}

	q.Prefetch = true
	ctx, cancel := context.WithTimeout(r.ctx, prefetchTimeout)
	defer cancel()
	err := r.forward(ctx, q, u)
	if err != nil {
		return
	}
	if rule != nil && rule.respIpSet != nil && rule.respIpSet.MatchMsg(q.Resp()) {
		r.cache.Store(key, q.Resp())
		if rule.respIpUpstream != nil {
			err = r.forward(ctx, q, rule.respIpUpstream)
			if err != nil {
				return
			}
			fb := cacheKeyPool.Get()
			fb.B = r.appendCacheKey(fb.B[:0], q, rule.cfg.RespIPForward)
			r.prefetchTotal.Inc()
			r.cache.Store(fb.B, q.Resp())
			cacheKeyPool.Release(fb)
		}
		return
	}
	r.prefetchTotal.Inc()
	r.cache.Store(key, q.Resp())
}

// forward query to upstream and set the response.
// Will remove edns0 from resp.
func (r *Router) forward(ctx context.Context, q *QueryCtx, upstream Upstream) error {
	noECS := false
	if uw, ok := upstream.(*UpstreamWrapper); ok {
		noECS = uw.noECS
	}
	m := r.makeQueryMsg(q, noECS)
	defer dnsmsg.ReleaseMsg(m)

	err := upstream.Exchange(ctx, q, m)
	if err != nil {
		return fmt.Errorf("failed to exchange, %w", err)
	}
	if r := q.Resp(); r != nil {
		dnsmsg.RemoveEDNS0(r)
	}
	return nil
}

func (r *Router) makeQueryMsg(q *QueryCtx, noECS bool) *dnsmsg.Msg {
	m := dnsmsg.NewMsg()
	m.Header.RecursionDesired = true
	m.Questions = append(m.Questions, q.Question.Copy())

	opt := newEDNS0(udpSize)
	if !noECS {
		if ecs := q.ECS2Upstream; ecs.IsValid() {
			addr := ecs.Addr()
			if !addr.IsPrivate() && addr.IsGlobalUnicast() {
				opt.Data = makeEdns0ClientSubnetReqOpt(ecs)
			}
		}
	}
	m.Additionals = append(m.Additionals, opt)
	return m
}
