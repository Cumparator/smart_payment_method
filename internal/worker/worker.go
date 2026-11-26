package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/health"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/router"
	"github.com/Cumparator/smart_payment_method/internal/store"
)

// Store — то, что воркеру нужно от хранилища. Реализует *store.DB.
type Store interface {
	ClaimPayment(ctx context.Context, now time.Time) (payment.Payment, bool, error)
	ListAttempts(ctx context.Context, paymentID string) ([]payment.Attempt, error)
	StartAttemptWithDecision(ctx context.Context, paymentID, channelID string, dec *store.RoutingDecision) (payment.Attempt, error)
	CompleteAttempt(ctx context.Context, c store.Completion) (bool, error)
	TransitionStatusAt(ctx context.Context, id string, from, to payment.State, nextRunAt time.Time, reason string) (bool, error)
}

// Router выбирает канал с учётом уже сделанных попыток.
type Router interface {
	ChoosePayment(ctx context.Context, p payment.Payment, attempts []payment.Attempt) (router.Decision, error)
}

var (
	_ Store  = (*store.DB)(nil)
	_ Router = (*router.Router)(nil)
)

// Config — настройки воркера. Нулевые поля заменяются значениями по умолчанию.
type Config struct {
	Workers        int           // параллельных циклов обработки (1)
	IdleWait       time.Duration // пауза, когда очередь пуста (500 мс)
	ChargeTimeout  time.Duration // предел ожидания ответа канала (10 с)
	PersistTimeout time.Duration // предел подготовки и отдельно записи результата (5 с)
	RetryBase      time.Duration // пауза перед 2-й попыткой; дальше удваивается (1 с)
	RetryMax       time.Duration // потолок паузы между попытками (30 с)
	ReconcileDelay time.Duration // через сколько сверщик впервые смотрит на RECONCILING (5 с)
	BusyWait       time.Duration // пауза, если в канале не нашлось места для запроса (200 мс)
}

func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	def(&c.IdleWait, 500*time.Millisecond)
	def(&c.ChargeTimeout, 10*time.Second)
	def(&c.PersistTimeout, 5*time.Second)
	def(&c.RetryBase, time.Second)
	def(&c.RetryMax, 30*time.Second)
	def(&c.ReconcileDelay, 5*time.Second)
	def(&c.BusyWait, 200*time.Millisecond)
	return c
}

// Worker — цикл обработки: взять платёж, выбрать канал, записать попытку,
// вызвать канал, применить исход.
type Worker struct {
	store    Store
	router   Router
	channels map[string]channel.PaymentChannel
	clock    clock.Clock
	cfg      Config
	log      *slog.Logger
	jitter   func() float64 // [0,1); подменяется в тестах
	health   *health.Registry
	budget   *health.RetryBudget
}

func New(st Store, rt Router, channels []channel.PaymentChannel, clk clock.Clock, cfg Config, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	m := make(map[string]channel.PaymentChannel, len(channels))
	for _, ch := range channels {
		m[ch.ID()] = ch
	}
	return &Worker{
		store: st, router: rt, channels: m, clock: clk,
		cfg: cfg.withDefaults(), log: log, jitter: rand.Float64,
	}
}

// WithHealth подключает защиту каналов: допуск запроса (лимит одновременных
// запросов и предохранитель) и общий бюджет повторов. Любой из аргументов может
// быть nil. Роутер стоит создавать с тем же реестром (router.WithHealth), иначе он
// будет выбирать каналы, в которые воркер не пускает, и платежи станут кружить в очереди.
func (w *Worker) WithHealth(reg *health.Registry, budget *health.RetryBudget) *Worker {
	w.health, w.budget = reg, budget
	return w
}

// Run запускает cfg.Workers циклов. После отмены ctx воркеры не берут новые платежи,
// но доводят до конца те, что уже взяты (включая вызов канала), и только потом
// Run возвращается. Подготовка, вызов канала и запись исхода ограничены таймаутами.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		did, err := w.ProcessOne(ctx)
		if err != nil {
			w.log.Error("обработка платежа", "err", err)
		}
		if did {
			continue
		}
		t := time.NewTimer(w.cfg.IdleWait)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
}

