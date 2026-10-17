package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type traceTurnKey struct{}
type traceLog struct {
	mu             sync.Mutex
	file           *os.File
	path, session  string
	size, maxBytes int64
	sequence       atomic.Uint64
	secrets        []string
	failure        error
}

func openTrace(dir string, secrets []string) (*traceLog, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("не удалось создать каталог диагностического журнала")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("каталог диагностики должен быть обычным каталогом")
	}
	path := filepath.Join(dir, "trace.jsonl")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("журнал диагностики должен быть обычным файлом")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть диагностический журнал")
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, fmt.Errorf("не удалось ограничить доступ к журналу")
	}
	info, err = f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("не удалось проверить журнал")
	}
	return &traceLog{file: f, path: path, size: info.Size(), maxBytes: 5 << 20, session: fmt.Sprint(time.Now().UnixNano()), secrets: secrets}, nil
}
func (t *traceLog) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
}
func (t *traceLog) turn(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, traceTurnKey{}, fmt.Sprintf("%s-%d", t.session, t.sequence.Add(1)))
}
func (t *traceLog) callID() uint64 {
	if t == nil {
		return 0
	}
	return t.sequence.Add(1)
}
func (t *traceLog) status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure != nil {
		return "Журнал: " + t.path + "; запись остановлена из-за ошибки файловой системы."
	}
	return "Журнал: " + t.path + "; запись включена. Ротация: 5 МиБ, до трёх файлов."
}

var traceAssignments = regexp.MustCompile(`(?i)((?:api[_-]?key|password|passwd|secret|token|authorization|x-access-token)\s*["']?\s*[:=]\s*["']?)([^\s"',;<>]+)`)
var traceTokens = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|\d{5,12}:[a-z0-9_-]{20,}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)\b`)
var traceAuth = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[a-z0-9._~+/=-]+`)
var traceIP = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var traceHost = regexp.MustCompile(`(?i)\b[\w-]+(?:\.[\w-]+)*\.(?:local|internal|intranet|corp|lan|svc|priv|vpn)\b`)
var traceURLPassword = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`)
var tracePrivateKey = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
var traceOtherTokens = regexp.MustCompile(`\b(?:A(?:KIA|SIA)[0-9A-Z]{16}|glpat-[A-Za-z0-9_-]{18,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35})\b`)
var traceThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
var traceIPv6 = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,}[0-9a-f:.]*`)

func (t *traceLog) redact(text string) string {
	for _, secret := range t.secrets {
		if len(secret) >= 3 {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	text = traceTokens.ReplaceAllString(text, "[REDACTED]")
	text = traceOtherTokens.ReplaceAllString(text, "[REDACTED]")
	text = tracePrivateKey.ReplaceAllString(text, "[REDACTED]")
	text = traceThinkBlock.ReplaceAllString(text, "[OMITTED]")
	text = traceAuth.ReplaceAllString(text, "[REDACTED]")
	text = traceAssignments.ReplaceAllString(text, "${1}[REDACTED]")
	text = traceURLPassword.ReplaceAllString(text, "${1}[REDACTED]@")
	text = traceIP.ReplaceAllString(text, "[REDACTED-IP]")
	text = traceIPv6.ReplaceAllStringFunc(text, func(value string) string {
		if net.ParseIP(value) != nil {
			return "[REDACTED-IP]"
		}
		return value
	})
	return traceHost.ReplaceAllString(text, "[REDACTED-HOST]")
}
func (t *traceLog) sanitize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, entry := range v {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			switch normalized {
			case "authorization", "x_access_token", "token", "api_key", "password", "secret", "signature", "reasoning_content", "reasoning", "chain_of_thought", "analysis", "thoughts", "reasoning_details":
				v[key] = "[REDACTED]"
			default:
				v[key] = t.sanitize(entry)
			}
		}
		return v
	case []any:
		for i := range v {
			v[i] = t.sanitize(v[i])
		}
		return v
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "```json") && strings.HasSuffix(trimmed, "```") {
			trimmed = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "```json"), "```"))
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var nested any
			if json.Unmarshal([]byte(trimmed), &nested) == nil {
				encoded, _ := json.Marshal(t.sanitize(nested))
				return t.redact(string(encoded))
			}
		}
		return t.redact(v)
	default:
		return v
	}
}
func (t *traceLog) event(ctx context.Context, event string, data any) {
	if t == nil {
		return
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return
	}
	var generic any
	if json.Unmarshal(encoded, &generic) != nil {
		return
	}
	generic = t.sanitize(generic)
	encoded, _ = json.Marshal(generic)
	if len(encoded) > 256<<10 {
		preview := []rune(string(encoded))
		generic = map[string]any{"truncated": true, "bytes": len(encoded), "preview": string(preview[:min(len(preview), 64<<10)])}
	}
	turn, _ := ctx.Value(traceTurnKey{}).(string)
	line, err := json.Marshal(map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "session_id": t.session, "turn_id": turn, "event": event, "data": generic})
	if err != nil {
		return
	}
	line = append(line, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil || t.failure != nil {
		return
	}
	if t.size+int64(len(line)) > t.maxBytes {
		_ = t.file.Close()
		t.file = nil
		// Only rotate this known diagnostic file; never follow an existing backup link.
		if err := os.Rename(t.path+".1", t.path+".2"); err != nil && !os.IsNotExist(err) {
			t.failure = err
			return
		}
		if err := os.Rename(t.path, t.path+".1"); err != nil {
			t.failure = err
			return
		}
		t.file, err = os.OpenFile(t.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.failure = err
			return
		}
		t.size = 0
	}
	written, err := t.file.Write(line)
	t.size += int64(written)
	if err == nil {
		err = t.file.Sync()
	}
	if err != nil {
		t.failure = err
	}
}
