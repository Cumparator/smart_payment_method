// Package clock — источник времени. Доменная логика не вызывает time.Now()
// напрямую, чтобы эксперименты могли идти на виртуальных часах.
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Virtual — часы, которые двигает сам прогон эксперимента.
type Virtual struct {
	mu  sync.Mutex
	now time.Time
}

func NewVirtual(start time.Time) *Virtual {
	return &Virtual{now: start}
}

func (v *Virtual) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.now
}

func (v *Virtual) Advance(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.now = v.now.Add(d)
}