// ProcessOne обрабатывает один платёж из очереди. false — очередь пуста.
//
// ctx управляет только захватом платежа. Взятый платёж обрабатывается до конца
// независимо от отмены ctx: оборвать вызов канала на середине значило бы получить
// UNKNOWN и ненужную сверку, хотя канал мог спокойно ответить. Время вызова
// ограничено ChargeTimeout, подготовка и запись результата — PersistTimeout.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	p, ok, err := w.store.ClaimPayment(ctx, w.clock.Now())
	if err != nil {
		return false, fmt.Errorf("worker: взять платёж: %w", err)
	}
	if !ok {
		return false, nil
	}
	return true, w.process(context.WithoutCancel(ctx), p)
}

func (w *Worker) process(ctx context.Context, p payment.Payment) error {
	prepareCtx, cancelPrepare := w.persistCtx(ctx)
	defer cancelPrepare()
	attempts, err := w.store.ListAttempts(prepareCtx, p.ID)
	if err != nil {
		return w.release(ctx, p, "прочитать попытки", err)
	}
	dec, err := w.router.ChoosePayment(prepareCtx, p, attempts)
	switch {
	case errors.Is(err, store.ErrLiveAttemptExists):
		return w.finish(ctx, p, payment.Reconciling, w.cfg.ReconcileDelay, "уже есть незавершённая попытка")
	case errors.Is(err, router.ErrNoChannel):
		return w.finish(ctx, p, payment.Failed, 0, "нет доступного канала")
	case err != nil:
		return w.release(ctx, p, "выбрать канал", err)
	}
	ch, ok := w.channels[dec.ChannelID]
	if !ok {
		return w.finish(ctx, p, payment.Failed, 0, "канал не зарегистрирован: "+dec.ChannelID)
	}

	// Место в канале занимается до записи попытки: если его нет, платёж просто
	// возвращается в очередь, и попытка не тратится.
	var slot *health.Slot
	if w.health != nil {
		var why health.Unavailable
		if slot, why = w.health.TryBegin(dec.ChannelID); why != "" {
			return w.finish(ctx, p, payment.Received, w.cfg.BusyWait, "канал "+dec.ChannelID+" недоступен: "+string(why))
		}
	}

	// Попытка пишется в БД ДО обращения к каналу: если процесс упадёт посреди
	// вызова, в БД останется STARTED, и сверщик выяснит исход по ключу попытки.
	att, err := w.store.StartAttemptWithDecision(prepareCtx, p.ID, dec.ChannelID,
		&store.RoutingDecision{Strategy: dec.Strategy, Details: dec.Details})
	if err != nil && slot != nil {
		slot.Cancel() // запроса в канал не будет
	}
	switch {
	case errors.Is(err, store.ErrLiveAttemptExists):
		// У платежа уже есть незавершённая попытка — исход неизвестен, повторять нельзя.
		return w.finish(ctx, p, payment.Reconciling, w.cfg.ReconcileDelay, "уже есть незавершённая попытка")
	case errors.Is(err, store.ErrMaxAttempts):
		return w.finish(ctx, p, payment.Failed, 0, "попытки исчерпаны")
	case err != nil:
		return w.release(ctx, p, "записать попытку", err)
	}

	if att.AttemptNo == 1 && w.budget != nil {
		w.budget.RecordFirst()
	}
	cancelPrepare()
	started := w.clock.Now()
	res := w.charge(ctx, ch, att, p)
	if slot != nil {
		slot.Done(res.Outcome, w.clock.Now().Sub(started))
	}
	pl := w.plan(res, att.AttemptNo)

	var next time.Time
	if pl.delay > 0 {
		next = w.clock.Now().Add(pl.delay)
	}
	pctx, cancel := w.persistCtx(ctx)
	defer cancel()
	done, err := w.store.CompleteAttempt(pctx, store.Completion{
		PaymentID: p.ID, AttemptNo: att.AttemptNo,
		AttemptTo: pl.attemptTo, PaymentTo: pl.paymentTo,
		NextRunAt: next, Reason: pl.reason,
	})
	if err != nil {
		// Попытка осталась STARTED, платёж — PROCESSING: исход потерян для БД,
		// его должен подобрать уборщик зависших попыток.
		return fmt.Errorf("worker: сохранить исход попытки %s (%s): %w", att.ChannelKey(), res.Outcome, err)
	}
	if !done {
		w.log.Warn("попытка уже изменена другим процессом, исход не применён",
			"payment", p.ID, "attempt", att.AttemptNo, "outcome", res.Outcome)
		return nil
	}
	w.log.Info("попытка завершена", "payment", p.ID, "attempt", att.AttemptNo,
		"channel", dec.ChannelID, "outcome", res.Outcome, "payment_state", pl.paymentTo)
	return nil
}

