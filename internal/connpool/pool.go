package connpool

import (
	"context"
	"errors"
	"io"
	"sync"
)

const (
	maxSearchSteps = 4
)

var (
	ErrPoolClosed       = errors.New("pool closed")
	ErrNoConnAvailable  = errors.New("no available connection")
	ErrConnNotAvailable = errors.New("connection not available")
)

// Conn will be stored in Pool.
type Conn interface {
	io.Closer

	// Report the current status of the connection.
	// Should be fast and should not block. Otherwise, it slow down or block most of
	// calls of the Pool.
	Status() ConnStatus

	// The connection is picked by the pool. Reserve one request.
	Reserve()
}

type ConnStatus struct {
	// Closed connections will not be picked up and will be removed from the pool.
	// This status MUST NOT be changed once it was true.
	Closed bool
	// Indicate the connection can be picked up at least once (can take at least one
	// more request).
	// If unavailable, the connection won't be picked up.
	// This status can be changed at any time.
	Available bool
}

type streamStatus struct {
	curStream int
}

// Pool is a conn pool for client.
// It can distribute streams/requests to different
// connections while trying to keep reuse efficiency high.
type Pool struct {
	opts Opts

	m            sync.Mutex
	closed       bool
	dialingCalls map[*dialingCall]struct{}
	lastDialCall *dialingCall

	busyConns map[Conn]*streamStatus
	idleConns map[Conn]*streamStatus
}

type Opts struct {
	// Func for dialing new connections.
	// ctx does not have timeout. It will only be canceled when the Pool being closed.
	// Note: the returned Conn MUST comparable (e.g. typically a pointer).
	Dial func(ctx context.Context) (Conn, error)

	// Max number of connections (include dialing connections) this pool
	// can have. Default 0 means no limit.
	MaxConn int

	// Max number of streams that one connection can handle.
	// Default is 1.
	MaxStream int

	// The max ratio of idle connections / busy connections.
	// Default is 1.
	// Note for "0" corner cases: The pool will always allow one idle connection.
	MaxIdleBusyRatio float64
}

func NewPool(opts Opts) *Pool {
	return &Pool{
		opts:         opts,
		dialingCalls: make(map[*dialingCall]struct{}),
		busyConns:    make(map[Conn]*streamStatus),
		idleConns:    make(map[Conn]*streamStatus),
	}
}

func (p *Pool) maxStream() int {
	if m := p.opts.MaxStream; m > 0 {
		return m
	}
	return 1
}

func (p *Pool) maxIdleBusyRatio() float64 {
	if v := p.opts.MaxIdleBusyRatio; v > 0 {
		return v
	}
	return 1
}

type PoolStatus struct {
	Closed  bool
	Dialing int
	Busy    int
	Idle    int
}

func (p *Pool) Status() PoolStatus {
	p.m.Lock()
	defer p.m.Unlock()
	if p.closed {
		return PoolStatus{Closed: true}
	}

	return PoolStatus{
		Dialing: len(p.dialingCalls),
		Busy:    len(p.busyConns),
		Idle:    len(p.idleConns),
	}
}

// Get a connection from pool to open a stream.
// If there is an available connection, it returns immediately.
// Otherwise, it waits for a new connection.
// If Opts.MaxConn was set and we reached that limit, it returns ErrNoConnAvailable.
// If a new dialed connection return a unavailable Status, it returns ErrConnNotAvailable.
// User MUST call Pool.Release to release the connection once the stream was closed.
func (p *Pool) Get(ctx context.Context) (_ Conn, newConn bool, err error) {
	waitDialCall := func(dc *dialingCall, ctx context.Context) (conn Conn, newConn bool, err error) {
		conn, err = dc.waitConn(ctx)
		if err != nil {
			return nil, false, err
		}
		if !conn.Status().Available {
			p.Release(conn)
			return nil, false, ErrConnNotAvailable
		}
		conn.Reserve()
		return conn, true, nil
	}

	p.m.Lock()
	if p.closed {
		p.m.Unlock()
		return nil, false, ErrPoolClosed
	}

	// Try pick a busy conn from pool.
	conn := p.pickBusyConnLocked()
	if conn != nil {
		conn.Reserve()
		p.m.Unlock()
		return conn, false, nil
	}

	// Try pick an idle conn from pool.
	for conn, sc := range p.idleConns {
		cs := conn.Status()
		if cs.Closed {
			delete(p.idleConns, conn)
			continue
		}
		if !cs.Available {
			continue
		}

		sc.curStream++
		conn.Reserve() // FIX: was missing for idle conn path
		delete(p.idleConns, conn)
		p.busyConns[conn] = sc
		p.m.Unlock()
		return conn, false, nil
	}

	// Try to queue the request to the latest dial call.
	if dc := p.lastDialCall; dc != nil {
		dc.m.Lock()
		if dc.streamQueue < p.maxStream() {
			dc.streamQueue++
			dc.m.Unlock()
			p.m.Unlock()
			return waitDialCall(dc, ctx)
		}
		dc.m.Unlock()
	}

	// Dial new connection.
	// FIX: include busyConns in the total connection count.
	maxConn := p.opts.MaxConn
	if maxConn <= 0 || len(p.dialingCalls)+len(p.busyConns)+len(p.idleConns) < maxConn {
		dc := p.newDialCall()
		dc.streamQueue = 1
		p.lastDialCall = dc
		p.dialingCalls[dc] = struct{}{}
		p.m.Unlock()

		go dc.dial()
		return waitDialCall(dc, ctx)
	}

	// No conn available and we can't dial more connections.
	p.m.Unlock()
	return nil, false, ErrNoConnAvailable
}

