#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

usage() {
  echo 'Из каталога d35: sudo bash deploy/bootstrap-vps.sh'
  echo 'Проверка исходников без установки: bash deploy/bootstrap-vps.sh --check'
}
fail() { echo "Ошибка: $1" >&2; exit 1; }
script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
day_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
if [[ ${1:-} == --help ]]; then usage; exit 0; fi
[[ $# == 0 || ( $# == 1 && $1 == --check ) ]] || { usage; exit 2; }
[[ $(uname -s) == Linux ]] || fail 'скрипт предназначен для Linux VPS (Ubuntu 24.04/26.04).'
for path in Dockerfile go.mod main.go agent.go travel.go telegram.go; do
  [[ -f "$day_dir/$path" ]] || fail "не найден исходный файл $path"
done
[[ -f "$day_dir/../terminal/go.mod" ]] || fail 'нужен полный checkout aiac9, включая каталог terminal рядом с d35.'
if [[ ${1:-} == --check ]]; then
  echo 'Структура исходников корректна. Проверка не устанавливает Docker и не проверяет ключи или внешние API.'
  exit 0
fi
[[ $(id -u) == 0 ]] || fail 'запустите через sudo bash deploy/bootstrap-vps.sh.'
command -v systemctl >/dev/null || fail 'требуется VPS с systemd.'
if ! command -v docker >/dev/null || ! docker buildx version >/dev/null 2>&1; then
  [[ -f /etc/os-release ]] || fail 'не удалось определить Linux.'
  . /etc/os-release
  [[ ${ID:-} == ubuntu && ( ${VERSION_ID:-} == 24.04 || ${VERSION_ID:-} == 26.04 ) ]] || fail 'автоустановка Docker поддерживает Ubuntu 24.04/26.04; на другой системе установите Docker Engine и Buildx вручную.'
  echo 'Устанавливаю Docker и Buildx из репозитория Ubuntu…'
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io docker-buildx
fi
systemctl enable --now docker >/dev/null
command -v docker >/dev/null || fail 'Docker не установлен.'
docker info >/dev/null 2>&1 || fail 'Docker daemon недоступен.'
docker buildx version >/dev/null 2>&1 || fail 'нужен Docker Buildx с поддержкой --build-context.'

# Pass only source files to the additional context, never the complete checkout.
work_dir=$(mktemp -d)
cleanup() { rm -f -- "$work_dir/runtime.conf"; rm -f -- "$work_dir/terminal/"*.go "$work_dir/terminal/go.mod"; rmdir -- "$work_dir/terminal" "$work_dir" 2>/dev/null || true; }
trap cleanup EXIT
mkdir "$work_dir/terminal"
cp "$day_dir/../terminal/go.mod" "$work_dir/terminal/"
for path in "$day_dir/../terminal/"*.go; do
  [[ $path == *_test.go ]] || cp "$path" "$work_dir/terminal/"
done
image="mrkai-flights:$(date +%Y%m%d%H%M%S)"
echo 'Собираю образ бота…'
docker buildx build --load --build-context "terminal=$work_dir/terminal" -t "$image" "$day_dir"

[[ -r /dev/tty && -w /dev/tty ]] || fail 'нужен интерактивный терминал для ввода ключей.'
read -r -s -p 'Токен Telegram-бота от @BotFather: ' telegram_value </dev/tty; echo >/dev/tty
[[ $telegram_value =~ ^[0-9]+:[A-Za-z0-9_-]{20,}$ ]] || fail 'неверный формат токена Telegram.'
read -r -s -p 'API-токен Travelpayouts (программа Aviasales): ' travel_value </dev/tty; echo >/dev/tty
[[ $travel_value =~ ^[A-Za-z0-9_-]{10,200}$ ]] || fail 'неверный формат токена Travelpayouts.'
read -r -s -p 'API-ключ DeepSeek: ' deepseek_value </dev/tty; echo >/dev/tty
[[ $deepseek_value =~ ^[A-Za-z0-9_-]{10,200}$ ]] || fail 'неверный формат ключа DeepSeek.'
read -r -p 'Разрешённые Telegram ID через запятую (Enter — публичный бот): ' allowed_value </dev/tty
[[ -z $allowed_value || $allowed_value =~ ^[0-9]+(,[0-9]+)*$ ]] || fail 'введите числовые ID через запятую без пробелов.'
{
 printf 'TELEGRAM_BOT_TOKEN=%s\n' "$telegram_value"
 printf 'TRAVELPAYOUTS_TOKEN=%s\n' "$travel_value"
 printf 'DEEPSEEK_API_KEY=%s\n' "$deepseek_value"
 printf 'ALLOWED_TELEGRAM_IDS=%s\n' "$allowed_value"
} > "$work_dir/runtime.conf"
unset telegram_value travel_value deepseek_value

# Keep a stopped previous container until the new one is healthy, for rollback.
backup="mrkai-flights-previous-$(date +%s)"
has_backup=0
if docker container inspect mrkai-flights >/dev/null 2>&1; then
 docker stop -t 30 mrkai-flights >/dev/null
 docker rename mrkai-flights "$backup"
 has_backup=1
fi
rollback() {
 docker rm -f mrkai-flights >/dev/null 2>&1 || true
 if [[ $has_backup == 1 ]]; then docker rename "$backup" mrkai-flights; docker start mrkai-flights >/dev/null; fi
 fail 'новый бот не стал готов. Предыдущий контейнер восстановлен, если существовал. Диагностика: docker logs --tail 50 mrkai-flights'
}
echo 'Запускаю бота и проверяю доступ к Telegram и Aviasales…'
docker run -d --name mrkai-flights --restart unless-stopped \
 --env-file "$work_dir/runtime.conf" \
 --mount type=volume,source=mrkai-flights-data,target=/data \
 --read-only --tmpfs /tmp:rw,noexec,nosuid,size=16m \
 --cap-drop ALL --security-opt no-new-privileges \
 --memory 256m --cpus 1 --pids-limit 64 \
 --log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
 "$image" >/dev/null || rollback
for (( attempt=0; attempt<60; attempt++ )); do
 status=$(docker inspect --format '{{.State.Health.Status}}' mrkai-flights)
 if [[ $status == healthy ]]; then
  if [[ $has_backup == 1 ]]; then docker rm "$backup" >/dev/null; fi
  echo 'Готово: бот запущен, Telegram и Aviasales доступны. Отправьте вашему боту /start.'
  echo 'DeepSeek проверяется при первом запросе пользователя, платных вызовов при установке нет.'
  echo 'Логи: docker logs --tail 50 -f mrkai-flights'
  echo 'Остановка: docker stop mrkai-flights'
  exit 0
 fi
 [[ $status != unhealthy ]] || rollback
 sleep 3
done
rollback
