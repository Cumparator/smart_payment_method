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
	OpenFor             time.Duration // сколько канал отключён до пробных запросов (30 с)
	MaxOpenFor          time.Duration // потолок паузы: после каждой неудачной пробы она удваивается (5 мин)
	Probes              int           // пробных запросов одновременно в HALF_OPEN (1)
	ProbeSuccesses      int           // удачных проб подряд, после которых канал включается (3)
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
	if c.MaxOpenFor < c.OpenFor {
		c.MaxOpenFor = max(c.OpenFor, 5*time.Minute)
	}
	if c.Probes <= 0 {
		c.Probes = 1
	}
	if c.ProbeSuccesses <= 0 {
		c.ProbeSuccesses = 3
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
// ошибках и включает обратно осторожно. Когда пауза проходит, канал получает
// несколько пробных запросов (не больше Probes одновременно); он включается,
// только если подряд прошли ProbeSuccesses проб. Любая неудачная проба снова
// отключает канал, и пауза растёт вдвое, до MaxOpenFor.
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
	openFor  time.Duration // текущая пауза; растёт после неудачных проб
	probing  int           // пробных запросов в полёте
	probeOK  int           // удачных проб подряд
}

// NewBreaker создаёт предохранитель в состоянии CLOSED. onChange вызывается после
// каждой смены состояния, вне внутренней блокировки; может быть nil.
func NewBreaker(channelID string, cfg BreakerConfig, clk clock.Clock, onChange func(Transition)) *Breaker {
	cfg = cfg.withDefaults()
	if clk == nil {
		clk = clock.Real{}
	}
	return &Breaker{id: channelID, cfg: cfg, clock: clk, onChange: onChange, state: Closed, openFor: cfg.OpenFor, ring: make([]bool, cfg.Window)}
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

// Allow решает, пускать ли запрос в канал. В HALF_OPEN пускает пробные запросы,
// пока их в полёте меньше Probes; за каждый пущенный нужно вызвать Record или Cancel.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	tr := b.refreshLocked(b.clock.Now())
	ok := false
	switch b.state {
	case Closed:
		ok = true
	case HalfOpen:
		if b.probing < b.cfg.Probes {
			b.probing++
			ok = true
		}
	}
	b.mu.Unlock()
	b.emit(tr)
	return ok
}

// CanAdmit — то же, что Allow, но ничего не занимает: можно ли пустить запрос сейчас.
func (b *Breaker) CanAdmit() bool {
	b.mu.Lock()
	tr := b.refreshLocked(b.clock.Now())
	ok := b.state == Closed || (b.state == HalfOpen && b.probing < b.cfg.Probes)
	b.mu.Unlock()
	b.emit(tr)
	return ok
}

// Cancel возвращает пробу, занятую в Allow, если запрос так и не ушёл в канал.
func (b *Breaker) Cancel() {
	b.mu.Lock()
	if b.state == HalfOpen && b.probing > 0 {
		b.probing--
	}
	b.mu.Unlock()
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
	if b.state == Open && now.Sub(b.openedAt) >= b.openFor {
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
	if b.probing > 0 {
		b.probing--
	}
	if failure {
		b.openFor = min(b.openFor*2, b.cfg.MaxOpenFor)
		t := b.moveLocked(Open, now, fmt.Sprintf("пробный запрос не прошёл, следующая пауза %s", b.openFor))
		return &t
	}
	b.probeOK++
	if b.probeOK < b.cfg.ProbeSuccesses {
		return nil
	}
	t := b.moveLocked(Closed, now, fmt.Sprintf("проб подряд прошло: %d", b.probeOK))
	return &t
}

// moveLocked меняет состояние и приводит счётчики в порядок для нового.
func (b *Breaker) moveLocked(to State, now time.Time, reason string) Transition {
	t := Transition{ChannelID: b.id, From: b.state, To: to, At: now, Reason: reason}
	b.state = to
	b.probing, b.probeOK = 0, 0
	switch to {
	case Open:
		b.openedAt = now
	case Closed:
		b.head, b.filled, b.fails, b.consec = 0, 0, 0, 0
		b.openFor = b.cfg.OpenFor
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
