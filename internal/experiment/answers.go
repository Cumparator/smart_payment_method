package experiment

import (
	"github.com/Cumparator/smart_payment_method/internal/channel"
	"github.com/Cumparator/smart_payment_method/internal/payment"
)

// Agreement — итог сравнения ответов каналов в двух прогонах одного сценария.
type Agreement struct {
	Common    int // вопросов «канал + ключ попытки», заданных в обоих прогонах
	Different int // из них, на которые канал ответил по-разному
}

// CompareAnswers сопоставляет ответы каналов на одинаковые вопросы в двух
// прогонах одного сценария с одним seed. Стратегии выбирают каналы по-разному,
// поэтому вопросы совпадают не все, но на совпавшие ответ обязан быть один:
// иначе разницу в итогах нельзя приписать выбору канала, и сравнение стратегий
// нечестное. При верной работе Different равно нулю.
func CompareAnswers(a, b Result) Agreement {
	type question struct{ channelID, key string }
	key := func(p PaymentResult, at AttemptResult) question {
		return question{at.ChannelID, payment.Attempt{PaymentID: p.ID, AttemptNo: at.No}.ChannelKey()}
	}

	answers := make(map[question]channel.ChargeOutcome)
	for _, p := range a.Payments {
		for _, at := range p.Attempts {
			answers[key(p, at)] = at.Outcome
		}
	}

	var ag Agreement
	for _, p := range b.Payments {
		for _, at := range p.Attempts {
			if out, ok := answers[key(p, at)]; ok {
				ag.Common++
				if out != at.Outcome {
					ag.Different++
				}
			}
		}
	}
	return ag
}
