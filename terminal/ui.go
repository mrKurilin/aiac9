package terminal

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

// Handler executes one submitted line and reports whether the shell should exit.
// The supplied writer also supports ClearChat for reset commands.
type Handler func(context.Context, string, io.Writer) bool

func Run(ctx context.Context, in io.Reader, out io.Writer, title string, commands []Command, handle Handler) error {
	setCommands(commands)
	edited, restore, err := prepareInput(in, out)
	if err != nil {
		return err
	}
	defer restore()
	if !edited {
		return lineMode(ctx, in, out, handle)
	}
	footer, err := newBottomTerminal(out)
	if err != nil {
		return err
	}
	defer footer.Close()
	fmt.Fprintln(footer, title)
	return runInteractive(ctx, bufio.NewReader(in), footer, handle)
}

func lineMode(ctx context.Context, in io.Reader, out io.Writer, handle Handler) error {
	scanner := bufio.NewScanner(in)
	for {
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := scanner.Text()
		if strings.Contains(line, "\t") {
			prefix := strings.ReplaceAll(line, "\t", "")
			values := []string{}
			for _, item := range commandCompletions(prefix) {
				values = append(values, item.Value)
			}
			fmt.Fprintln(out, strings.Join(values, "  "))
			continue
		}
		if strings.TrimSpace(line) != "" {
			PrintMessage(out, "ВЫ", line)
		}
		if handle(ctx, line, out) {
			return nil
		}
	}
}

func runInteractive(ctx context.Context, reader *bufio.Reader, footer *bottomTerminal, handle Handler) error {
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
				PrintMessage(footer, "ВЫ", line)
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
			go func(done chan<- bool) { done <- handle(executionCtx, line, footer) }(finished)
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
					PrintDiagnostic(footer, key+": останавливаю текущее выполнение…")
					if double {
						exitAfterCancel = true
					}
				} else if double {
					return nil
				} else {
					PrintDiagnostic(footer, "Нет активного выполнения. Нажмите "+key+" ещё раз для выхода.")
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
