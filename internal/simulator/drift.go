package simulator

import (
	"fmt"
	"time"
)

// DriftPoint — доля технических ошибок канала через At после Config.Start.
// Между соседними точками доля меняется линейно; две точки с одинаковым At
// дают скачок. До первой и после последней точки доля постоянна.
type DriftPoint struct {
	At        time.Duration
	ErrorRate float64
}

func validateDrift(c Config) error {
	for i, p := range c.Drift {
		if i > 0 && p.At < c.Drift[i-1].At {
			return fmt.Errorf("simulator %s: точки дрейфа не по порядку", c.ID)
		}
		if p.ErrorRate < 0 || p.ErrorRate > 1 {
			return fmt.Errorf("simulator %s: доля вне 0..1", c.ID)
		}
		if c.DeclineRate+p.ErrorRate+c.TimeoutRate > 1 {
			return fmt.Errorf("simulator %s: сумма долей больше 1", c.ID)
		}
	}
	return nil
}

// errorRateAt — доля технических ошибок в момент t.
func (c Config) errorRateAt(t time.Time) float64 {
	if len(c.Drift) == 0 {
		return c.ErrorRate
	}
	d := t.Sub(c.Start)
	if d < c.Drift[0].At {
		return c.Drift[0].ErrorRate
	}
	for i := len(c.Drift) - 1; i > 0; i-- {
		a, b := c.Drift[i-1], c.Drift[i]
		if d < a.At {
			continue
		}
		if d >= b.At {
			return b.ErrorRate
		}
		k := float64(d-a.At) / float64(b.At-a.At)
		return a.ErrorRate + k*(b.ErrorRate-a.ErrorRate)
	}
	return c.Drift[len(c.Drift)-1].ErrorRate
}
