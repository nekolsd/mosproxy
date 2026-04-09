package connpool

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type dummyConn struct {
	m         sync.Mutex
	closed    bool
	available bool
}

func (c *dummyConn) Status() ConnStatus {
	c.m.Lock()
	defer c.m.Unlock()
	return ConnStatus{
		Closed:    c.closed,
		Available: c.available,
	}
}
func (c *dummyConn) Close() error {
	c.m.Lock()
	c.closed = true
	c.m.Unlock()
	return nil
}
func (c *dummyConn) Reserve() {}

func Test_Pool(t *testing.T) {
	r := require.New(t)
	p := NewPool(Opts{
		Dial: func(ctx context.Context) (Conn, error) {
			time.Sleep(time.Millisecond * time.Duration(rand.IntN(20)))
			return &dummyConn{available: true}, nil
		},
		MaxStream:        10,
		MaxIdleBusyRatio: 1,
	})

	var cm sync.Mutex
	conns := make([]Conn, 0)
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, _, err := p.Get(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			cm.Lock()
			conns = append(conns, conn)
			cm.Unlock()
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	r.True(p.Status().Busy >= 100)
	r.True(p.Status().Idle == 0)
	r.True(p.Status().Dialing == 0)

	for _, conn := range conns {
		p.Release(conn)
	}
	r.Equal(0, p.Status().Busy)
	r.Equal(1, p.Status().Idle)
	r.Equal(0, p.Status().Dialing)
}

func Test_Pool_Reuse_Efficiency(t *testing.T) {
	r := require.New(t)
	p := NewPool(Opts{
		Dial:             func(ctx context.Context) (Conn, error) { return &dummyConn{available: true}, nil },
		MaxStream:        1000,
		MaxIdleBusyRatio: 1,
	})

	conns := make([]Conn, 0)
	p.m.Lock()
	for i := 0; i < 1000; i++ {
		conn := &dummyConn{}
		conns = append(conns, conn)
		p.busyConns[conn] = &streamStatus{curStream: 1}
	}
	p.m.Unlock()

	for i := 0; i < 100000; i++ {
		randIdx := rand.IntN(len(conns))
		conn := conns[randIdx]
		p.Release(conn)
		nc, _, err := p.Get(context.Background())
		r.NoError(err)
		conns[randIdx] = nc
	}
	r.Less(p.Status().Busy, 2)
	r.Less(p.Status().Idle, 2)
}

func Test_Pool_MaxConn(t *testing.T) {
	r := require.New(t)

	const maxConn = 3
	p := NewPool(Opts{
		Dial: func(ctx context.Context) (Conn, error) {
			return &dummyConn{available: true}, nil
		},
		MaxConn:   maxConn,
		MaxStream: 1,
	})

	conns := make([]Conn, 0, maxConn)
	for i := 0; i < maxConn; i++ {
		c, _, err := p.Get(context.Background())
		r.NoError(err)
		conns = append(conns, c)
	}

	// All connections are busy; the next Get should fail.
	_, _, err := p.Get(context.Background())
	r.ErrorIs(err, ErrNoConnAvailable)

	for _, c := range conns {
		p.Release(c)
	}
}
