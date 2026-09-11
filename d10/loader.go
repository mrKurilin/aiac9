package main

import (
	"fmt"
	"io"
	"time"
)

type workingLoader struct {
	out     io.Writer
	enabled bool
	stop    chan struct{}
	done    chan struct{}
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

// A turn can make several API calls — facts extraction runs through the same
// meter — so Start may follow Start; retire the running ticker rather than
// leaving it redrawing over the answer.
func (l *workingLoader) Start() {
	if !l.enabled {
		return
	}
	l.Stop()
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

// Stop is idempotent through the nil channel, so the deferred call after a
// finished turn stays harmless.
func (l *workingLoader) Stop() {
	if !l.enabled || l.stop == nil {
		return
	}
	close(l.stop)
	<-l.done
	l.stop, l.done = nil, nil
	fmt.Fprint(l.out, "\r\x1b[2K")
}
