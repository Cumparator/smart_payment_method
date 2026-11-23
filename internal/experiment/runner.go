package experiment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/health"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/simulator"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

// ReconcileDelay — сколько сверщик выжидает до запроса статуса у канала.
// Идёт в длительность платежа, но не двигает часы сценария.
const ReconcileDelay = 30 * time.Second

// AttemptResult — одна попытка платежа.
type AttemptResult struct {
	No        int
	ChannelID string
	At        time.Duration // смещение от начала сценария
	Outcome   channel.ChargeOutcome
	Latency   time.Duration
}

// PaymentResult — итог платежа.
type PaymentResult struct {
	ID       string
	Amount   int64
	At       time.Duration
	Final    payment.State // SUCCEEDED или FAILED
	Attempts []AttemptResult

	Reconciled bool          // хотя бы одна попытка закончилась UNKNOWN и ушла на сверку
	ChannelID  string        // канал, через который платёж прошёл; пусто, если не прошёл
	Fee        float64       // комиссия канала, копейки; 0, если платёж не прошёл
	Duration   time.Duration // от первой попытки до финала
}

// Result — итог прогона одной стратегии на одном сценарии при одном seed.
type Result struct {
	Strategy string
	Seed     int64
	Payments []PaymentResult
	Ledgers  map[string][]simulator.Entry // книги списаний каналов
}

// Run прогоняет сценарий стратегией на виртуальных часах. Каналы отвечают по
// hash(seed, канал, ключ попытки, время), поэтому при одном seed разные стратегии
// получают от канала одинаковые ответы на одинаковые вопросы.
//
// Платежи обрабатываются по одному, как поток из одного воркера: задержки
// каналов и сверки считаются в длительности платежа, но часы сценария на них
// не сдвигаются — платёж целиком происходит в момент своего поступления.
func Run(ctx context.Context, sc Scenario, strat strategy.Strategy, seed int64) (Result, error) {
	if len(sc.Header.Channels) == 0 {
		return Result{}, fmt.Errorf("run: в сценарии нет каналов")
	}
	r := &run{
		strat: strat,
		start: sc.Header.Start,
		clk:   clock.NewVirtual(sc.Header.Start),
		reg:   health.New(health.DefaultWindow),
		byID:  make(map[string]*simChannel),
	}
	sleep := func(_ context.Context, d time.Duration) error {
		r.lastLatency = d
		return nil
	}
	for _, spec := range sc.Header.Channels {
		if err := spec.Validate(); err != nil {
			return Result{}, fmt.Errorf("run: %w", err)
		}
		cfg := spec.SimConfig(seed, sc.Header.Start)
		cfg.Sleep = sleep
		ledger := simulator.NewMemLedger()
		ch, err := simulator.New(cfg, ledger, r.clk)
		if err != nil {
			return Result{}, fmt.Errorf("run: канал %s: %w", spec.ID, err)
		}
		c := &simChannel{spec: spec, ch: ch, ledger: ledger}
		r.chans = append(r.chans, c)
		r.byID[spec.ID] = c
		r.reg.Register(spec.ID, ch.Latency(), 0)
	}

	res := Result{Strategy: strat.Name(), Seed: seed, Payments: make([]PaymentResult, 0, len(sc.Payments))}
	var prev Dur
	for _, p := range sc.Payments {
		if p.At < prev {
			return Result{}, fmt.Errorf("run: платёж %s идёт раньше предыдущего, сценарий не упорядочен", p.ID)
		}
		prev = p.At
		pr, err := r.process(ctx, p)
		if err != nil {
			return Result{}, fmt.Errorf("run: платёж %s: %w", p.ID, err)
		}
		res.Payments = append(res.Payments, pr)
	}

	res.Ledgers = make(map[string][]simulator.Entry, len(r.chans))
	for _, c := range r.chans {
		entries, err := c.ledger.Entries(ctx, c.spec.ID)
		if err != nil {
			return Result{}, err
		}
		res.Ledgers[c.spec.ID] = entries
	}
	return res, nil
}

