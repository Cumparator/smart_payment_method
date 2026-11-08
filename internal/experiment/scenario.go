package experiment

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/simulator"
)

// Dur — длительность, которая в JSON записывается строкой вида "1h30m".
type Dur time.Duration

func (d Dur) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

func (d *Dur) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Dur(v)
	return nil
}

// DriftSpec — точка дрейфа доли технических ошибок канала.
type DriftSpec struct {
	At        Dur     `json:"at"`
	ErrorRate float64 `json:"error_rate"`
}

// OutageSpec — окно полной недоступности канала.
type OutageSpec struct {
	From Dur `json:"from"`
	To   Dur `json:"to"`
}

// ChannelSpec — описание канала в сценарии. Суммы — в копейках.
type ChannelSpec struct {
	ID      string  `json:"id"`
	Fixed   int64   `json:"fixed"`
	Percent float64 `json:"percent"`
	Latency Dur     `json:"latency"`
	Jitter  Dur     `json:"jitter"`

	DeclineRate       float64 `json:"decline_rate"`
	ErrorRate         float64 `json:"error_rate"`
	TimeoutRate       float64 `json:"timeout_rate"`
	TimeoutChargeRate float64 `json:"timeout_charge_rate"`

	Drift   []DriftSpec  `json:"drift,omitempty"`
	Outages []OutageSpec `json:"outages,omitempty"`
}

// Tariff — комиссия канала.
func (c ChannelSpec) Tariff() channel.Tariff {
	return channel.Tariff{Fixed: c.Fixed, Percent: c.Percent}
}

// SimConfig строит настройки имитатора. seed — seed прогона: при одном seed
// все стратегии получают от канала одинаковые ответы на одинаковые ключи попыток.
func (c ChannelSpec) SimConfig(seed int64, start time.Time) simulator.Config {
	cfg := simulator.Config{
		ID:                c.ID,
		Tariff:            c.Tariff(),
		Seed:              seed,
		DeclineRate:       c.DeclineRate,
		ErrorRate:         c.ErrorRate,
		TimeoutRate:       c.TimeoutRate,
		TimeoutChargeRate: c.TimeoutChargeRate,
		Latency:           time.Duration(c.Latency),
		Jitter:            time.Duration(c.Jitter),
		Start:             start,
	}
	cfg.Drift = c.driftPoints()
	return cfg
}

// driftPoints переводит Drift и Outages в точки дрейфа имитатора. Авария —
// это скачок доли технических ошибок до максимума: любая попытка, кроме
// бизнес-отказов и таймаутов, получает CHANNEL_ERROR. Вычитание 1e-9 нужно,
// чтобы из-за округления сумма долей не превысила 1.
func (c ChannelSpec) driftPoints() []simulator.DriftPoint {
	var pts []simulator.DriftPoint
	for _, p := range c.Drift {
		pts = append(pts, simulator.DriftPoint{At: time.Duration(p.At), ErrorRate: p.ErrorRate})
	}
	if len(c.Outages) == 0 {
		return pts
	}
	down := 1 - c.DeclineRate - c.TimeoutRate - 1e-9
	pts = []simulator.DriftPoint{{At: 0, ErrorRate: c.ErrorRate}}
	for _, o := range c.Outages {
		from, to := time.Duration(o.From), time.Duration(o.To)
		pts = append(pts,
			simulator.DriftPoint{At: from, ErrorRate: c.ErrorRate},
			simulator.DriftPoint{At: from, ErrorRate: down},
			simulator.DriftPoint{At: to, ErrorRate: down},
			simulator.DriftPoint{At: to, ErrorRate: c.ErrorRate},
		)
	}
	return pts
}

// Validate проверяет описание канала. Окна аварий должны идти по порядку и не
// пересекаться. Аварии и Drift в одном канале не сочетаются: авария записывается
// в имитатор теми же точками дрейфа.
func (c ChannelSpec) Validate() error {
	if len(c.Outages) > 0 && len(c.Drift) > 0 {
		return fmt.Errorf("канал %s: аварии и дрейф в одном канале не поддерживаются", c.ID)
	}
	var prevTo Dur
	for _, o := range c.Outages {
		if o.From < 0 || o.To <= o.From || o.From < prevTo {
			return fmt.Errorf("канал %s: некорректное окно аварии %s–%s", c.ID, time.Duration(o.From), time.Duration(o.To))
		}
		prevTo = o.To
	}
	return nil
}

// Header — первая строка файла сценария.
type Header struct {
	Seed     int64         `json:"seed"` // seed генератора, не прогона
	Start    time.Time     `json:"start"`
	Duration Dur           `json:"duration"`
	Channels []ChannelSpec `json:"channels"`
}

// Payment — платёж сценария; At — смещение от Header.Start.
type Payment struct {
	ID       string `json:"id"`
	At       Dur    `json:"at"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Scenario — вход эксперимента. Платежи упорядочены по At.
type Scenario struct {
	Header   Header
	Payments []Payment
}

const (
	lineHeader  = "header"
	linePayment = "payment"
)

// Write пишет сценарий в формате JSONL: строка header, затем строки payment.
func Write(w io.Writer, s Scenario) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(struct {
		Type string `json:"type"`
		Header
	}{lineHeader, s.Header}); err != nil {
		return err
	}
	for _, p := range s.Payments {
		if err := enc.Encode(struct {
			Type string `json:"type"`
			Payment
		}{linePayment, p}); err != nil {
			return err
		}
	}
	return nil
}

// Read читает сценарий, записанный Write.
func Read(r io.Reader) (Scenario, error) {
	var s Scenario
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	haveHeader := false
	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &kind); err != nil {
			return Scenario{}, fmt.Errorf("строка %d: %w", n, err)
		}
		switch kind.Type {
		case lineHeader:
			if haveHeader {
				return Scenario{}, fmt.Errorf("строка %d: второй header", n)
			}
			if err := json.Unmarshal(line, &s.Header); err != nil {
				return Scenario{}, fmt.Errorf("строка %d: %w", n, err)
			}
			haveHeader = true
		case linePayment:
			var p Payment
			if err := json.Unmarshal(line, &p); err != nil {
				return Scenario{}, fmt.Errorf("строка %d: %w", n, err)
			}
			s.Payments = append(s.Payments, p)
		default:
			return Scenario{}, fmt.Errorf("строка %d: неизвестный тип %q", n, kind.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return Scenario{}, err
	}
	if !haveHeader {
		return Scenario{}, fmt.Errorf("в сценарии нет строки header")
	}
	return s, nil
}
