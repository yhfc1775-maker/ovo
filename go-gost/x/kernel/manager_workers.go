package kernel

import (
	"context"
	"sync"
	"time"
)

type managerWorkerGroup struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started map[string]bool
	stopped bool
}

func newManagerWorkerGroup() *managerWorkerGroup {
	ctx, cancel := context.WithCancel(context.Background())
	return &managerWorkerGroup{ctx: ctx, cancel: cancel, started: make(map[string]bool)}
}

// All manager-owned periodic jobs share one cancel/join boundary. Registration
// and WaitGroup.Add are serialized with stop, so Shutdown cannot miss a late job.
func (m *Manager) startManagerWorker(name string, run func(context.Context)) {
	m.workersMu.Lock()
	defer m.workersMu.Unlock()
	if m.workerJobs == nil {
		m.workerJobs = make(map[string]func(context.Context))
	}
	m.workerJobs[name] = run
	if m.workers == nil {
		m.workers = newManagerWorkerGroup()
	}
	m.launchManagerWorkerLocked(name, run)
}

func (m *Manager) launchManagerWorkerLocked(name string, run func(context.Context)) {
	group := m.workers
	if group.stopped || group.started[name] {
		return
	}
	group.started[name] = true
	group.wg.Add(1)
	go func() {
		defer group.wg.Done()
		run(group.ctx)
	}()
}

// Called only by an explicit lifecycle operation while lifecycleMu is held for
// reading. Reusing a stopped Manager retains the pre-existing Apply/Control API.
func (m *Manager) resumeManagerWorkers() {
	m.workersMu.Lock()
	defer m.workersMu.Unlock()
	if m.workers == nil || !m.workers.stopped {
		return
	}
	m.workers = newManagerWorkerGroup()
	for name, run := range m.workerJobs {
		m.launchManagerWorkerLocked(name, run)
	}
}

// Call before taking applyMu: geo/rule-set jobs may be finishing a swap under
// that mutex. lifecycleMu excludes new public operations until join completes.
func (m *Manager) stopManagerWorkers() {
	m.workersMu.Lock()
	if m.workers == nil {
		m.workers = newManagerWorkerGroup()
	}
	group := m.workers
	group.stopped = true
	group.cancel()
	m.workersMu.Unlock()
	group.wg.Wait()
}

func waitManagerWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
