package pool

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sync"
)

func Go(fn func()) {
	go fn()
}

type Buffer []byte

func GetBuf(size int) Buffer {
	return getBuf(size)
}

func ReleaseBuf(b Buffer) {
	releaseBuf(b)
}

func CopyBuf(b []byte) Buffer {
	bb := GetBuf(len(b))
	copy(bb, b)
	return bb
}

type BytesBufPool struct {
	p sync.Pool
}

func NewBytesBufPool(initSize int) *BytesBufPool {
	if initSize < 0 {
		panic(fmt.Sprintf("utils.NewBytesBufPool: negative init size %d", initSize))
	}

	return &BytesBufPool{
		p: sync.Pool{New: func() any {
			b := new(bytes.Buffer)
			b.Grow(initSize)
			return b
		}},
	}
}

func (p *BytesBufPool) Get() *bytes.Buffer {
	return p.p.Get().(*bytes.Buffer)
}

func (p *BytesBufPool) Release(b *bytes.Buffer) {
	b.Reset()
	p.p.Put(b)
}

var br1kPool = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 1024) }}

func NewBR1K(r io.Reader) *bufio.Reader {
	br := br1kPool.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

func ReleaseBR1K(br *bufio.Reader) {
	br.Reset(nil)
	br1kPool.Put(br)
}

type BytesPool struct {
	sp sync.Pool
}

func NewBytesPool() *BytesPool {
	return &BytesPool{sp: sync.Pool{New: func() any { return new(Bytes) }}}
}

type Bytes struct {
	B []byte
}

func (p *BytesPool) Get() *Bytes {
	return p.sp.Get().(*Bytes)
}

func (p *BytesPool) Release(b *Bytes) {
	if b.B != nil {
		b.B = b.B[:0]
	}
	p.sp.Put(b)
}
