package utils

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

type Profiler struct {
	mu   sync.Mutex
	on   bool
	data map[string][30]float64
}

func NewProfiler(on bool) *Profiler {
	return &Profiler{
		on:   on,
		data: make(map[string][30]float64),
	}
}

// TODO: implement
func (p *Profiler) set(funcN string, value float64) {
}

// TODO: we need to print running avg of 30 or less sample points
func (p *Profiler) Perf() func() {
	if !p.on {
		return func() {}
	}

	start := time.Now()

	pc, _, _, ok := runtime.Caller(1)
	fun := runtime.FuncForPC(pc)
	funcName := "nil"
	if fun != nil {
		funcName = fun.Name()
	}
	if !ok {
		return func() {
			slog.Debug("Execution time", "func", funcName, "time_duration(sec)", time.Since(start).Seconds())
		}
	}

	return func() {
		s := fmt.Sprintf("function name: %s | time duration: %vs", funcName, time.Since(start).Seconds())
		r := slog.NewRecord(time.Now(), slog.LevelDebug, s, pc)
		_ = slog.Default().Handler().Handle(context.Background(), r)
	}
}
