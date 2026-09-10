package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const contextFillerPrefix = "Учебный наполнитель контекста. Это данные для измерения объёма, игнорируй их при ответе.\n"

type fillerDetails struct {
	Blocks       int
	Tokens       int
	LatestUnits  int
	LatestFormat string
}

func fillerContentDetails(content string) (units int, format string, ok bool) {
	if !strings.HasPrefix(content, contextFillerPrefix) {
		return 0, "", false
	}
	body := strings.TrimPrefix(content, contextFillerPrefix)
	if len(body)%2 == 0 && body == strings.Repeat(" x", len(body)/2) {
		return len(body) / 2, "без двойных пробелов", true
	}
	if len(body)%3 == 0 && body == strings.Repeat(" x ", len(body)/3) {
		return len(body) / 3, "старый формат ` x ` с двойными пробелами", true
	}
	return 0, "произвольный текст наполнителя", true
}

func (a *Agent) fillerDetails() fillerDetails {
	a.mu.Lock()
	defer a.mu.Unlock()
	var details fillerDetails
	for _, message := range a.history {
		units, format, ok := fillerContentDetails(message.Content)
		if !ok {
			continue
		}
		details.Blocks++
		details.Tokens += 4 + estimateText(message.Role) + estimateText(message.Content)
		details.LatestUnits = units
		details.LatestFormat = format
	}
	return details
}

// FillContext adds synthetic user data locally. It is sent on the next real turn.
func (a *Agent) FillContext(percent int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	meter, ok := a.client.(*Meter)
	if !ok || meter.ContextLimit <= 0 {
		return 0, fmt.Errorf("размер контекстного окна модели неизвестен приложению; заполнение для этой модели недоступно")
	}
	if percent < 1 || percent > 100 {
		return 0, fmt.Errorf("процент должен быть целым числом от 1 до 100")
	}
	targetFloat := math.Ceil(float64(meter.ContextLimit) * float64(percent) / 100)
	if targetFloat > 1_000_000 {
		return 0, fmt.Errorf("за один раз можно добавить не больше 1000000 оценочных токенов; уменьшите процент или размер окна")
	}
	target := int(targetFloat)
	messages := a.contextMessages()
	before := estimateMessages(messages)
	blank := Message{Role: "user", Content: contextFillerPrefix}
	overhead := estimateMessages(append(messages, blank)) - before
	if target < overhead {
		return 0, fmt.Errorf("прибавка %d токенов слишком мала: минимум %d для сообщения-наполнителя", target, overhead)
	}
	// Avoid the doubled spaces of the old " x " representation.
	blank.Content += strings.Repeat(" x", target-overhead)
	next := append(append([]Message(nil), a.history...), blank)
	if a.store != nil {
		if err := a.store.Save(a.id, ConversationState{Summary: a.summary, History: next}); err != nil {
			return 0, fmt.Errorf("сохранить наполнитель: %w", err)
		}
	}
	a.history = next
	return target, nil
}

