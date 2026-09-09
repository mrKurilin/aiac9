package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWorkingLoader(t *testing.T) {
	var out bytes.Buffer
	loader := newWorkingLoader(&out, true)
	loader.Start()
	loader.Stop()
	if !strings.Contains(out.String(), "• Working (0s • esc to interrupt)") {
		t.Fatalf("loader missing: %q", out.String())
	}
	if !strings.HasSuffix(out.String(), "\r\x1b[2K") {
		t.Fatalf("loader line was not cleared: %q", out.String())
	}
	if got := formatElapsed(150 * time.Second); got != "2m 30s" {
		t.Fatalf("elapsed: %q", got)
	}
}

func TestDisabledLoaderWritesNothing(t *testing.T) {
	var out bytes.Buffer
	loader := newWorkingLoader(&out, false)
	loader.Start()
	loader.Stop()
	if out.Len() != 0 {
		t.Fatalf("loader leaked into redirected output: %q", out.String())
	}
}
