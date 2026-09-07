package view

import "sync"

// logRecordingRegistry owns background recording work independently of the
// currently displayed page so application exit can deterministically drain it.
type logRecordingRegistry struct {
	mu          sync.Mutex
	workbenches map[*logWorkbench]struct{}
	starts      map[*logRecordingStart]struct{}
	writers     map[*logWriter]struct{}
	wg          sync.WaitGroup
	stopping    bool
}

func (a *App) registerLogWorkbench(w *logWorkbench) {
	if a == nil || w == nil {
		return
	}
	r := &a.logRecordings
	r.mu.Lock()
	if r.workbenches == nil {
		r.workbenches = make(map[*logWorkbench]struct{})
	}
	if !r.stopping {
		r.workbenches[w] = struct{}{}
	}
	r.mu.Unlock()
}

func (a *App) unregisterLogWorkbench(w *logWorkbench) {
	if a == nil {
		return
	}
	a.logRecordings.mu.Lock()
	delete(a.logRecordings.workbenches, w)
	a.logRecordings.mu.Unlock()
}

func (a *App) registerLogStart(op *logRecordingStart) bool {
	if a == nil || op == nil {
		return true
	}
	r := &a.logRecordings
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		op.cancel()
		<-op.done
		return false
	}
	if r.starts == nil {
		r.starts = make(map[*logRecordingStart]struct{})
	}
	r.starts[op] = struct{}{}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		<-op.done
		r.mu.Lock()
		delete(r.starts, op)
		r.mu.Unlock()
		r.wg.Done()
	}()
	return true
}

func (a *App) registerLogWriter(wr *logWriter) bool {
	if a == nil || wr == nil {
		return true
	}
	r := &a.logRecordings
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		wr.close()
		<-wr.done
		return false
	}
	if r.writers == nil {
		r.writers = make(map[*logWriter]struct{})
	}
	r.writers[wr] = struct{}{}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		<-wr.done
		r.mu.Lock()
		delete(r.writers, wr)
		r.mu.Unlock()
		r.wg.Done()
	}()
	return true
}

func (a *App) shutdownLogRecordings() {
	if a == nil {
		return
	}
	r := &a.logRecordings
	r.mu.Lock()
	r.stopping = true
	workbenches := make([]*logWorkbench, 0, len(r.workbenches))
	for w := range r.workbenches {
		workbenches = append(workbenches, w)
	}
	starts := make([]*logRecordingStart, 0, len(r.starts))
	for op := range r.starts {
		starts = append(starts, op)
	}
	writers := make([]*logWriter, 0, len(r.writers))
	for wr := range r.writers {
		writers = append(writers, wr)
	}
	r.mu.Unlock()

	for _, w := range workbenches {
		w.stop()
	}
	for _, op := range starts {
		op.cancel()
	}
	for _, wr := range writers {
		wr.close()
	}
	r.wg.Wait()
}
