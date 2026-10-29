package simulator

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
)

// Config — параметры имитатора. Доли задаются в 0..1, их сумма не больше 1;
// остаток — доля одобренных списаний.
type Config struct {
	ID          string
	Tariff      channel.Tariff
	Seed        int64
	DeclineRate float64 // бизнес-отказы → DECLINED
	ErrorRate   float64 // технические ошибки → CHANNEL_ERROR
	TimeoutRate float64 // потерянные ответы → UNKNOWN
	// TimeoutChargeRate — доля таймаутов, при которых деньги всё же списаны.
	TimeoutChargeRate float64

	Latency time.Duration // базовая задержка ответа
	Jitter  time.Duration // случайная добавка к задержке, 0..Jitter
	// Sleep ждёт d. По умолчанию — реальное ожидание; в экспериментах
	// подменяется сдвигом виртуальных часов.
	Sleep func(ctx context.Context, d time.Duration) error

	// Drift — изменение ErrorRate во времени; если задан, ErrorRate не используется.
	// Start — точка отсчёта, по умолчанию время создания канала по его часам.
	Drift []DriftPoint
	Start time.Time
}

func (c Config) validate() error {
	for _, r := range []float64{c.DeclineRate, c.ErrorRate, c.TimeoutRate, c.TimeoutChargeRate} {
		if r < 0 || r > 1 {
			return fmt.Errorf("simulator %s: доля вне 0..1", c.ID)
		}
	}
	if c.DeclineRate+c.ErrorRate+c.TimeoutRate > 1 {
		return fmt.Errorf("simulator %s: сумма долей больше 1", c.ID)
	}
	if c.Latency < 0 || c.Jitter < 0 {
		return fmt.Errorf("simulator %s: отрицательная задержка", c.ID)
	}
	return validateDrift(c)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Channel — имитатор платёжного канала. Исход попытки детерминирован:
// он зависит только от seed, канала, ключа попытки и времени по часам канала.
type Channel struct {
	cfg    Config
	ledger Ledger
	clock  clock.Clock

	mu       sync.Mutex
	declined map[string]bool
}

var _ channel.PaymentChannel = (*Channel)(nil)

func New(cfg Config, ledger Ledger, clk clock.Clock) (*Channel, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.Start.IsZero() {
		cfg.Start = clk.Now()
	}
	return &Channel{cfg: cfg, ledger: ledger, clock: clk, declined: make(map[string]bool)}, nil
}

func (c *Channel) ID() string { return c.cfg.ID }

func (c *Channel) Tariff() channel.Tariff { return c.cfg.Tariff }

// Latency — типичная задержка ответа канала.
func (c *Channel) Latency() time.Duration { return c.cfg.Latency + c.cfg.Jitter/2 }

// ErrorRateAt — истинная доля технических ошибок в момент t. Стратегиям
// недоступна; нужна эксперименту для сравнения с «оракулом».
func (c *Channel) ErrorRateAt(t time.Time) float64 { return c.cfg.errorRateAt(t) }

func (c *Channel) Charge(ctx context.Context, key string, amount int64, _ string) (channel.ChargeResult, error) {
	res, err := c.process(ctx, key, amount)
	if err != nil {
		return res, err
	}
	if err := c.wait(ctx, key); err != nil {
		// запрос канал уже обработал, но ответ до клиента не дошёл
		return channel.ChargeResult{Outcome: channel.Unknown, Reason: err.Error()}, nil
	}
	return res, nil
}

func (c *Channel) wait(ctx context.Context, key string) error {
	d := c.cfg.Latency + time.Duration(c.roll(key, "latency")*float64(c.cfg.Jitter))
	if d <= 0 {
		return ctx.Err()
	}
	return c.cfg.Sleep(ctx, d)
}

func (c *Channel) process(ctx context.Context, key string, amount int64) (channel.ChargeResult, error) {
	if res, ok, err := c.repeat(ctx, key); err != nil || ok {
		return res, err
	}

	r := c.roll(key, "outcome")
	errRate := c.cfg.errorRateAt(c.clock.Now())
	switch {
	case r < c.cfg.TimeoutRate:
		if c.roll(key, "timeout") < c.cfg.TimeoutChargeRate {
			if _, err := c.charge(ctx, key, amount); err != nil {
				return channel.ChargeResult{}, err
			}
		}
		return channel.ChargeResult{Outcome: channel.Unknown, Reason: "timeout"}, nil
	case r < c.cfg.TimeoutRate+errRate:
		return channel.ChargeResult{Outcome: channel.ChannelError, Reason: "provider error"}, nil
	case r < c.cfg.TimeoutRate+errRate+c.cfg.DeclineRate:
		c.mu.Lock()
		c.declined[key] = true
		c.mu.Unlock()
		return channel.ChargeResult{Outcome: channel.Declined, Reason: "insufficient funds"}, nil
	}

	e, err := c.charge(ctx, key, amount)
	if err != nil {
		return channel.ChargeResult{}, err
	}
	if e.Status == Cancelled {
		return channel.ChargeResult{Outcome: channel.Declined, Reason: "cancelled"}, nil
	}
	return channel.ChargeResult{Outcome: channel.Approved}, nil
}

// repeat отвечает на повтор по уже обработанному ключу, не списывая заново.
func (c *Channel) repeat(ctx context.Context, key string) (channel.ChargeResult, bool, error) {
	c.mu.Lock()
	declined := c.declined[key]
	c.mu.Unlock()
	if declined {
		return channel.ChargeResult{Outcome: channel.Declined, Reason: "insufficient funds"}, true, nil
	}
	e, ok, err := c.ledger.Get(ctx, c.cfg.ID, key)
	if err != nil || !ok {
		return channel.ChargeResult{}, false, err
	}
	if e.Status == Cancelled {
		return channel.ChargeResult{Outcome: channel.Declined, Reason: "cancelled"}, true, nil
	}
	return channel.ChargeResult{Outcome: channel.Approved}, true, nil
}

func (c *Channel) charge(ctx context.Context, key string, amount int64) (Entry, error) {
	return c.ledger.Charge(ctx, Entry{ChannelID: c.cfg.ID, Key: key, Amount: amount, At: c.clock.Now()})
}

func (c *Channel) Status(ctx context.Context, key string) (channel.StatusResult, error) {
	c.mu.Lock()
	declined := c.declined[key]
	c.mu.Unlock()
	if declined {
		return channel.StatusResult{Outcome: channel.StatusDeclined}, nil
	}
	e, ok, err := c.ledger.Get(ctx, c.cfg.ID, key)
	if err != nil {
		return channel.StatusResult{}, err
	}
	switch {
	case !ok:
		return channel.StatusResult{Outcome: channel.StatusNotFound}, nil
	case e.Status == Cancelled:
		return channel.StatusResult{Outcome: channel.StatusDeclined}, nil
	}
	return channel.StatusResult{Outcome: channel.StatusApproved}, nil
}

func (c *Channel) Cancel(ctx context.Context, key string) (channel.CancelResult, error) {
	if _, err := c.ledger.Cancel(ctx, c.cfg.ID, key, c.clock.Now()); err != nil {
		return channel.CancelResult{}, err
	}
	return channel.CancelResult{Confirmed: true}, nil
}

// Summary — итог по книге: сколько списано и сколько стоила комиссия.
type Summary struct {
	Charges   int
	Amount    int64
	Fees      float64
	Cancelled int
}

func (c *Channel) Summary(ctx context.Context) (Summary, error) {
	entries, err := c.ledger.Entries(ctx, c.cfg.ID)
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	for _, e := range entries {
		if e.Status == Cancelled {
			s.Cancelled++
			continue
		}
		s.Charges++
		s.Amount += e.Amount
		s.Fees += c.cfg.Tariff.Fee(e.Amount)
	}
	return s, nil
}

// roll — детерминированное число в [0, 1) от hash(seed, channel, key, salt).
func (c *Channel) roll(key, salt string) float64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%s|%s|%s", c.cfg.Seed, c.cfg.ID, key, salt)
	return float64(h.Sum64()>>11) / (1 << 53)
}