type simChannel struct {
	spec   ChannelSpec
	ch     *simulator.Channel
	ledger *simulator.MemLedger
}

type run struct {
	strat strategy.Strategy
	start time.Time
	clk   *clock.Virtual
	reg   *health.Registry
	chans []*simChannel
	byID  map[string]*simChannel

	lastLatency time.Duration // задержка последнего вызова канала, выставляется Sleep
}

func (r *run) advanceTo(offset time.Duration) {
	if d := r.start.Add(offset).Sub(r.clk.Now()); d > 0 {
		r.clk.Advance(d)
	}
}

// process проводит платёж по правилам боевого потока: не больше MaxAttempts
// попыток; CHANNEL_ERROR — следующая попытка в другом канале; DECLINED — конец;
// UNKNOWN — платёж на сверку, канал не меняется, пока статус не выяснен.
func (r *run) process(ctx context.Context, p Payment) (PaymentResult, error) {
	r.advanceTo(time.Duration(p.At))
	pr := PaymentResult{ID: p.ID, Amount: p.Amount, At: time.Duration(p.At), Final: payment.Failed}
	failed := make(map[string]bool) // каналы, давшие CHANNEL_ERROR по этому платежу
	var elapsed time.Duration

	settle := func(c *simChannel) {
		pr.Final, pr.ChannelID, pr.Fee = payment.Succeeded, c.spec.ID, c.spec.Tariff().Fee(p.Amount)
	}

	for no := 1; no <= payment.MaxAttempts; no++ {
		cands := make([]strategy.Candidate, 0, len(r.chans))
		for _, c := range r.chans {
			if !failed[c.spec.ID] {
				cands = append(cands, r.reg.Candidate(c.spec.ID, c.spec.Tariff()))
			}
		}
		if len(cands) == 0 {
			break
		}
		c := r.byID[strategy.Rank(r.strat, p.Amount, cands)[0].ChannelID]
		key := payment.Attempt{PaymentID: p.ID, AttemptNo: no}.ChannelKey()

		r.lastLatency = 0
		cr, err := c.ch.Charge(ctx, key, p.Amount, p.Currency)
		if err != nil {
			return pr, err
		}
		r.reg.Record(c.spec.ID, cr.Outcome, r.lastLatency)
		pr.Attempts = append(pr.Attempts, AttemptResult{
			No: no, ChannelID: c.spec.ID, At: pr.At + elapsed, Outcome: cr.Outcome, Latency: r.lastLatency,
		})
		elapsed += r.lastLatency

		switch cr.Outcome {
		case channel.Approved:
			settle(c)
		case channel.Declined:
			// бизнес-отказ окончателен
		case channel.ChannelError:
			failed[c.spec.ID] = true
			continue
		case channel.Unknown:
			pr.Reconciled = true
			elapsed += ReconcileDelay
			st, err := c.ch.Status(ctx, key)
			if err != nil {
				return pr, err
			}
			switch st.Outcome {
			case channel.StatusApproved:
				settle(c)
			case channel.StatusNotFound:
				// деньги не списаны: отменяем ключ и запускаем платёж заново
				if _, err := c.ch.Cancel(ctx, key); err != nil {
					return pr, err
				}
				continue
			}
		}
		break
	}
	pr.Duration = elapsed
	return pr, nil
}

// chargedByPayment считает по книгам каналов, сколько раз с каждого платежа
// списаны деньги. Ключ записи имеет вид "<payment_id>:<attempt_no>".
func (r Result) chargedByPayment() map[string]int {
	out := make(map[string]int)
	for _, entries := range r.Ledgers {
		for _, e := range entries {
			if e.Status != simulator.Charged {
				continue
			}
			id := e.Key[:strings.LastIndexByte(e.Key, ':')]
			out[id]++
		}
	}
	return out
}
