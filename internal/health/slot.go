package health

import (
	"sync/atomic"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
)

// Unavailable — причина, по которой в канал сейчас нельзя отправлять запросы.
// Пустая строка означает, что канал доступен.
type Unavailable string

const (
	BreakerOpen Unavailable = "breaker_open" // предохранитель отключил канал или идут пробы
	Overloaded  Unavailable = "overloaded"   // все места под одновременные запросы заняты
)

// Slot — запрос, допущенный в канал. Его обязательно закрыть одним из двух
// способов: Done, когда канал ответил, или Cancel, если запрос так и не ушёл.
// Повторные вызовы ничего не делают.
type Slot struct {
	r       *Registry
	id      string
	breaker *Breaker
	used    atomic.Bool
}

// Done закрывает слот ответом канала: исход идёт в статистику и предохранитель.
func (s *Slot) Done(o channel.ChargeOutcome, latency time.Duration) {
	if !s.used.CompareAndSwap(false, true) {
		return
	}
	s.r.mu.Lock()
	s.r.stats(s.id).inflight--
	s.r.recordLocked(s.id, o, latency)
	s.r.mu.Unlock()
	if s.breaker != nil {
		s.breaker.Record(o)
	}
}

// Cancel закрывает слот без исхода: статистика не меняется, проба возвращается.
func (s *Slot) Cancel() {
	if !s.used.CompareAndSwap(false, true) {
		return
	}
	s.r.release(s.id)
	if s.breaker != nil {
		s.breaker.Cancel()
	}
}

// reserveLimited занимает место под запрос, если лимит одновременных запросов
// канала (см. Register) не исчерпан. Проверка и занятие — одним действием.
func (r *Registry) reserveLimited(channelID string) (*Slot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats(channelID)
	if s.maxInflight > 0 && s.inflight >= s.maxInflight {
		return nil, false
	}
	s.inflight++
	return &Slot{r: r, id: channelID, breaker: s.breaker}, true
}

// reserve занимает место под запрос без проверок.
func (r *Registry) reserve(channelID string) *Slot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats(channelID)
	s.inflight++
	return &Slot{r: r, id: channelID, breaker: s.breaker}
}

func (r *Registry) release(channelID string) {
	r.mu.Lock()
	r.stats(channelID).inflight--
	r.mu.Unlock()
}

// TryBegin пускает запрос в канал, если не исчерпан лимит одновременных запросов
// и предохранитель не против. При отказе слот nil, а причина в Unavailable.
// В состоянии HALF_OPEN запрос занимает пробу.
func (r *Registry) TryBegin(channelID string) (*Slot, Unavailable) {
	slot, ok := r.reserveLimited(channelID)
	if !ok {
		return nil, Overloaded
	}
	if slot.breaker != nil && !slot.breaker.Allow() {
		slot.used.Store(true)
		r.release(channelID)
		return nil, BreakerOpen
	}
	return slot, ""
}

// Unavailable сообщает, почему канал сейчас недоступен; пустая строка — доступен.
// Ничего не занимает: между проверкой и TryBegin место может уйти другому.
func (r *Registry) Unavailable(channelID string) Unavailable {
	r.mu.Lock()
	s := r.stats(channelID)
	b, full := s.breaker, s.maxInflight > 0 && s.inflight >= s.maxInflight
	r.mu.Unlock()
	switch {
	case b != nil && !b.CanAdmit():
		return BreakerOpen
	case full:
		return Overloaded
	}
	return ""
}

// State — состояние предохранителя канала; CLOSED, если предохранители выключены.
func (r *Registry) State(channelID string) State {
	r.mu.Lock()
	b := r.stats(channelID).breaker
	r.mu.Unlock()
	if b == nil {
		return Closed
	}
	return b.State()
}
