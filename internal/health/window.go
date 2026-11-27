package health

import (
	"sync"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

// DefaultWindow — размер окна из статьи arXiv:2308.01028 (SW-UCB, W = 200).
const DefaultWindow = 200

type observation struct {
	channelID string
	success   bool
}

type channelStats struct {
	successes int
	attempts  int

	latencyS        float64 // сглаженная задержка ответа, секунды
	hasLatency      bool
	defaultLatencyS float64 // пока замеров нет
	inflight        int
	maxInflight     int // 0 — лимит не задан
	breaker         *Breaker
}

// Registry — статистика каналов по скользящему окну последних технических
// исходов по всем каналам вместе. Канал, который давно не выбирали, выпадает
// из окна, и его статистика обнуляется: стратегии придётся его перепроверить.
type Registry struct {
	mu     sync.Mutex
	ring   []observation
	head   int // куда писать следующий исход
	filled int
	chans  map[string]*channelStats

	clock    clock.Clock
	breaker  *BreakerConfig // nil — предохранители выключены
	onChange func(Transition)
}

// Option настраивает реестр.
type Option func(*Registry)

// WithBreaker включает предохранитель на каждом канале с заданными порогами.
func WithBreaker(cfg BreakerConfig) Option {
	return func(r *Registry) { r.breaker = &cfg }
}

// WithClock задаёт часы предохранителей; по умолчанию реальные.
func WithClock(clk clock.Clock) Option {
	return func(r *Registry) { r.clock = clk }
}

// OnStateChange вызывает fn при каждой смене состояния любого предохранителя.
// Вызывается вне блокировок реестра, поэтому fn может обращаться к нему сама.
func OnStateChange(fn func(Transition)) Option {
	return func(r *Registry) { r.onChange = fn }
}

// New создаёт реестр с окном на window исходов; при window <= 0 берётся DefaultWindow.
func New(window int, opts ...Option) *Registry {
	if window <= 0 {
		window = DefaultWindow
	}
	r := &Registry{ring: make([]observation, window), chans: make(map[string]*channelStats), clock: clock.Real{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// technical сообщает, входит ли исход в статистику. DECLINED — бизнес-отказ
// (нет денег у клиента), о здоровье канала он ничего не говорит.
func technical(o channel.ChargeOutcome) (counted, success bool) {
	switch o {
	case channel.Approved:
		return true, true
	case channel.ChannelError, channel.Unknown:
		return true, false
	}
	return false, false
}

func (r *Registry) stats(id string) *channelStats {
	s, ok := r.chans[id]
	if !ok {
		s = &channelStats{}
		if r.breaker != nil {
			s.breaker = NewBreaker(id, *r.breaker, r.clock, r.onChange)
		}
		r.chans[id] = s
	}
	return s
}

// Observe добавляет исход попытки в окно.
func (r *Registry) Observe(channelID string, o channel.ChargeOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeLocked(channelID, o)
}

func (r *Registry) observeLocked(channelID string, o channel.ChargeOutcome) {
	counted, success := technical(o)
	if !counted {
		return
	}

	if r.filled == len(r.ring) {
		old := r.ring[r.head]
		s := r.stats(old.channelID)
		s.attempts--
		if old.success {
			s.successes--
		}
	} else {
		r.filled++
	}
	r.ring[r.head] = observation{channelID: channelID, success: success}
	r.head = (r.head + 1) % len(r.ring)

	s := r.stats(channelID)
	s.attempts++
	if success {
		s.successes++
	}
}

// Counts возвращает успехи и число технических исходов канала в окне.
func (r *Registry) Counts(channelID string) (successes, attempts int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.chans[channelID]
	if !ok {
		return 0, 0
	}
	return s.successes, s.attempts
}

// Filled — сколько исходов сейчас в окне (не больше его размера).
func (r *Registry) Filled() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.filled
}
