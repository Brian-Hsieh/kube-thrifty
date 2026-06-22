package utils

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

const capacity = 50

type Profiler struct {
	mu       sync.Mutex
	on       bool
	index    int
	capacity int
	data     []time.Duration
}

func NewProfiler(on bool) *Profiler {
	return &Profiler{
		on:       on,
		index:    0,
		capacity: capacity,
		data:     make([]time.Duration, 0, capacity),
	}
}

func (p *Profiler) set(t time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.data) < p.capacity {
		p.data = append(p.data, t)
		return
	}
	p.data[p.index] = t
	p.index = (p.index + 1) % p.capacity
}

func (p *Profiler) calRunningAvg() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	var total time.Duration
	for _, d := range p.data {
		total += d
	}
	return total.Seconds() / float64(p.capacity)
}

func (p *Profiler) Perf() func() {
	if !p.on {
		return func() {}
	}

	start := time.Now()

	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		slog.Error("Profiler.Perf should be called inside a function")
		return func() {}
	}

	fun := runtime.FuncForPC(pc)
	funcName := "nil"
	if fun != nil {
		funcName = fun.Name()
	}

	return func() {
		p.set(time.Since(start))
		if len(p.data) < p.capacity {
			return
		}
		s := fmt.Sprintf("function name: %s | running avg of %v points: %vs", funcName, p.capacity, p.calRunningAvg())
		r := slog.NewRecord(time.Now(), slog.LevelDebug, s, pc)
		_ = slog.Default().Handler().Handle(context.Background(), r)
	}
}
