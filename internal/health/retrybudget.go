package health

import "sync"

// Значения по умолчанию для бюджета повторов.
const (
	DefaultRetryRatio  = 0.2 // повторы — не больше пятой части попыток
	DefaultRetryWindow = 100 // сколько последних попыток смотрим
	DefaultRetryFloor  = 10  // база для доли, пока попыток в окне мало
)

// RetryBudget — общий для всех каналов лимит повторных попыток. Когда каналы
// массово ошибаются, повторы каждого платежа удвоили бы нагрузку на и без того
// больные каналы; бюджет не даёт повторам занять больше ratio от последних
// попыток, остальные платежи при ошибке канала сразу заканчиваются неудачей.
//
// В окне хранятся последние window попыток: первые и повторные. Повтор
// разрешён, если после него доля повторов в окне не превысит ratio. Пока попыток
// меньше floor, доля считается от floor: в начале работы можно сделать
// несколько повторов, а не ни одного.
type RetryBudget struct {
	ratio float64
	floor int

	mu      sync.Mutex
	ring    []bool // true — повтор
	head    int
	filled  int
	retries int
}

// NewRetryBudget создаёт бюджет; нулевые и отрицательные параметры заменяются
// значениями по умолчанию.
func NewRetryBudget(ratio float64, window, floor int) *RetryBudget {
	if ratio <= 0 {
		ratio = DefaultRetryRatio
	}
	if window <= 0 {
		window = DefaultRetryWindow
	}
	if floor <= 0 {
		floor = DefaultRetryFloor
	}
	return &RetryBudget{ratio: ratio, floor: floor, ring: make([]bool, window)}
}

// RecordFirst учитывает первую попытку платежа.
func (b *RetryBudget) RecordFirst() {
	b.mu.Lock()
	b.pushLocked(false)
	b.mu.Unlock()
}

// TryRetry занимает повтор в бюджете. Если доля повторов в окне уже на пределе,
// возвращает false и ничего не занимает.
func (b *RetryBudget) TryRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	base := max(b.filled+1, b.floor)
	if float64(b.retries+1) > b.ratio*float64(base)+1e-9 {
		return false
	}
	b.pushLocked(true)
	return true
}

func (b *RetryBudget) pushLocked(retry bool) {
	if b.filled == len(b.ring) {
		if b.ring[b.head] {
			b.retries--
		}
	} else {
		b.filled++
	}
	b.ring[b.head] = retry
	b.head = (b.head + 1) % len(b.ring)
	if retry {
		b.retries++
	}
}
