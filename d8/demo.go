package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type demoClient struct{}

func (demoClient) Complete(context.Context, []Message) (string, error) {
	return "Запомнил. Продолжим обсуждение.", nil
}
func runDemo(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "ДЕМО: имитация модели, оценки токенов, условные тарифы 1/0.1/2 USD за миллион. Реальных запросов и списаний нет.")
	for _, scenario := range []struct {
		name   string
		turns  int
		prompt string
	}{
		{"Короткий диалог", 2, "Привет!"},
		{"Длинный диалог", 8, strings.Repeat("Обсудим контекст. ", 12)},
		{"Переполнение", 1, strings.Repeat("Большой запрос. ", 400)},
	} {
		fmt.Fprintln(out, "\n==", scenario.name, "==")
		m := &Meter{Client: demoClient{}, Out: out, ContextLimit: 1600, Reserve: 128, InputPrice: 1, CachedPrice: 0.1, OutputPrice: 2}
		a := NewAgent(m, "Отвечай кратко.", nil, "demo")
		for i := 0; i < scenario.turns; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			fmt.Fprintf(out, "Ход %d\n", i+1)
			if _, err := a.Ask(ctx, scenario.prompt); err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
				break
			}
		}
		fmt.Fprintf(out, "Сообщений в памяти: %d\n", len(a.History()))
	}
	return nil
}
