package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/netpoll"
)

// RefCountMockConn for testing
type RefCountMockConn struct {
	netpoll.Connection
	active bool
}

func (m *RefCountMockConn) IsActive() bool {
	return m.active
}

func (m *RefCountMockConn) Close() error {
	m.active = false
	return nil
}

func TestRefCount_RaceCondition(t *testing.T) {
	e := NewEngine(nil)

	// Simulate OnPrepare
	s := NewConnectionState(time.Second)
	if s.refCount != 1 {
		t.Fatalf("Initial RefCount should be 1, got %d", s.refCount)
	}

	// Use RefCountMockConn
	// conn := &RefCountMockConn{active: true}

	var wg sync.WaitGroup
	wg.Add(1)
	acquired := make(chan struct{})
	releaseWorker := make(chan struct{})

	// Simulate ServeConn running in a separate goroutine
	go func() {
		defer wg.Done()
		e.AcquireConnectionState(s) // Ref -> 2
		if atomic.LoadInt32(&s.refCount) != 2 {
			t.Errorf("RefCount inside goroutine should be 2")
		}
		close(acquired)

		<-releaseWorker
		e.ReleaseConnectionState(s) // Ref -> 1 (if Disconnect happened) or 0 (if not)
	}()

	// Simulate OnDisconnect happening concurrently
	<-acquired
	e.ReleaseConnectionState(s) // Ref -> 1

	if got := atomic.LoadInt32(&s.refCount); got != 1 {
		t.Errorf("RefCount should be 1 before the final release, got %d", got)
	}

	close(releaseWorker)
	wg.Wait()
}

func TestConnectionState_Cancel_Concurrent_NoRace(t *testing.T) {
	state := NewConnectionState(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state.Cancel()
		}()
	}
	wg.Wait()

	if err := state.Err(); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	select {
	case <-state.Done():
	default:
		t.Fatalf("expected done channel to be closed")
	}
}