// Release conn to pool.
func (p *Pool) Release(conn Conn) {
	closed := conn.Status().Closed

	p.m.Lock()
	sc, ok := p.busyConns[conn]
	if !ok {
		p.m.Unlock()
		return
	}
	if closed {
		delete(p.busyConns, conn)
		p.m.Unlock()
		return
	}

	sc.curStream--
	if sc.curStream == 0 {
		delete(p.busyConns, conn)
		p.idleConns[conn] = sc

		maxIdleAllowed := int(float64(len(p.busyConns)) * p.maxIdleBusyRatio())
		maxIdleAllowed = max(maxIdleAllowed, 1)

		var closingConns []Conn
		rm := len(p.idleConns) - maxIdleAllowed
		if rm > 0 {
			closingConns = make([]Conn, 0, rm)
			for idleConn := range p.idleConns {
				// FIX: skip the just-returned conn to avoid closing it immediately.
				if idleConn == conn {
					continue
				}
				delete(p.idleConns, idleConn)
				closingConns = append(closingConns, idleConn)
				rm--
				if rm <= 0 {
					break
				}
			}
		}
		p.m.Unlock()
		for _, closingConn := range closingConns {
			closingConn.Close()
		}
		return
	}
	p.m.Unlock()
}

// Remove conn from pool immediately.
func (p *Pool) MarkDead(conn Conn) {
	p.m.Lock()
	defer p.m.Unlock()
	delete(p.idleConns, conn)
	delete(p.busyConns, conn)
}

func (p *Pool) pickBusyConnLocked() Conn {
	searchSteps := 0
	var pickedConn Conn
	var pickedSc *streamStatus
	for conn, sc := range p.busyConns {
		cs := conn.Status()
		if cs.Closed {
			delete(p.busyConns, conn)
			continue
		}

		if searchSteps > maxSearchSteps {
			break
		}
		searchSteps++

		if !cs.Available || sc.curStream >= p.maxStream() {
			continue
		}

		if pickedConn == nil || sc.curStream > pickedSc.curStream {
			pickedConn = conn
			pickedSc = sc
			if sc.curStream == p.maxStream()-1 {
				break
			}
		}
	}
	if pickedConn != nil {
		pickedSc.curStream++
	}
	return pickedConn
}

// Close the pool and all its connections.
// Always returns nil.
func (p *Pool) Close() error {
	p.m.Lock()
	defer p.m.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true
	for dc := range p.dialingCalls {
		dc.cancelDial(ErrPoolClosed)
	}
	for conn := range p.busyConns {
		conn.Close()
	}
	for conn := range p.idleConns {
		conn.Close()
	}
	return nil
}

type dialingCall struct {
	p      *Pool
	ctx    context.Context
	cancel context.CancelFunc

	m             sync.Mutex
	streamQueue   int
	deliverNotify chan struct{}
	conn          Conn
	err           error
}

func (p *Pool) newDialCall() *dialingCall {
	ctx, cancel := context.WithCancel(context.Background())
	ls := &dialingCall{
		p:             p,
		ctx:           ctx,
		cancel:        cancel,
		deliverNotify: make(chan struct{}),
	}
	return ls
}

func (dc *dialingCall) dial() {
	p := dc.p
	defer dc.cancel()

	c, err := p.opts.Dial(dc.ctx)

	p.m.Lock()
	dc.m.Lock()
	if p.closed || dc.err != nil {
		p.m.Unlock()
		dc.m.Unlock()
		if c != nil {
			c.Close()
		}
		return
	}

	dc.conn = c
	dc.err = err
	close(dc.deliverNotify)

	if p.lastDialCall == dc {
		p.lastDialCall = nil
	}
	delete(p.dialingCalls, dc)

	if c != nil {
		ss := &streamStatus{curStream: dc.streamQueue}
		if ss.curStream == 0 {
			p.idleConns[c] = ss
		} else {
			p.busyConns[c] = ss
		}
	}
	p.m.Unlock()
	dc.m.Unlock()
}

func (dc *dialingCall) waitConn(ctx context.Context) (Conn, error) {
	select {
	case <-ctx.Done():
		dc.m.Lock()
		if dc.conn != nil || dc.err != nil {
			dc.m.Unlock()
			return dc.conn, dc.err
		}
		dc.streamQueue--
		dc.m.Unlock()
		return nil, context.Cause(ctx)
	case <-dc.deliverNotify:
		return dc.conn, dc.err
	}
}

var (
	errDialCanceled = errors.New("dial canceled")
)

func (dc *dialingCall) cancelDial(err error) {
	if err == nil {
		err = errDialCanceled
	}

	dc.m.Lock()
	defer dc.m.Unlock()
	if dc.conn != nil || dc.err != nil {
		return
	}
	dc.cancel()
	dc.err = err
	close(dc.deliverNotify)
}
