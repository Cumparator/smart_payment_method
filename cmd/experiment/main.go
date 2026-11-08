// Command experiment — прогон одного сценария обеими стратегиями на виртуальных
// часах. Каналы отвечают одинаково, поэтому разница в итогах вызвана только
// выбором канала.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/experiment"
	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/strategy"
)

func main() {
	scenarioPath := flag.String("scenario", "", "файл сценария .jsonl (cmd/scenariogen)")
	seed := flag.Int64("seed", 1, "seed прогона: от него зависят ответы каналов")
	flag.Parse()

	if *scenarioPath == "" {
		log.Fatal("укажите сценарий: -scenario scenarios/mixed-1.jsonl")
	}
	f, err := os.Open(*scenarioPath)
	if err != nil {
		log.Fatal(err)
	}
	sc, err := experiment.Read(f)
	f.Close()
	if err != nil {
		log.Fatalf("%s: %v", *scenarioPath, err)
	}

	strategies := []strategy.Strategy{
		strategy.Cheapest{},
		// Параметры ExpectedCost стартовые, не подобранные.
		strategy.ExpectedCost{FailurePenalty: 5_000, SecondCost: 50, LoadThreshold: 0.7, LoadPenalty: 1_000},
	}

	fmt.Printf("Сценарий %s: %d платежей, %d каналов, %s; seed прогона: %d\n\n",
		*scenarioPath, len(sc.Payments), len(sc.Header.Channels), time.Duration(sc.Header.Duration), *seed)

	ctx := context.Background()
	results := make([]experiment.Result, len(strategies))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Стратегия\tУспешно\tНе прошло\tПопыток")
	for i, s := range strategies {
		res, err := experiment.Run(ctx, sc, s, *seed)
		if err != nil {
			log.Fatalf("%s: %v", s.Name(), err)
		}
		results[i] = res
		succeeded, failed, attempts := totals(res)
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", s.Name(), succeeded, failed, attempts)
	}
	if err := tw.Flush(); err != nil {
		log.Fatal(err)
	}

	ag := experiment.CompareAnswers(results[0], results[1])
	fmt.Printf("\nОдинаковых вопросов к каналам (канал + ключ попытки): %d, ответили по-разному: %d\n", ag.Common, ag.Different)
	if ag.Different != 0 {
		log.Fatal("каналы отвечают по-разному: сравнение стратегий некорректно")
	}
}

// totals считает успешные и неуспешные платежи и все попытки прогона.
func totals(r experiment.Result) (succeeded, failed, attempts int) {
	for _, p := range r.Payments {
		if p.Final == payment.Succeeded {
			succeeded++
		} else {
			failed++
		}
		attempts += len(p.Attempts)
	}
	return succeeded, failed, attempts
}
