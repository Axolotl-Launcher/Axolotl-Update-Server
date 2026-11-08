package analytics

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type Sink interface {
	Write(context.Context, []Event) error
}
type Counters struct {
	Written, FailedBatches, Dropped uint64
	QueueLength                     int
}
type Collector struct {
	sink                     Sink
	queue                    chan Event
	batchSize                int
	interval                 time.Duration
	stop                     chan struct{}
	done                     chan struct{}
	ctx                      context.Context
	cancel                   context.CancelFunc
	mu                       sync.RWMutex
	closed                   bool
	written, failed, dropped atomic.Uint64
}

func NewCollector(sink Sink, capacity, batch int, interval time.Duration) *Collector {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{sink: sink, queue: make(chan Event, capacity), batchSize: batch, interval: interval, stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go c.run()
	return c
}
func (c *Collector) Enqueue(e Event) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		c.dropped.Add(1)
		return
	}
	select {
	case c.queue <- e:
	default:
		c.dropped.Add(1)
	}
}
func (c *Collector) Stats() Counters {
	return Counters{c.written.Load(), c.failed.Load(), c.dropped.Load(), len(c.queue)}
}
func (c *Collector) Close(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.stop)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		c.cancel()
		<-c.done
		return ctx.Err()
	}
}
func (c *Collector) run() {
	defer close(c.done)
	defer c.cancel()
	tick := time.NewTicker(c.interval)
	defer tick.Stop()
	logTick := time.NewTicker(time.Minute)
	defer logTick.Stop()
	batch := make([]Event, 0, c.batchSize)
	nextAttempt := time.Time{}
	backoff := time.Second
	stopping := false
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		e := c.sink.Write(ctx, batch)
		cancel()
		if e != nil {
			c.failed.Add(1)
			slog.Warn("analytics batch write failed", "count", len(batch), "error", e)
			nextAttempt = time.Now().Add(backoff)
			backoff = min(backoff*2, 30*time.Second)
			return false
		}
		c.written.Add(uint64(len(batch)))
		clear(batch)
		batch = batch[:0]
		backoff = time.Second
		nextAttempt = time.Time{}
		return true
	}
	for {
		if stopping {
			for len(batch) < c.batchSize {
				select {
				case e := <-c.queue:
					batch = append(batch, e)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) == 0 {
				return
			}
			if time.Now().Before(nextAttempt) {
				timer := time.NewTimer(time.Until(nextAttempt))
				select {
				case <-timer.C:
				case <-c.ctx.Done():
					timer.Stop()
					c.dropped.Add(uint64(len(batch) + len(c.queue)))
					return
				}
			}
			if c.ctx.Err() != nil {
				c.dropped.Add(uint64(len(batch) + len(c.queue)))
				return
			}
			flush()
			continue
		}
		input := c.queue
		if len(batch) >= c.batchSize {
			input = nil
		}
		select {
		case e := <-input:
			batch = append(batch, e)
			if len(batch) >= c.batchSize && !time.Now().Before(nextAttempt) {
				flush()
			}
		case <-tick.C:
			if !time.Now().Before(nextAttempt) {
				flush()
			}
		case <-logTick.C:
			slog.Info("analytics counters", "stats", c.Stats())
		case <-c.stop:
			stopping = true
		case <-c.ctx.Done():
			c.dropped.Add(uint64(len(batch) + len(c.queue)))
			return
		}
	}
}
