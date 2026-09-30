package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

type terminalInput struct {
	line string
	err  error
}

func runTerminal(ctx context.Context, a *Agent, in io.Reader, out io.Writer) error {
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()
	if !edited {
		return terminalLineMode(ctx, a, in, out)
	}
	footer, err := newBottomTerminal(out)
	if err != nil {
		return err
	}
	defer footer.Close()
	fmt.Fprintln(footer, "mrkai · RAG с rewrite и фильтрацией · /help — команды")
	return runInteractive(ctx, a, bufio.NewReader(in), footer)
}

func runInteractive(ctx context.Context, a *Agent, reader *bufio.Reader, footer *bottomTerminal) error {
	readCtx, stopReading := context.WithCancel(ctx)
	inputs := make(chan terminalInput, 64)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		history := []string{}
		for {
			line, err := readEditedLine(readCtx, reader, footer, history)
			if readCtx.Err() != nil {
				return
			}
			if strings.TrimSpace(line) != "" {
				if len(history) == 0 || history[len(history)-1] != line {
					history = append(history, line)
				}
				fmt.Fprintln(footer, "→ "+line)
			}
			select {
			case inputs <- terminalInput{line, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil && err != errEscapeInterrupted && err != errInputInterrupted {
				return
			}
		}
	}()
	defer func() { stopReading(); <-readDone }()
	var lastInterrupt time.Time
	var lastKey string
	var queue []string
	var cancel context.CancelFunc
	var finished chan bool
	exitAfterCancel := false
	defer func() {
		if cancel != nil {
			cancel()
			<-finished
		}
	}()
	for {
		if finished == nil && len(queue) > 0 {
			line := queue[0]
			queue = queue[1:]
			executionCtx, stop := context.WithCancel(readCtx)
			cancel = stop
			finished = make(chan bool, 1)
			_ = footer.setOperation("Выполняю")
			go func(done chan<- bool) {
				if handled, exit := runCommand(executionCtx, a, line, footer); handled {
					done <- exit
					return
				}
				answer, _, err := a.Answer(executionCtx, line, func(s string) { fmt.Fprintln(footer, s) })
				if err != nil {
					fmt.Fprintln(footer, "Ошибка:", err)
				} else {
					fmt.Fprintln(footer, answer)
				}
				done <- false
			}(finished)
		}
		select {
		case <-ctx.Done():
			return nil
		case exit := <-finished:
			cancel()
			cancel = nil
			finished = nil
			if exit || exitAfterCancel {
				return nil
			}
			_ = footer.setPlainChat(false)
		case input := <-inputs:
			if input.err == errEscapeInterrupted || input.err == errInputInterrupted {
				key := "Esc"
				if input.err == errInputInterrupted {
					key = "Ctrl+C"
				}
				now := time.Now()
				double := key == lastKey && now.Sub(lastInterrupt) < time.Second
				lastKey, lastInterrupt = key, now
				queue = nil
				if cancel != nil {
					cancel()
					fmt.Fprintf(footer, "%s: останавливаю текущее выполнение…\n", key)
					if double {
						exitAfterCancel = true
					}
				} else if double {
					return nil
				} else {
					fmt.Fprintf(footer, "Нет активного выполнения. Нажмите %s ещё раз для выхода.\n", key)
				}
				continue
			}
			if input.err == io.EOF {
				return nil
			}
			if input.err != nil {
				return input.err
			}
			if strings.TrimSpace(input.line) != "" {
				queue = append(queue, input.line)
			}
		}
	}
}
