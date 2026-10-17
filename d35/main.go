package main

import (
	terminal "aiac9-terminal"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var commands = []terminal.Command{
	{Value: "/help", Description: "условия поиска и примеры", Submit: true},
	{Value: "/log", Description: "путь и состояние диагностического журнала", Submit: true},
	{Value: "/reset", Description: "новый поиск", Submit: true},
	{Value: "/clear", Description: "новый поиск", Submit: true},
	{Value: "/exit", Description: "выход", Submit: true},
	{Value: "/quit", Description: "выход", Submit: true},
}

const helpText = "Опишите поездку: откуда, куда (или куда угодно по стране или миру), даты/месяц, в одну сторону или туда-обратно, бюджет в рублях, нужны ли прямые рейсы. Например: Из Казани в Сочи в ноябре, туда-обратно на 5–7 дней, до 20000 рублей, без пересадок. Можно уточнять: А если с пересадкой?\nТолько один взрослый, эконом. Источник — кэш Aviasales, актуальность цены проверяйте по ссылке. Багаж не фильтруется. /reset или /clear — новый поиск; /cancel — отмена; /id — ваш Telegram ID."

func newHTTP(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func moscowNow() time.Time { return time.Now().In(time.FixedZone("MSK", 3*60*60)) }
func configuredAgent() (*agent, error) {
	for _, name := range []string{"DEEPSEEK_API_KEY", "TRAVELPAYOUTS_TOKEN"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return nil, fmt.Errorf("задайте %s", name)
		}
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	endpoint := os.Getenv("DEEPSEEK_BASE_URL")
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	travelBase := "https://api.travelpayouts.com"
	if override := os.Getenv("TRAVELPAYOUTS_BASE_URL"); override != "" {
		u, err := url.Parse(override)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return nil, fmt.Errorf("TRAVELPAYOUTS_BASE_URL допускает только локальный HTTP-сервер тестирования")
		}
		travelBase = override
	}
	travel := &travelClient{key: os.Getenv("TRAVELPAYOUTS_TOKEN"), base: travelBase, http: newHTTP(30 * time.Second)}
	return &agent{client: deepSeekClient{key: os.Getenv("DEEPSEEK_API_KEY"), url: endpoint, model: model, http: newHTTP(60 * time.Second)}, travel: travel, now: moscowNow}, nil
}
func stateDir() string {
	if s := os.Getenv("DATA_DIR"); s != "" {
		return s
	}
	return "data"
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("mrkai — поиск авиабилетов в терминале. Ключи: TRAVELPAYOUTS_TOKEN и DEEPSEEK_API_KEY. Дополнительно: --telegram, --healthcheck.")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		info, err := os.Stat(filepath.Join(stateDir(), "heartbeat"))
		if err != nil || time.Since(info.ModTime()) > 2*time.Minute {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 2 || (len(os.Args) == 2 && os.Args[1] != "--telegram") {
		fmt.Fprintln(os.Stderr, "Использование: mrkai [--telegram]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 {
		// A previous container's heartbeat must never certify this startup.
		if err := os.Remove(filepath.Join(stateDir(), "heartbeat")); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Ошибка: не удалось сбросить проверку готовности")
			os.Exit(1)
		}
	}
	a, err := configuredAgent()
	if err == nil {
		var trace *traceLog
		trace, err = openTrace(stateDir(), []string{os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("TRAVELPAYOUTS_TOKEN"), os.Getenv("TELEGRAM_BOT_TOKEN")})
		if err == nil {
			defer trace.close()
			a.trace = trace
			a.travel.trace = trace
			if client, ok := a.client.(deepSeekClient); ok {
				client.trace = trace
				a.client = client
			}
			trace.event(ctx, "session.start", map[string]any{"mode": map[bool]string{true: "telegram", false: "terminal"}[len(os.Args) == 2]})
			fmt.Fprintln(os.Stderr, "Диагностика:", trace.path)
		}
	}
	if err == nil {
		fmt.Fprintln(os.Stderr, "Aviasales: загружаю мировой справочник городов")
		err = a.travel.loadCities(ctx)
	}
	if err == nil {
		if len(os.Args) == 2 {
			err = runTelegram(ctx, a)
		} else {
			err = run(ctx, a, os.Stdin, os.Stdout)
		}
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, a *agent, in io.Reader, out io.Writer) error {
	handler := func(ctx context.Context, line string, out io.Writer) bool {
		line = strings.TrimSpace(line)
		switch line {
		case "/exit", "/quit":
			return true
		case "/help":
			fmt.Fprintln(out, strings.Split(helpText, " /reset")[0]+"\nКоманды: /help, /log, /reset, /clear, /exit, /quit. Esc/Ctrl+C — отмена операции; повторное нажатие — выход.")
		case "/log":
			if a.trace == nil {
				fmt.Fprintln(out, "Диагностический журнал не подключён.")
			} else {
				fmt.Fprintln(out, a.trace.status())
			}
		case "/reset", "/clear":
			a.reset()
			a.trace.event(ctx, "agent.reset", map[string]string{"command": line})
			if screen, ok := out.(interface{ ClearChat() error }); ok {
				_ = screen.ClearChat()
			}
			fmt.Fprintln(out, "Новый поиск. История очищена.")
		default:
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(out, "Неизвестная команда. /help")
				break
			}
			answer, err := a.answer(ctx, line, func(s string) { terminal.PrintDiagnostic(out, s) })
			if err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			} else {
				terminal.PrintMessage(out, "АВИАБИЛЕТЫ", answer)
			}
		}
		return false
	}
	return terminal.Run(ctx, in, out, "mrkai · авиабилеты", commands, handler)
}
func parseAllowed(raw string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	if strings.TrimSpace(raw) == "" {
		return ids, nil
	}
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("ALLOWED_TELEGRAM_IDS: нужны числовые ID через запятую")
		}
		ids[id] = true
	}
	return ids, nil
}