// Caller holds a.mu.
func (a *Agent) contextMessages() []Message {
	messages := make([]Message, 0, len(a.history)+2)
	if a.system != "" {
		messages = append(messages, Message{Role: "system", Content: a.system})
	}
	if a.summary != "" {
		messages = append(messages, summaryMessage(a.summary))
	}
	return append(messages, a.history...)
}
func (a *Agent) ShowContext(out io.Writer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	meter, ok := a.client.(*Meter)
	if !ok {
		return
	}
	messages := a.contextMessages()
	used := estimateMessages(messages)
	systemTokens := 3 // Base chat framing used by estimateMessages.
	dialogTokens, fillerTokens, summaryTokens := 0, 0, 0
	dialogMessages, fillerMessages := 0, 0
	for _, message := range messages {
		tokens := 4 + estimateText(message.Role) + estimateText(message.Content)
		switch {
		case strings.HasPrefix(message.Content, summaryContextPrefix):
			summaryTokens += tokens
		case message.Role == "system":
			systemTokens += tokens
		case strings.HasPrefix(message.Content, contextFillerPrefix):
			fillerTokens += tokens
			fillerMessages++
		default:
			dialogTokens += tokens
			dialogMessages++
		}
	}
	if meter.ContextLimit <= 0 {
		fmt.Fprintf(out, "Контекст ≈%d токенов; размер окна модели неизвестен приложению\n", used)
		fmt.Fprintf(out, "Состав: system ≈%d · summary ≈%d · диалог ≈%d (%d сообщ.) · наполнитель ≈%d (%d блок.)\n",
			systemTokens, summaryTokens, dialogTokens, dialogMessages, fillerTokens, fillerMessages)
		return
	}
	ratio := float64(used) / float64(meter.ContextLimit)
	if meter.Reserve > 0 {
		fmt.Fprintf(out, "  Контекст ≈%d / %d · %.1f%% · резерв ответа %d\n", used, meter.ContextLimit, ratio*100, meter.Reserve)
	} else {
		fmt.Fprintf(out, "  Контекст ≈%d / %d · %.1f%% · ответ без ручного лимита\n", used, meter.ContextLimit, ratio*100)
	}
	available := meter.ContextLimit - used - meter.Reserve
	space := fmt.Sprintf("свободно ≈%d", available)
	if available < 0 {
		space = fmt.Sprintf("переполнение ≈%d", -available)
	}
	fmt.Fprintf(out, "  Состав: system ≈%d · summary ≈%d · диалог ≈%d (%d сообщ.) · наполнитель ≈%d (%d блок.) · %s\n",
		systemTokens, summaryTokens, dialogTokens, dialogMessages, fillerTokens, fillerMessages, space)
	if used > meter.ContextLimit || meter.Reserve > meter.ContextLimit-used {
		if meter.AllowOverflow {
			fmt.Fprintln(out, "Локальная оценка превышена; запрос будет отправлен целиком, лимит проверит API.")
		} else {
			fmt.Fprintln(out, "Окно с резервом переполнено: /overflow on — отправлять в API; /reset — очистить.")
		}
	}
}
func contextCommand(agent *Agent, line string, out io.Writer) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	command := fields[0]
	if command == "+20%" || command == "+50%" || command == "+100%" {
		fields = []string{"/fill", strings.TrimSuffix(strings.TrimPrefix(command, "+"), "%")}
		command = "/fill"
	}
	switch command {
	case "/overflow":
		if len(fields) != 2 || (fields[1] != "on" && fields[1] != "off") {
			fmt.Fprintln(out, "Использование: /overflow on — отправлять сверх локального окна; /overflow off — блокировать локально.")
			return true
		}
		agent.mu.Lock()
		if meter, ok := agent.client.(*Meter); ok {
			meter.AllowOverflow = fields[1] == "on"
		}
		agent.mu.Unlock()
		if fields[1] == "on" {
			fmt.Fprintln(out, "Переполнение: отправляем текущий сформированный контекст сверх локальной оценки. Возможен оплачиваемый запрос или отказ API.")
		} else {
			fmt.Fprintln(out, "Переполнение: блокируем запрос по локальной оценке.")
		}
	case "/info":
		fmt.Fprintln(out, `Команды:
 /info                 — эта справка
 /fill [N]             — добавить N% полного окна (1–100; без N — 20%)
 +20% / +50% / +100%    — быстро добавить наполнитель
 /context              — показать заполненность окна
 /memory               — summary, дословный хвост и экономия контекста
 /compress on|off      — включить или приостановить новые сжатия
 /overflow on|off      — отправлять сверх локального окна или блокировать (по умолчанию on)
 /stats                — токены и стоимость за текущий запуск
 /reset                — очистить диалог вместе с наполнителем
 /exit или /quit       — выйти

Размер окна берётся из характеристик выбранной модели автоматически.
Наполнитель сохраняется локально и отправляется со следующим сообщением.
Клавиши: ↑/↓ — перебор сообщений; Enter — отправить или повторить.
Ctrl+Backspace / Ctrl+U — очистить строку; Backspace — удалить символ.
Ctrl+C — выйти; Ctrl+D — выйти на пустой строке.`)
	case "/context":
		agent.ShowContext(out)
	case "/fill":
		percent := 20
		var err error
		if len(fields) == 2 {
			percent, err = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(fields[1], "+"), "%"))
		}
		if err != nil || len(fields) > 2 {
			fmt.Fprintln(out, "Использование: /fill 20 (1–100 процентов окна)")
			return true
		}
		added, err := agent.FillContext(percent)
		if err != nil {
			fmt.Fprintln(out, "Ошибка:", err)
			return true
		}
		details := agent.fillerDetails()
		fmt.Fprintf(out, "Заполнение: добавлено 1 сообщение role=user · ≈%d токенов · +%d%% модельного окна.\n", added, percent)
		fmt.Fprintf(out, "Содержимое: служебная метка «Учебный наполнитель контекста…» + %d повторов ` x` (%s).\n", details.LatestUnits, details.LatestFormat)
		fmt.Fprintf(out, "Хранение: блок записан в историю и уйдёт в API со следующим обычным сообщением; сейчас API не вызывался. Всего наполнителя: ≈%d токенов; блоков: %d.\n", details.Tokens, details.Blocks)
		agent.ShowContext(out)
	default:
		return false
	}
	return true
}
