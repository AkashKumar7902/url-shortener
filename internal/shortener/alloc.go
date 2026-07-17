package shortener

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// IDAllocator hands out unique, monotonic-ish ids. Implementations decide
// whether ids come from a DB sequence (batched) or an in-process counter.
type IDAllocator interface {
	Next(ctx context.Context) (uint64, error)
}

// BlockAllocator implements Option A (Hi/Lo): it fetches a block of ids in one
// round trip and serves them from memory, refilling when drained. nextval
// guarantees every block is disjoint across requests and instances, so
// generated codes never collide with each other. Blocks are held as an explicit
// slice because concurrent callers can make a batch non-contiguous.
type BlockAllocator struct {
	mu     sync.Mutex
	ids    []uint64
	pos    int
	size   uint64
	refill func(ctx context.Context, n uint64) ([]uint64, error)
}

// NewBlockAllocator builds a block allocator. refill must return up to n unique
// ids in one round trip (e.g. SELECT nextval(seq) FROM generate_series(1,n)).
func NewBlockAllocator(size uint64, refill func(ctx context.Context, n uint64) ([]uint64, error)) *BlockAllocator {
	if size == 0 {
		size = 100
	}
	return &BlockAllocator{size: size, refill: refill}
}

func (a *BlockAllocator) Next(ctx context.Context) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pos >= len(a.ids) {
		ids, err := a.refill(ctx, a.size)
		if err != nil {
			return 0, err
		}
		if len(ids) == 0 {
			return 0, errors.New("shortener: id allocator returned an empty block")
		}
		a.ids, a.pos = ids, 0
	}
	id := a.ids[a.pos]
	a.pos++
	return id, nil
}

// CounterAllocator is an in-process monotonic counter for single-process
// deployments (e.g. the in-memory store) where no shared sequence exists.
type CounterAllocator struct{ n atomic.Uint64 }

// NewCounterAllocator starts allocating at start+1.
func NewCounterAllocator(start uint64) *CounterAllocator {
	c := &CounterAllocator{}
	c.n.Store(start)
	return c
}

func (c *CounterAllocator) Next(context.Context) (uint64, error) {
	return c.n.Add(1), nil
}
