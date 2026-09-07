# День 6 — первый LLM-агент

Веб-чат на Go принимает сообщения из браузера и отправляет их в DeepSeek API. `Agent` — отдельная сущность: она хранит историю конкретного браузера, формирует контекст и вызывает LLM через интерфейс `ChatClient`. Токен DeepSeek остаётся на сервере и никогда не передаётся странице.

## Что понадобится

- Go 1.21 или новее (`go version` для проверки);
- DeepSeek API-токен и положительный баланс API-аккаунта;
- доступ в интернет.

Токен берётся в кабинете DeepSeek: <https://platform.deepseek.com/api_keys>. Не вставляйте его в исходный код и не коммитьте в Git.

## Запуск на macOS или Linux

1. Откройте Terminal и перейдите в каталог проекта:

   ```bash
   cd /путь/к/AiAdventChallenge-9/aiac9/d6
   ```

2. Передайте токен и придумайте отдельный пароль для посетителей чата:

   ```bash
   export DEEPSEEK_API_KEY='sk-ваш_токен'
   export CHAT_PASSWORD='длинный-случайный-пароль'
   ```

3. Запустите агента:

   ```bash
   go run .
   ```

4. Откройте <http://localhost:8080>. Браузер спросит логин и пароль: логин может быть любым, пароль — значение `CHAT_PASSWORD`. Кнопка «Новый диалог» очищает историю текущего браузера.

Переменная, заданная через `export`, действует только в этом окне терминала. Одноразовый запуск без сохранения токена в истории команды можно сделать так:

```bash
read -s DEEPSEEK_API_KEY
export DEEPSEEK_API_KEY
read -s CHAT_PASSWORD
export CHAT_PASSWORD
go run .
unset DEEPSEEK_API_KEY CHAT_PASSWORD
```

После `read` вставьте токен, нажмите Enter — введённые символы на экране не показываются.

## Запуск на Windows PowerShell

```powershell
cd C:\путь\к\AiAdventChallenge-9\aiac9\d6
$env:DEEPSEEK_API_KEY = "sk-ваш_токен"
$env:CHAT_PASSWORD = "длинный-случайный-пароль"
go run .
```

Удалить токен из текущей PowerShell-сессии после работы:

```powershell
Remove-Item Env:DEEPSEEK_API_KEY, Env:CHAT_PASSWORD
```

## Публикация в интернете через HTTPS

Сам по себе `go run .` запускает веб-сервер на вашей машине. Чтобы получить публичную HTTPS-ссылку без настройки роутера, можно использовать Cloudflare Quick Tunnel.

1. Установите `cloudflared` один раз. На macOS с Homebrew:

   ```bash
   brew install cloudflared
   ```

   Для Windows можно выполнить `winget install --id Cloudflare.cloudflared` или скачать программу с официальной страницы Cloudflare.

2. В первом терминале запустите чат и не закрывайте его:

   ```bash
   cd /путь/к/AiAdventChallenge-9/aiac9/d6
   export DEEPSEEK_API_KEY='sk-ваш_токен'
   export CHAT_PASSWORD='длинный-случайный-пароль'
   go run .
   ```

3. Во втором терминале создайте туннель:

   ```bash
   cloudflared tunnel --url http://localhost:8080
   ```

4. В выводе появится адрес наподобие `https://random-words.trycloudflare.com`. Отправьте эту ссылку пользователю и отдельно сообщите пароль. Логин в окне авторизации может быть любым.

Ссылка работает, пока запущены **оба** процесса и компьютер не спит. При следующем запуске Quick Tunnel выдаст другой адрес. Для постоянного адреса нужен именованный Cloudflare Tunnel или деплой приложения на сервер.

> Не запускайте публичный туннель без `CHAT_PASSWORD`: посторонние смогут отправлять запросы за ваш счёт. Не сообщайте посетителям `DEEPSEEK_API_KEY` — им нужен только пароль чата.

## Настройки

Необязательные переменные окружения:

- `DEEPSEEK_MODEL` — модель, по умолчанию `deepseek-chat`; например `deepseek-reasoner`.
- `DEEPSEEK_BASE_URL` — endpoint, по умолчанию `https://api.deepseek.com/chat/completions`; удобно для тестового совместимого сервера.
- `AGENT_SYSTEM_PROMPT` — системная инструкция агенту.
- `CHAT_PASSWORD` — пароль HTTP Basic Auth; настоятельно обязателен для публичного доступа.
- `ADDR` — адрес веб-сервера, по умолчанию `:8080` (все сетевые интерфейсы).

Пример:

```bash
export DEEPSEEK_MODEL='deepseek-reasoner'
export AGENT_SYSTEM_PROMPT='Ты преподаватель Go. Объясняй с короткими примерами.'
go run .
```

## Проверка без расходования токенов

Автотест использует подменный HTTP-транспорт и проверяет авторизацию, вызов API и передачу истории без сети:

```bash
go test ./...
```

## Структура

- `main.go` — запускает HTTP-сервер;
- `web.go` и `index.html` — API, сессии, авторизация и интерфейс чата;
- `agent.go` — агент и клиент DeepSeek API;
- `agent_test.go` — тесты агента и HTTP-интеграции.

Если API отвечает `HTTP 401`, проверьте токен. При `HTTP 402` проверьте баланс. Если запрос зависает или завершается сетевой ошибкой, проверьте интернет, VPN/прокси и доступность `api.deepseek.com`.
