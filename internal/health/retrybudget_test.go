package health

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestRetryBudgetFloorAllowsFewRetriesAtStart(t *testing.T) {
	b := NewRetryBudget(0.2, 100, 10)

	first, second := b.TryRetry(), b.TryRetry()
	if !first || !second {
		t.Fatal("в начале работы два повтора (20% от базы 10) должны пройти")
	}
	if b.TryRetry() {
		t.Fatal("третий повтор превышает 20% базы")
	}
}

func TestRetryBudgetGrowsWithFlow(t *testing.T) {
	b := NewRetryBudget(0.2, 100, 10)
	for range 80 {
		b.RecordFirst()
	}
	allowed := 0
	for b.TryRetry() {
		allowed++
		if allowed > 100 {
			t.Fatal("бюджет не ограничивает повторы")
		}
	}
	// 80 первых попыток: повторы могут занять до 20% окна, то есть 20 из 100
	if allowed < 19 || allowed > 21 {
		t.Fatalf("разрешено %d повторов, ожидалось около 20", allowed)
	}
}

// Все платежи заканчиваются ошибкой канала и просят по два повтора: бюджет
// должен удержать долю повторов в пределах пятой части всех попыток.
func TestRetryBudgetCapsRetryShareUnderTotalFailure(t *testing.T) {
	b := NewRetryBudget(0.2, 100, 10)
	const payments = 1000
	retries := 0
	for range payments {
		b.RecordFirst()
		for range 2 {
			if !b.TryRetry() {
				break
			}
			retries++
		}
	}
	share := float64(retries) / float64(payments+retries)
	if share > 0.2+2.0/float64(payments) {
		t.Fatalf("повторов %d из %d попыток (%.1f%%), больше 20%%", retries, payments+retries, share*100)
	}
	if retries < 200 {
		t.Fatalf("повторов всего %d: бюджет не должен быть заметно строже 20%%", retries)
	}
}

func TestRetryBudgetRecoversWhenFlowIsHealthy(t *testing.T) {
	b := NewRetryBudget(0.2, 50, 10)
	for range 50 {
		b.RecordFirst()
		b.TryRetry()
	}
	if b.TryRetry() {
		t.Fatal("бюджет должен быть исчерпан")
	}
	for range 50 { // поток без ошибок вытесняет старые повторы из окна
		b.RecordFirst()
	}
	if !b.TryRetry() {
		t.Fatal("после здорового потока повторы снова должны быть доступны")
	}
}

func TestRetryBudgetDenialTakesNothing(t *testing.T) {
	b := NewRetryBudget(0.2, 100, 10)
	b.TryRetry()
	b.TryRetry()
	for range 50 {
		if b.TryRetry() {
			t.Fatal("отказанные повторы не должны расходовать окно")
		}
	}
	for range 40 { // 40 первых попыток дают место ещё на 8 повторов, а не меньше
		b.RecordFirst()
	}
	got := 0
	for b.TryRetry() {
		got++
	}
	if got < 7 || got > 9 {
		t.Fatalf("после 40 первых попыток разрешено %d повторов, ожидалось около 8", got)
	}
}

func TestRetryBudgetDefaults(t *testing.T) {
	b := NewRetryBudget(0, 0, 0)
	if b.ratio != DefaultRetryRatio || len(b.ring) != DefaultRetryWindow || b.floor != DefaultRetryFloor {
		t.Fatalf("%+v", b)
	}
}

func TestRetryBudgetConcurrent(t *testing.T) {
	b := NewRetryBudget(0.2, 100, 10)
	var wg sync.WaitGroup
	var firsts, retries atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				b.RecordFirst()
				firsts.Add(1)
				if b.TryRetry() {
					retries.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	total := firsts.Load() + retries.Load()
	if float64(retries.Load()) > 0.2*float64(total)+3 {
		t.Fatalf("повторов %d из %d попыток", retries.Load(), total)
	}
}
