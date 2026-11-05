package simulator

import (
	"context"
	"sync"
	"time"
)

type EntryStatus string

const (
	Charged   EntryStatus = "CHARGED"
	Cancelled EntryStatus = "CANCELLED"
)

// Entry — запись книги списаний канала. Ключ — payment_id:attempt_no.
type Entry struct {
	ChannelID string
	Key       string
	Amount    int64
	Status    EntryStatus
	At        time.Time
}

// Ledger — книга списаний. Одна запись на (канал, ключ), поэтому повтор
// с тем же ключом не спишет деньги дважды.
type Ledger interface {
	Get(ctx context.Context, channelID, key string) (Entry, bool, error)
	// Charge добавляет запись, если её ещё нет, и возвращает актуальную.
	Charge(ctx context.Context, e Entry) (Entry, error)
	// Cancel переводит запись в CANCELLED; если записи нет — создаёт
	// отменённую с нулевой суммой, чтобы ключ больше не списывался.
	Cancel(ctx context.Context, channelID, key string, at time.Time) (Entry, error)
	Entries(ctx context.Context, channelID string) ([]Entry, error)
}

type ledgerKey struct{ channel, key string }

// MemLedger — книга в памяти для тестов и экспериментов.
type MemLedger struct {
	mu      sync.Mutex
	entries map[ledgerKey]Entry
	order   []ledgerKey
}

func NewMemLedger() *MemLedger {
	return &MemLedger{entries: make(map[ledgerKey]Entry)}
}

func (l *MemLedger) Get(_ context.Context, channelID, key string) (Entry, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ledgerKey{channelID, key}]
	return e, ok, nil
}

func (l *MemLedger) Charge(_ context.Context, e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey{e.ChannelID, e.Key}
	if old, ok := l.entries[k]; ok {
		return old, nil
	}
	e.Status = Charged
	l.entries[k] = e
	l.order = append(l.order, k)
	return e, nil
}

func (l *MemLedger) Cancel(_ context.Context, channelID, key string, at time.Time) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey{channelID, key}
	e, ok := l.entries[k]
	if !ok {
		e = Entry{ChannelID: channelID, Key: key}
		l.order = append(l.order, k)
	}
	if e.Status != Cancelled {
		e.Status = Cancelled
		e.At = at
	}
	l.entries[k] = e
	return e, nil
}

func (l *MemLedger) Entries(_ context.Context, channelID string) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Entry
	for _, k := range l.order {
		if k.channel == channelID {
			out = append(out, l.entries[k])
		}
	}
	return out, nil
}
