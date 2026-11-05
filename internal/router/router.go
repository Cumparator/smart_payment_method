// Package router filters unavailable payment channels and ranks the remaining
// candidates with the configured strategy.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/clock"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/store"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

var ErrNilRepository = errors.New("router: repository is nil")

// Repository is the subset of storage operations required by the router.
type Repository interface {
	ListChannels(context.Context) ([]store.ChannelRecord, error)
	GetLiveAttempt(context.Context, string) (payment.Attempt, bool, error)
	ListAttempts(context.Context, string) ([]payment.Attempt, error)
}

// channelConfig is stored in channels.config (JSONB). Amount values are in kopecks.
type channelConfig struct {
	MinAmount *int64         `json:"min_amount,omitempty"`
	MaxAmount *int64         `json:"max_amount,omitempty"`
	Schedule  string         `json:"schedule,omitempty"`
	Tariff    channel.Tariff `json:"tariff"`
}

// ExclusionReason describes why a channel did not reach the ranking stage.
type ExclusionReason string

const (
	ReasonDisabled           ExclusionReason = "disabled"
	ReasonBelowMinAmount     ExclusionReason = "below_min_amount"
	ReasonAboveMaxAmount     ExclusionReason = "above_max_amount"
	ReasonOutsideSchedule    ExclusionReason = "outside_schedule_utc"
	ReasonPreviousChannelErr ExclusionReason = "previous_channel_error"
)

// Exclusion is an explanation for a filtered channel.
type Exclusion struct {
	ChannelID string
	Reason    ExclusionReason
}

// Decision contains ranked candidates and a trace of filters applied.
// If LiveAttempt is non-nil, no new routing decision may be made for this payment.
type Decision struct {
	Strategy    string
	Ranked      []strategy.Scored
	Excluded    []Exclusion
	LiveAttempt *payment.Attempt
}

// Router selects channels. The initial implementation uses neutral statistics:
// zero successes/attempts (Laplace-smoothed probability 0.5), zero load, and the
// configured average latency. A future health snapshot can replace these defaults.
type Router struct {
	repo     Repository
	clock    clock.Clock
	strategy strategy.Strategy
}

// New creates a router. A nil clock defaults to real time; a nil strategy defaults
// to Cheapest. The repository is required and is checked when Select is called.
func New(repo Repository, clk clock.Clock, strat strategy.Strategy) *Router {
	if clk == nil {
		clk = clock.Real{}
	}
	if strat == nil {
		strat = strategy.Cheapest{}
	}
	return &Router{repo: repo, clock: clk, strategy: strat}
}

// Select filters channels for the payment and ranks the remaining candidates.
// STARTED/UNKNOWN attempts stop routing entirely. CHANNEL_ERROR excludes only
// the channel that produced it; DECLINED does not exclude a channel.
func (r *Router) Select(ctx context.Context, p payment.Payment) (Decision, error) {
	if r == nil || r.repo == nil {
		return Decision{}, ErrNilRepository
	}

	decision := Decision{Strategy: r.strategy.Name()}

	live, found, err := r.repo.GetLiveAttempt(ctx, p.ID)
	if err != nil {
		return Decision{}, fmt.Errorf("get live attempt: %w", err)
	}
	if found {
		decision.LiveAttempt = &live
		return decision, nil
	}

	attempts, err := r.repo.ListAttempts(ctx, p.ID)
	if err != nil {
		return Decision{}, fmt.Errorf("list payment attempts: %w", err)
	}
	failedChannels := make(map[string]struct{})
	for _, attempt := range attempts {
		if attempt.State == payment.AttemptChannelError {
			failedChannels[attempt.ChannelID] = struct{}{}
		}
	}

	channels, err := r.repo.ListChannels(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("list channels: %w", err)
	}

	candidates := make([]strategy.Candidate, 0, len(channels))
	nowUTC := r.clock.Now().UTC()
	for _, ch := range channels {
		if _, failed := failedChannels[ch.ID]; failed {
			decision.Excluded = append(decision.Excluded, Exclusion{ChannelID: ch.ID, Reason: ReasonPreviousChannelErr})
			continue
		}
		if !ch.IsEnabled {
			decision.Excluded = append(decision.Excluded, Exclusion{ChannelID: ch.ID, Reason: ReasonDisabled})
			continue
		}

		cfg, err := decodeChannelConfig(ch)
		if err != nil {
			return Decision{}, err
		}
		if cfg.MinAmount != nil && p.Amount < *cfg.MinAmount {
			decision.Excluded = append(decision.Excluded, Exclusion{ChannelID: ch.ID, Reason: ReasonBelowMinAmount})
			continue
		}
		if cfg.MaxAmount != nil && p.Amount > *cfg.MaxAmount {
			decision.Excluded = append(decision.Excluded, Exclusion{ChannelID: ch.ID, Reason: ReasonAboveMaxAmount})
			continue
		}
		available, err := isWithinSchedule(cfg.Schedule, nowUTC)
		if err != nil {
			return Decision{}, fmt.Errorf("channel %q schedule: %w", ch.ID, err)
		}
		if !available {
			decision.Excluded = append(decision.Excluded, Exclusion{ChannelID: ch.ID, Reason: ReasonOutsideSchedule})
			continue
		}

		candidates = append(candidates, strategy.Candidate{
			ChannelID: ch.ID,
			Tariff:    cfg.Tariff,
			// No health snapshot is wired in at step 1. Keep statistics neutral.
			LatencyS: float64(ch.AvgLatencyMS) / 1000,
		})
	}

	decision.Ranked = strategy.Rank(r.strategy, p.Amount, candidates)
	return decision, nil
}

func decodeChannelConfig(ch store.ChannelRecord) (channelConfig, error) {
	var cfg channelConfig
	if len(ch.Config) == 0 {
		ch.Config = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return channelConfig{}, fmt.Errorf("channel %q has invalid config: %w", ch.ID, err)
	}
	if cfg.MinAmount != nil && *cfg.MinAmount < 0 {
		return channelConfig{}, fmt.Errorf("channel %q has negative min_amount", ch.ID)
	}
	if cfg.MaxAmount != nil && *cfg.MaxAmount < 0 {
		return channelConfig{}, fmt.Errorf("channel %q has negative max_amount", ch.ID)
	}
	if cfg.MinAmount != nil && cfg.MaxAmount != nil && *cfg.MinAmount > *cfg.MaxAmount {
		return channelConfig{}, fmt.Errorf("channel %q has min_amount greater than max_amount", ch.ID)
	}
	return cfg, nil
}

// isWithinSchedule accepts "24x7", an empty schedule (no restriction), or
// "HH:MM-HH:MM". The interval is start-inclusive/end-exclusive and is evaluated
// strictly in UTC. Overnight ranges such as 22:00-06:00 are supported.
func isWithinSchedule(schedule string, now time.Time) (bool, error) {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" || strings.EqualFold(schedule, "24x7") {
		return true, nil
	}

	parts := strings.Split(schedule, "-")
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid schedule %q: expected HH:MM-HH:MM or 24x7", schedule)
	}
	start, err := parseClockMinutes(parts[0])
	if err != nil {
		return false, fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	end, err := parseClockMinutes(parts[1])
	if err != nil {
		return false, fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	if start == end {
		return false, fmt.Errorf("invalid schedule %q: start and end are equal", schedule)
	}

	utc := now.UTC()
	current := utc.Hour()*60 + utc.Minute()
	if start < end {
		return current >= start && current < end, nil
	}
	return current >= start || current < end, nil
}

func parseClockMinutes(value string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}