// charge вызывает канал. Любой сбой вызова (ошибка, таймаут, отмена) — это UNKNOWN,
// а не отказ: запрос мог дойти до канала и списать деньги.
func (w *Worker) charge(ctx context.Context, ch channel.PaymentChannel, att payment.Attempt, p payment.Payment) channel.ChargeResult {
	cctx, cancel := context.WithTimeout(ctx, w.cfg.ChargeTimeout)
	defer cancel()
	res, err := ch.Charge(cctx, att.ChannelKey(), p.Amount, p.Currency)
	if err != nil {
		return channel.ChargeResult{Outcome: channel.Unknown, Reason: err.Error()}
	}
	return res
}

// plan — исход канала → переходы попытки и платежа.
type plan struct {
	attemptTo payment.AttemptState
	paymentTo payment.State
	delay     time.Duration // для RECEIVED (повтор) и RECONCILING (первая сверка)
	reason    string
}

func (w *Worker) plan(res channel.ChargeResult, attemptNo int) plan {
	switch res.Outcome {
	case channel.Approved:
		return plan{attemptTo: payment.AttemptApproved, paymentTo: payment.Succeeded, reason: "списано"}
	case channel.Declined:
		return plan{attemptTo: payment.AttemptDeclined, paymentTo: payment.Failed, reason: "отказ: " + res.Reason}
	case channel.ChannelError:
		if attemptNo >= payment.MaxAttempts {
			return plan{attemptTo: payment.AttemptChannelError, paymentTo: payment.Failed,
				reason: "ошибка канала, попытки исчерпаны: " + res.Reason}
		}
		if w.budget != nil && !w.budget.TryRetry() {
			return plan{attemptTo: payment.AttemptChannelError, paymentTo: payment.Failed,
				reason: "ошибка канала, бюджет повторов исчерпан: " + res.Reason}
		}
		return plan{attemptTo: payment.AttemptChannelError, paymentTo: payment.Received,
			delay: w.retryDelay(attemptNo), reason: "ошибка канала, повтор в другом канале: " + res.Reason}
	default: // UNKNOWN и всё непонятное
		return plan{attemptTo: payment.AttemptUnknown, paymentTo: payment.Reconciling,
			delay: w.cfg.ReconcileDelay, reason: "исход неизвестен: " + res.Reason}
	}
}

// retryDelay — экспоненциальная пауза после attemptNo-й попытки с разбросом ±20%.
func (w *Worker) retryDelay(attemptNo int) time.Duration {
	d := w.cfg.RetryBase
	for i := 1; i < attemptNo && d < w.cfg.RetryMax; i++ {
		d *= 2
	}
	if d > w.cfg.RetryMax {
		d = w.cfg.RetryMax
	}
	return time.Duration(float64(d) * (0.8 + 0.4*w.jitter()))
}

// persistCtx — контекст для записи в БД. Он не отменяется вместе с ctx воркера:
// при остановке результат уже выполненного вызова канала всё равно надо сохранить.
func (w *Worker) persistCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), w.cfg.PersistTimeout)
}

// finish переводит платёж PROCESSING → to без попытки (попытка не создавалась).
func (w *Worker) finish(ctx context.Context, p payment.Payment, to payment.State, delay time.Duration, reason string) error {
	var next time.Time
	if delay > 0 {
		next = w.clock.Now().Add(delay)
	}
	pctx, cancel := w.persistCtx(ctx)
	defer cancel()
	ok, err := w.store.TransitionStatusAt(pctx, p.ID, payment.Processing, to, next, reason)
	if err != nil {
		return fmt.Errorf("worker: перевести платёж %s в %s: %w", p.ID, to, err)
	}
	if !ok {
		w.log.Warn("платёж уже не в PROCESSING", "payment", p.ID, "to", to)
	}
	return nil
}

// release возвращает платёж в очередь после сбоя инфраструктуры до вызова канала.
// Деньги не тронуты, поэтому повтор безопасен.
func (w *Worker) release(ctx context.Context, p payment.Payment, stage string, cause error) error {
	err := w.finish(ctx, p, payment.Received, w.retryDelay(1), stage+": "+cause.Error())
	return errors.Join(fmt.Errorf("worker: %s: %w", stage, cause), err)
}
