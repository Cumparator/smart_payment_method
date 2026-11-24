package health

import (
	"fmt"
	"sync"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

// State — состояние предохранителя канала. Значения совпадают с channel_events.
type State string

const (
	Closed   State = "CLOSED"    // канал работает, запросы идут как обычно
	Open     State = "OPEN"      // канал отключён, запросы не пускаем
	HalfOpen State = "HALF_OPEN" // пауза прошла, пускаем пробный запрос
)

// BreakerConfig — когда предохранитель отключает канал. Нулевые поля заменяются
// значениями по умолчанию.
type BreakerConfig struct {
	ConsecutiveFailures int           // неудач подряд, после которых канал отключается (5)
	Window              int           // сколько последних исходов канала смотрим (20)
	MinAttempts         int           // сколько исходов в окне нужно, чтобы судить по доле (10)
	FailureRate         float64       // доля неудач в окне, после которой канал отключается (0.5)
	OpenFor             time.Duration // сколько канал отключён до пробного запроса (30 с)
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.ConsecutiveFailures <= 0 {
		c.ConsecutiveFailures = 5
	}
	if c.Window <= 0 {
		c.Window = 20
	}
	if c.MinAttempts <= 0 {
		c.MinAttempts = 10
	}
	if c.FailureRate <= 0 {
		c.FailureRate = 0.5
	}
	if c.OpenFor <= 0 {
		c.OpenFor = 30 * time.Second
	}
	return c
}

// Transition — смена состояния предохранителя.
type Transition struct {
	ChannelID string
	From, To  State
	At        time.Time
	Reason    string
}

// Breaker — предохранитель одного канала: отключает его при частых технических
// ошибках и включает обратно, когда пробный запрос проходит.
//
// Неудача — это CHANNEL_ERROR и UNKNOWN (таймаут). Бизнес-отказ DECLINED
// неудачей не считается: канал ответил, значит жив.
type Breaker struct {
	id       string
	cfg      BreakerConfig
	clock    clock.Clock
	onChange func(Transition)

	mu       sync.Mutex
	state    State
	ring     []bool // окно последних исходов в CLOSED, true — неудача
	head     int
	filled   int
	fails    int // неудач в окне
	consec   int // неудач подряд
	openedAt time.Time
	probing  bool // пробный запрос в полёте
}

// NewBreaker создаёт предохранитель в состоянии CLOSED. onChange вызывается после
// каждой смены состояния, вне внутренней блокировки; может быть nil.
func NewBreaker(channelID string, cfg BreakerConfig, clk clock.Clock, onChange func(Transition)) *Breaker {
	cfg = cfg.withDefaults()
	if clk == nil {
		clk = clock.Real{}
	}
	return &Breaker{id: channelID, cfg: cfg, clock: clk, onChange: onChange, state: Closed, ring: make([]bool, cfg.Window)}
}

func isFailure(o channel.ChargeOutcome) bool {
	return o != channel.Approved && o != channel.Declined
}

// State возвращает текущее состояние; если пауза отключения прошла — HALF_OPEN.
func (b *Breaker) State() State {
	b.mu.Lock()
	tr := b.refreshLocked(b.clock.Now())
	st := b.state
	b.mu.Unlock()
	b.emit(tr)
	return st
}

// Allow решает, пускать ли запрос в канал. В HALF_OPEN пускает один пробный запрос.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	tr := b.refreshLocked(b.clock.Now())
	ok := false
	switch b.state {
	case Closed:
		ok = true
	case HalfOpen:
		if !b.probing {
			b.probing, ok = true, true
		}
	}
	b.mu.Unlock()
	b.emit(tr)
	return ok
}

// Record учитывает исход запроса, пущенного через Allow.
func (b *Breaker) Record(o channel.ChargeOutcome) {
	b.mu.Lock()
	now := b.clock.Now()
	tr := b.refreshLocked(now)
	failure := isFailure(o)
	var t *Transition
	switch b.state {
	case Closed:
		t = b.recordClosedLocked(now, failure)
	case HalfOpen:
		t = b.recordProbeLocked(now, failure)
	}
	if t != nil {
		tr = append(tr, *t)
	}
	b.mu.Unlock()
	b.emit(tr)
}

// refreshLocked переводит OPEN в HALF_OPEN, когда пауза прошла.
func (b *Breaker) refreshLocked(now time.Time) []Transition {
	if b.state == Open && now.Sub(b.openedAt) >= b.cfg.OpenFor {
		return []Transition{b.moveLocked(HalfOpen, now, "пауза прошла, пробуем канал")}
	}
	return nil
}

func (b *Breaker) recordClosedLocked(now time.Time, failure bool) *Transition {
	if b.filled == len(b.ring) {
		if b.ring[b.head] {
			b.fails--
		}
	} else {
		b.filled++
	}
	b.ring[b.head] = failure
	b.head = (b.head + 1) % len(b.ring)
	if failure {
		b.fails++
		b.consec++
	} else {
		b.consec = 0
	}

	var reason string
	switch {
	case b.consec >= b.cfg.ConsecutiveFailures:
		reason = fmt.Sprintf("неудач подряд: %d", b.consec)
	case b.filled >= b.cfg.MinAttempts && float64(b.fails) >= b.cfg.FailureRate*float64(b.filled):
		reason = fmt.Sprintf("неудач %d из последних %d", b.fails, b.filled)
	default:
		return nil
	}
	t := b.moveLocked(Open, now, reason)
	return &t
}

func (b *Breaker) recordProbeLocked(now time.Time, failure bool) *Transition {
	b.probing = false
	if failure {
		t := b.moveLocked(Open, now, "пробный запрос не прошёл")
		return &t
	}
	t := b.moveLocked(Closed, now, "пробный запрос прошёл")
	return &t
}

// moveLocked меняет состояние и приводит счётчики в порядок для нового.
func (b *Breaker) moveLocked(to State, now time.Time, reason string) Transition {
	t := Transition{ChannelID: b.id, From: b.state, To: to, At: now, Reason: reason}
	b.state = to
	b.probing = false
	switch to {
	case Open:
		b.openedAt = now
	case Closed:
		b.head, b.filled, b.fails, b.consec = 0, 0, 0, 0
	}
	return t
}

func (b *Breaker) emit(tr []Transition) {
	if b.onChange == nil {
		return
	}
	for _, t := range tr {
		b.onChange(t)
	}
}
