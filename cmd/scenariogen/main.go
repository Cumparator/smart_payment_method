// Command scenariogen — генератор сценария эксперимента в .jsonl:
// платежи с логнормальными суммами и расписание аварий каналов.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Cumparator/smart_payment_method/internal/experiment"
)

func main() {
	seed := flag.Int64("seed", 1, "seed генератора")
	payments := flag.Int("payments", 20000, "число платежей")
	duration := flag.Duration("duration", 24*time.Hour, "длительность сценария")
	kind := flag.String("kind", experiment.KindMixed, "вид сценария: baseline или mixed")
	out := flag.String("out", "", `файл результата; по умолчанию scenarios/<kind>-<seed>.jsonl, "-" — stdout`)
	flag.Parse()

	s, err := experiment.Generate(experiment.Options{
		Seed: *seed, Payments: *payments, Duration: *duration, Kind: *kind,
	})
	if err != nil {
		log.Fatal(err)
	}

	path := *out
	if path == "" {
		path = filepath.Join("scenarios", fmt.Sprintf("%s-%d.jsonl", *kind, *seed))
	}
	w := os.Stdout
	if path != "-" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		w = f
	}

	bw := bufio.NewWriter(w)
	if err := experiment.Write(bw, s); err != nil {
		log.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		log.Fatal(err)
	}
	if path != "-" {
		log.Printf("записано %d платежей, %d каналов → %s", len(s.Payments), len(s.Header.Channels), path)
	}
}
