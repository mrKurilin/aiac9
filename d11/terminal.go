package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const terminalHelp = `Команды:
  /remember short|working|long KEY VALUE  — явно сохранить запись в слой
  /forget short|working|long KEY          — удалить запись
  /memory [short|working|long]            — показать память
  /clear short|working|long|all           — очистить слой
  /reset                                  — очистить диалог и всю память
  /info                                   — справка
  /exit                                   — выход`

func printMemory(out io.Writer, layers MemoryLayers, layer string) {
	show := func(name string, value any) {
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintf(out, "%s:\n%s\n", name, data)
	}
	switch layer {
	case "short":
		show("Краткосрочная", layers.ShortTerm)
	case "working":
		show("Рабочая", layers.Working)
	case "long":
		show("Долговременная", layers.LongTerm)
	default:
		show("Краткосрочная", layers.ShortTerm)
		show("Рабочая", layers.Working)
		show("Долговременная", layers.LongTerm)
	}
}

func runMemoryCommand(a *Agent, line string, out io.Writer) (handled, exit bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false, false
	}
	var err error
	switch fields[0] {
	case "/exit", "/quit":
		return true, true
	case "/":
		for _, entry := range slashCommands {
			fmt.Fprintln(out, entry.Label)
		}
	case "/info":
		fmt.Fprintln(out, terminalHelp)
	case "/memory":
		layer := ""
		if len(fields) == 2 {
			layer, err = normalizeLayer(fields[1])
		} else if len(fields) > 2 {
			err = fmt.Errorf("/memory [LAYER]")
		}
		if err == nil {
			printMemory(out, a.Snapshot(), layer)
		}
	case "/remember":
		parts := strings.SplitN(strings.TrimSpace(line), " ", 4)
		if len(parts) != 4 {
			err = fmt.Errorf("/remember LAYER KEY VALUE")
		} else {
			err = a.Remember(parts[1], parts[2], parts[3])
		}
		if err == nil {
			fmt.Fprintln(out, "Сохранено явно в слой", parts[1])
		}
	case "/forget":
		if len(fields) != 3 {
			err = fmt.Errorf("/forget LAYER KEY")
		} else {
			err = a.Forget(fields[1], fields[2])
		}
		if err == nil {
			fmt.Fprintln(out, "Удалено")
		}
	case "/clear":
		if len(fields) != 2 {
			err = fmt.Errorf("/clear LAYER|all")
		} else {
			err = a.Clear(fields[1])
		}
		if err == nil {
			fmt.Fprintln(out, "Слой очищен")
		}
	case "/reset":
		if len(fields) != 1 {
			err = fmt.Errorf("/reset")
		} else {
			err = a.Clear("all")
		}
		if err == nil {
			fmt.Fprintln(out, "Диалог и все слои памяти очищены")
		}
	default:
		err = fmt.Errorf("неизвестная команда; /info — справка")
	}
	if err != nil {
		fmt.Fprintln(out, "Ошибка:", err)
	}
	return true, false
}

func handleTerminalLine(ctx context.Context, a *Agent, line string, out io.Writer) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	if handled, exit := runMemoryCommand(a, line, out); handled {
		return exit, nil
	}
	fmt.Fprint(out, "MrKai > ")
	markdown := newMarkdownWriter(out)
	_, err := a.Ask(ctx, line, markdown.Write)
	flushErr := markdown.Flush()
	if err != nil {
		return false, err
	}
	if flushErr != nil {
		return false, flushErr
	}
	fmt.Fprintln(out)
	if warning := a.DrainMemoryWarning(); warning != "" {
		fmt.Fprintln(out, "Предупреждение: память не обновлена:", warning)
	}
	if notice := a.DrainToolNotice(); notice != "" {
		fmt.Fprintln(out, "Источник:", notice)
	}
	return false, nil
}

func runTerminal(ctx context.Context, a *Agent, in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "mrkai · память: краткосрочная / рабочая / долговременная")
	fmt.Fprintln(out, "Важные факты автоматически распределяются по слоям; полные сообщения на диск не пишутся. /info — команды.")
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()
	if edited {
		fmt.Fprintln(out, "/ — команды · ↑/↓ — выбор и история · Tab — дополнить · Enter — подтвердить · Esc — закрыть меню.")
		reader := bufio.NewReader(in)
		history := []string{}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			line, err := readEditedLine(ctx, reader, out, memoryCompleter(a), history)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fmt.Fprintln(out, "Вы >", terminalControls.ReplaceAllString(line, ""))
			if len(history) == 0 || history[len(history)-1] != line {
				history = append(history, line)
			}
			exit, err := handleTerminalLine(ctx, a, line, out)
			if exit {
				return nil
			}
			if err != nil {
				fmt.Fprintln(out, "\nОшибка:", err)
			}
		}
	}
	scanner := bufio.NewScanner(in)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 2<<20)
	for {
		fmt.Fprint(out, "\nВы > ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		exit, err := handleTerminalLine(ctx, a, scanner.Text(), out)
		if exit {
			return nil
		}
		if err != nil {
			fmt.Fprintln(out, "\nОшибка:", err)
		}
	}
}
