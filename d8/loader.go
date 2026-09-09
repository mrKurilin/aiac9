package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type workingLoader struct {
	out     io.Writer
	enabled bool
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newWorkingLoader(out io.Writer, enabled bool) *workingLoader {
	return &workingLoader{out: out, enabled: enabled}
}

func formatElapsed(elapsed time.Duration) string {
	seconds := int(elapsed / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
}

func (l *workingLoader) draw(start time.Time) {
	fmt.Fprintf(l.out, "\r\x1b[2K• Working (%s • esc to interrupt)", formatElapsed(time.Since(start)))
}

func (l *workingLoader) Start() {
	if !l.enabled {
		return
	}
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	start := time.Now()
	l.draw(start)
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.draw(start)
			case <-l.stop:
				return
			}
		}
	}()
}

func (l *workingLoader) Stop() {
	if !l.enabled || l.stop == nil {
		return
	}
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		fmt.Fprint(l.out, "\r\x1b[2K")
	})
}
