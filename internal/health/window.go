package health

import (
	"sync"

	"github.com/Cumparator/smart_payment_method/internal/channel"
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
}

// New создаёт реестр с окном на window исходов; при window <= 0 берётся DefaultWindow.
func New(window int) *Registry {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Registry{ring: make([]observation, window), chans: make(map[string]*channelStats)}
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
		r.chans[id] = s
	}
	return s
}

// Observe добавляет исход попытки в окно.
func (r *Registry) Observe(channelID string, o channel.ChargeOutcome) {
	counted, success := technical(o)
	if !counted {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

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
