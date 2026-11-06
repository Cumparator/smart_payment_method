package health

import (
	"sync"
	"testing"

	"github.com/Cumparator/smart_payment_method/internal/channel"
)

func TestWindowEvictsOldest(t *testing.T) {
	r := New(3)
	r.Observe("A", channel.Approved)
	r.Observe("A", channel.Approved)
	r.Observe("B", channel.ChannelError)
	r.Observe("B", channel.Unknown) // первый успех A вытеснен

	if s, n := r.Counts("A"); s != 1 || n != 1 {
		t.Fatalf("A: %d/%d, ожидалось 1/1", s, n)
	}
	if s, n := r.Counts("B"); s != 0 || n != 2 {
		t.Fatalf("B: %d/%d, ожидалось 0/2", s, n)
	}
	if r.Filled() != 3 {
		t.Fatalf("в окне %d исходов, ожидалось 3", r.Filled())
	}
}

func TestDeclinedIsNotTechnical(t *testing.T) {
	r := New(10)
	r.Observe("A", channel.Declined)
	if s, n := r.Counts("A"); s != 0 || n != 0 || r.Filled() != 0 {
		t.Fatalf("DECLINED попал в окно: %d/%d, заполнено %d", s, n, r.Filled())
	}
}

func TestUnknownChannelHasNoStats(t *testing.T) {
	if s, n := New(0).Counts("nope"); s != 0 || n != 0 {
		t.Fatalf("%d/%d", s, n)
	}
}

func TestIdleChannelForgotten(t *testing.T) {
	r := New(5)
	r.Observe("A", channel.ChannelError)
	for range 5 {
		r.Observe("B", channel.Approved)
	}
	if _, n := r.Counts("A"); n != 0 {
		t.Fatalf("A не выпал из окна: %d исходов", n)
	}
}

func TestConcurrentObserve(t *testing.T) {
	r := New(100)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('A' + g%2))
			for range 500 {
				r.Observe(id, channel.Approved)
			}
		}()
	}
	wg.Wait()

	_, a := r.Counts("A")
	_, b := r.Counts("B")
	if a+b != 100 || r.Filled() != 100 {
		t.Fatalf("A=%d B=%d filled=%d, ожидалось 100 в сумме", a, b, r.Filled())
	}
}
