package ble

import "sync"

type valueQueue struct {
	mu      sync.Mutex
	pending [][]byte
	closed  bool

	out     chan []byte
	wake    chan struct{}
	done    chan struct{}
	stopped chan struct{}

	closeOnce sync.Once
}

func newValueQueue() *valueQueue {
	q := &valueQueue{
		out:     make(chan []byte),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go q.run()
	return q
}

func (q *valueQueue) Send(value []byte) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.pending = append(q.pending, value)
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *valueQueue) Listen() <-chan []byte {
	return q.out
}

func (q *valueQueue) Close() {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.pending = nil
		q.mu.Unlock()
		close(q.done)
		<-q.stopped
	})
}

func (q *valueQueue) run() {
	defer close(q.stopped)
	defer close(q.out)

	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.mu.Unlock()
			select {
			case <-q.wake:
				continue
			case <-q.done:
				return
			}
		}

		value := q.pending[0]
		q.pending[0] = nil
		if len(q.pending) == 1 {
			q.pending = nil
		} else {
			q.pending = q.pending[1:]
		}
		q.mu.Unlock()

		select {
		case q.out <- value:
		case <-q.done:
			return
		}
	}
}
