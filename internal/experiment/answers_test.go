package experiment

import (
	"testing"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

func attemptsOf(id string, as ...AttemptResult) PaymentResult {
	return PaymentResult{ID: id, Attempts: as}
}

func TestCompareAnswersCountsOnlySharedQuestions(t *testing.T) {
	a := Result{Payments: []PaymentResult{
		attemptsOf("p1", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Approved}),
		attemptsOf("p2", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.ChannelError},
			AttemptResult{No: 2, ChannelID: "B", Outcome: channel.Approved}),
	}}
	b := Result{Payments: []PaymentResult{
		// тот же вопрос, тот же ответ
		attemptsOf("p1", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Approved}),
		// первую попытку вторая стратегия отправила в другой канал: вопрос уже другой
		attemptsOf("p2", AttemptResult{No: 1, ChannelID: "B", Outcome: channel.Approved}),
	}}

	if got := CompareAnswers(a, b); got != (Agreement{Common: 1, Different: 0}) {
		t.Fatalf("%+v", got)
	}
}

func TestCompareAnswersDetectsDifference(t *testing.T) {
	a := Result{Payments: []PaymentResult{
		attemptsOf("p1", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Approved}),
		attemptsOf("p2", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Approved}),
	}}
	b := Result{Payments: []PaymentResult{
		attemptsOf("p1", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Approved}),
		attemptsOf("p2", AttemptResult{No: 1, ChannelID: "A", Outcome: channel.Unknown}),
	}}

	if got := CompareAnswers(a, b); got != (Agreement{Common: 2, Different: 1}) {
		t.Fatalf("%+v", got)
	}
}

func TestCompareAnswersEmpty(t *testing.T) {
	if got := CompareAnswers(Result{}, Result{}); got != (Agreement{}) {
		t.Fatalf("%+v", got)
	}
}

// Главное условие честного сравнения: на один и тот же вопрос (канал, ключ
// попытки) канал отвечает одинаково, какая бы стратегия ни спрашивала.
func TestStrategiesGetSameAnswers(t *testing.T) {
	sc, err := Generate(Options{Seed: 5, Payments: 3000, Duration: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := mustRun(t, sc, strategy.Cheapest{}, 4)
	b := mustRun(t, sc, expectedCost, 4)

	got := CompareAnswers(a, b)
	if got.Common < 100 {
		t.Fatalf("общих вопросов всего %d, проверка бессмысленна", got.Common)
	}
	if got.Different != 0 {
		t.Fatalf("каналы ответили по-разному на %d из %d одинаковых вопросов", got.Different, got.Common)
	}
}

func TestSameRunHasFullAgreement(t *testing.T) {
	sc, _ := Generate(Options{Seed: 6, Payments: 500, Duration: 24 * time.Hour})
	r := mustRun(t, sc, expectedCost, 7)

	attempts := 0
	for _, p := range r.Payments {
		attempts += len(p.Attempts)
	}
	if got := CompareAnswers(r, r); got.Common != attempts || got.Different != 0 {
		t.Fatalf("%+v при %d попытках", got, attempts)
	}
}
