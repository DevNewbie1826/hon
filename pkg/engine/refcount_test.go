package engine

import (
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

	// Simulate ServeConn running in a separate goroutine
	go func() {
		defer wg.Done()
		e.AcquireConnectionState(s) // Ref -> 2
		if atomic.LoadInt32(&s.refCount) != 2 {
			t.Errorf("RefCount inside goroutine should be 2")
		}

		// Simulate some processing time
		time.Sleep(100 * time.Millisecond)

		e.ReleaseConnectionState(s) // Ref -> 1 (if Disconnect happened) or 0 (if not)
	}()

	// Simulate OnDisconnect happening concurrently
	time.Sleep(50 * time.Millisecond) // Wait for goroutine to acquire
	e.ReleaseConnectionState(s)       // Ref -> 1

	if atomic.LoadInt32(&s.refCount) == 0 {
		t.Errorf("RefCount should not be 0 yet")
	}

	wg.Wait()

	// After goroutine finishes, RefCount should be 0 and returned to pool
	// Use a trick to check if it's reset: s.ReadTimeout should be 0
	// Note: s pointer is technically invalid if in pool, but for this test we check the struct state
	// assuming no one else grabbed it from pool yet.
	// In a real high-concurrency scenario this check is flaky, but valid for single unit test.
	if atomic.LoadInt32(&s.refCount) != 0 {
		t.Errorf("Final RefCount should be 0, got %d", s.refCount)
	}
	if s.ReadTimeout != 0 {
		t.Errorf("State should be reset (ReadTimeout=0)")
	}
}
