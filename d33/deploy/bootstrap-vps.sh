#!/usr/bin/env bash
# Run on the VPS; never executes downloaded scripts or writes keys into files.
set +x
set -Eeuo pipefail

DEPLOY_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_APP_DIR="$(cd -- "$DEPLOY_SCRIPT_DIR/.." && pwd)"
DEPLOY_PROJECT=garage33
DEPLOY_TARGET=''
DEPLOY_HTTPS=0
DEPLOY_CHECK_ONLY=0
DEPLOY_STAGE='проверка аргументов'

usage() {
    cat <<'HELP'
Развёртывание Гараж 33 на VPS.

sudo bash deploy/bootstrap-vps.sh
sudo bash deploy/bootstrap-vps.sh --address PUBLIC_IP
sudo bash deploy/bootstrap-vps.sh --address garage.example.com --https

По умолчанию: публичный HTTP, без домена и TLS.
--address HOST   Публичный IPv4 VPS или домен; без аргумента запросится в терминале.
--https          HTTPS; для IPv4 создаётся имя IP.sslip.io, для домена используется он.
--project NAME   Имя экземпляра Compose, по умолчанию garage33.
--check          Только проверка ОС, ресурсов и исходников; без установки и запуска.
--help           Эта подсказка.

Свежий VPS: Ubuntu Server 24.04 / 26.04 LTS, amd64 или arm64.
Также работает с уже установленными Docker, Buildx и Compose >= 2.24 на Linux.
Нужны публичный IPv4 или домен и доступный порт 80; для HTTPS также 443.
API-ключ и пароль CRM запрашиваются скрыто. Можно задать DEEPSEEK_API_KEY и
CRM_ADMIN_PASSWORD в окружении. MRKAI_SERVICE_KEY генерируется автоматически.
Повторный запуск сохраняет volumes; ключ и пароль нужно ввести снова.
HELP
}

fail() { printf 'Ошибка: %s\n' "$1" >&2; exit 1; }
log() { printf '\n→ %s\n' "$1"; }
trap 'printf "Ошибка на этапе: %s. Секретные значения не выводятся.\n" "$DEPLOY_STAGE" >&2' ERR

while (($#)); do
    case "$1" in
        --help|-h) usage; exit 0 ;;
        --address) (($# >= 2)) || fail 'после --address нужен IP или домен'; DEPLOY_TARGET="$2"; shift 2 ;;
        --project) (($# >= 2)) || fail 'после --project нужно имя'; DEPLOY_PROJECT="$2"; shift 2 ;;
        --https) DEPLOY_HTTPS=1; shift ;;
        --check) DEPLOY_CHECK_ONLY=1; shift ;;
        *) fail 'неизвестный аргумент; см. --help' ;;
    esac
done
[[ "$DEPLOY_PROJECT" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'имя проекта: строчные латинские буквы, цифры, дефис или подчёркивание'
[[ "$(uname -s)" == Linux ]] || fail 'скрипт запускается на Linux VPS; на macOS установка запрещена'
case "$(uname -m)" in x86_64|aarch64) ;; *) fail 'нужна архитектура amd64 или arm64' ;; esac
[[ -r "$DEPLOY_APP_DIR/compose.yaml" && -r "$DEPLOY_APP_DIR/Dockerfile" && -r "$DEPLOY_APP_DIR/../terminal/go.mod" ]] || fail 'загрузите каталог aiac9 целиком: d33 и terminal должны находиться рядом'
[[ -r /etc/os-release ]] || fail 'не удалось определить ОС'
# This is the operating system's root-owned release metadata, not an application config.
. /etc/os-release
DEPLOY_OS_ID="${ID:-}"
DEPLOY_OS_VERSION="${VERSION_ID:-}"
DEPLOY_OS_SUPPORTED=0
if [[ "$DEPLOY_OS_ID" == ubuntu && ( "$DEPLOY_OS_VERSION" == 24.04 || "$DEPLOY_OS_VERSION" == 26.04 ) ]]; then DEPLOY_OS_SUPPORTED=1; fi

DEPLOY_STAGE='проверка ресурсов'
DEPLOY_RAM_MB="$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)"
DEPLOY_DISK_MB="$(df -Pk "$DEPLOY_APP_DIR" | awk 'NR==2 {print int($4/1024)}')"
[[ "$DEPLOY_RAM_MB" =~ ^[0-9]+$ && "$DEPLOY_DISK_MB" =~ ^[0-9]+$ ]] || fail 'не удалось проверить ресурсы'
((DEPLOY_RAM_MB >= 1800)) || fail 'для сборки нужно минимум 2 ГБ RAM; рекомендуется 4 ГБ'
((DEPLOY_DISK_MB >= 10240)) || fail 'нужно минимум 10 ГБ свободного места для образов и сборки'
printf 'ОС: %s %s; RAM: %s МБ; свободно на диске: %s МБ.\n' "$DEPLOY_OS_ID" "$DEPLOY_OS_VERSION" "$DEPLOY_RAM_MB" "$DEPLOY_DISK_MB"

if ((DEPLOY_CHECK_ONLY)); then
    if command -v docker >/dev/null 2>&1; then
        printf 'Docker CLI найден.\n'
    elif ((DEPLOY_OS_SUPPORTED)); then
        printf 'Docker будет установлен из репозиториев Ubuntu.\n'
    else
        fail 'автоустановка поддерживает Ubuntu 24.04/26.04; на этой ОС установите Docker заранее'
    fi
    printf 'Проверка завершена. Установки и запуск не выполнялись.\n'
    exit 0
fi
((EUID == 0)) || fail 'запустите на VPS через sudo bash deploy/bootstrap-vps.sh'

valid_address() {
    local address="$1" label octet first second third
    local -a pieces
    if [[ "$address" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        IFS=. read -r -a pieces <<< "$address"
        for octet in "${pieces[@]}"; do
            [[ "$octet" == 0 || "$octet" =~ ^[1-9][0-9]{0,2}$ ]] || return 1
            ((10#$octet <= 255)) || return 1
        done
        first="${pieces[0]}"; second="${pieces[1]}"; third="${pieces[2]}"
        ((first > 0 && first < 224 && first != 10 && first != 127)) || return 1
        ((first != 172 || second < 16 || second > 31)) || return 1
        ((first != 192 || second != 168)) || return 1
        ((first != 169 || second != 254)) || return 1
        ((first != 100 || second < 64 || second > 127)) || return 1
        ((first != 198 || second < 18 || second > 19)) || return 1
        ((first != 192 || second != 0 || third != 2)) || return 1
        ((first != 198 || second != 51 || third != 100)) || return 1
        ((first != 203 || second != 0 || third != 113)) || return 1
        return 0
    fi
    [[ ${#address} -le 253 && "$address" == *.* ]] || return 1
    case "$address" in *.local|*.internal|*.intranet|*.corp|*.lan|*.svc|*.priv|*.vpn) return 1 ;; esac
    IFS=. read -r -a pieces <<< "$address"
    for label in "${pieces[@]}"; do
        [[ ${#label} -ge 1 && ${#label} -le 63 && "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
    done
    [[ "$address" != *. && "$address" != *..* ]] || return 1
    return 0
}

DEPLOY_STAGE='настройка публичного адреса'
if [[ -z "$DEPLOY_TARGET" ]]; then
    [[ -t 0 ]] || fail 'в неинтерактивном запуске передайте --address с публичным IP или доменом'
    read -r -p 'Публичный IPv4 VPS (из панели хостинга) или домен: ' DEPLOY_TARGET
fi
# DNS names are case-insensitive; do not accept URLs, credentials or shell fragments.
DEPLOY_TARGET="$(printf '%s' "$DEPLOY_TARGET" | tr '[:upper:]' '[:lower:]')"
valid_address "$DEPLOY_TARGET" || fail 'нужен публичный IPv4 или корректный домен без http://, порта и пути'
DEPLOY_PUBLIC_HOST="$DEPLOY_TARGET"
if ((DEPLOY_HTTPS)) && [[ "$DEPLOY_TARGET" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then DEPLOY_PUBLIC_HOST="$DEPLOY_TARGET.sslip.io"; fi
if ((DEPLOY_HTTPS)); then
    SITE_DOMAIN="$DEPLOY_PUBLIC_HOST"
    DEPLOY_PUBLIC_URL="https://$DEPLOY_PUBLIC_HOST"
else
    SITE_DOMAIN="http://$DEPLOY_PUBLIC_HOST"
    DEPLOY_PUBLIC_URL="http://$DEPLOY_PUBLIC_HOST"
fi
export SITE_DOMAIN

read_secret() {
    local variable="$1" minimum="$2" prompt="$3" value
    [[ -t 0 ]] || fail 'задайте DEEPSEEK_API_KEY и CRM_ADMIN_PASSWORD в окружении либо запустите интерактивно'
    read -r -s -p "$prompt" value
    printf '\n'
    ((${#value} >= minimum)) || fail 'введённое значение слишком короткое'
    printf -v "$variable" '%s' "$value"
}
DEPLOY_STAGE='настройка ключей и пароля'
if [[ -z "${DEEPSEEK_API_KEY:-}" ]]; then read_secret DEEPSEEK_API_KEY 8 'API-ключ DeepSeek: '; fi
if [[ -z "${CRM_ADMIN_PASSWORD:-}" ]]; then
    read_secret CRM_ADMIN_PASSWORD 12 'Пароль CRM (от 12 символов): '
    read_secret DEPLOY_PASSWORD_REPEAT 12 'Повторите пароль CRM: '
    [[ "$CRM_ADMIN_PASSWORD" == "$DEPLOY_PASSWORD_REPEAT" ]] || fail 'пароли не совпадают'
    unset DEPLOY_PASSWORD_REPEAT
fi
[[ ${#CRM_ADMIN_PASSWORD} -ge 12 ]] || fail 'CRM_ADMIN_PASSWORD должен содержать минимум 12 символов'
if [[ -z "${MRKAI_SERVICE_KEY:-}" ]]; then MRKAI_SERVICE_KEY="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"; fi
[[ ${#MRKAI_SERVICE_KEY} -ge 16 ]] || fail 'MRKAI_SERVICE_KEY должен содержать минимум 16 символов'
export DEEPSEEK_API_KEY CRM_ADMIN_PASSWORD MRKAI_SERVICE_KEY

DEPLOY_STAGE='подготовка Docker'
if ! command -v docker >/dev/null 2>&1; then
    ((DEPLOY_OS_SUPPORTED)) || fail 'автоустановка поддерживает Ubuntu 24.04/26.04; на этой ОС установите Docker заранее'
    log 'Устанавливаю Docker, Compose и Buildx из репозиториев Ubuntu'
    apt-get update
    if ! apt-cache show docker-compose-v2 >/dev/null 2>&1; then
        apt-get install -y software-properties-common
        add-apt-repository --yes universe
        apt-get update
    fi
    apt-get install -y docker.io docker-compose-v2 docker-buildx ca-certificates
fi
command -v systemctl >/dev/null 2>&1 || fail 'нужен VPS с systemd'
systemctl enable --now docker

docker_local() { docker --host unix:///var/run/docker.sock "$@"; }
if ! docker_local compose version --short >/dev/null 2>&1 || ! docker_local buildx version >/dev/null 2>&1; then
    ((DEPLOY_OS_SUPPORTED)) || fail 'нужны Docker Compose >= 2.24 и Buildx'
    if dpkg-query -W -f='${Status}' docker-ce 2>/dev/null | grep -q 'install ok installed'; then
        apt-get update
        apt-get install -y docker-compose-plugin docker-buildx-plugin
    else
        apt-get update
        apt-get install -y docker-compose-v2 docker-buildx
    fi
fi
DEPLOY_COMPOSE_VERSION="$(docker_local compose version --short)"
DEPLOY_COMPOSE_VERSION="${DEPLOY_COMPOSE_VERSION#v}"
IFS=. read -r DEPLOY_COMPOSE_MAJOR DEPLOY_COMPOSE_MINOR _ <<< "$DEPLOY_COMPOSE_VERSION"
[[ "$DEPLOY_COMPOSE_MAJOR" =~ ^[0-9]+$ && "$DEPLOY_COMPOSE_MINOR" =~ ^[0-9]+$ ]] || fail 'не удалось определить версию Compose'
((DEPLOY_COMPOSE_MAJOR > 2 || (DEPLOY_COMPOSE_MAJOR == 2 && DEPLOY_COMPOSE_MINOR >= 24))) || fail 'обновите Compose до 2.24 или новее'
docker_local version --format '{{.Server.Version}}' >/dev/null

cd -- "$DEPLOY_APP_DIR"
compose_app() { docker_local compose --env-file /dev/null -f "$DEPLOY_APP_DIR/compose.yaml" -p "$DEPLOY_PROJECT" --profile public "$@"; }
DEPLOY_STAGE='валидация конфигурации'
compose_app config --quiet
DEPLOY_STAGE='сборка образов'
log 'Собираю приложения; Go-тесты выполняются внутри образа'
compose_app build
DEPLOY_STAGE='запуск сервисов'
log 'Запускаю сайт, CRM, поддержку и HTTP-прокси'
compose_app up -d --wait --wait-timeout 180

DEPLOY_STAGE='проверка публичного сайта'
log 'Проверяю публичный сайт, чат на странице и защищённый маршрут CRM'
DEPLOY_READY=0
for ((DEPLOY_ATTEMPT=1; DEPLOY_ATTEMPT<=24; DEPLOY_ATTEMPT++)); do
    if compose_app exec -T crm /app/mrkai --deployment-check "$DEPLOY_PUBLIC_URL" >/dev/null 2>&1; then DEPLOY_READY=1; break; fi
    printf "Ожидаю публичный сайт, попытка %d/24…\n" "$DEPLOY_ATTEMPT"
    sleep 5
done
if (( ! DEPLOY_READY )); then
    printf '\nКонтейнеры запущены, но публичный адрес пока не прошёл проверку.\n' >&2
    printf 'Проверьте публичный IP/DNS и входящий порт 80 в панели VPS' >&2
    if ((DEPLOY_HTTPS)); then printf ', также 443 и выпуск TLS-сертификата' >&2; fi
    printf '. Затем повторите запуск скрипта.\n' >&2
    exit 1
fi

printf '\nГотово — публичный сайт проверен.\n'
printf 'Сайт для записи видео: %s/\n' "$DEPLOY_PUBLIC_URL"
printf 'CRM: %s/crm\n' "$DEPLOY_PUBLIC_URL"
printf 'Логин CRM: admin; пароль — введённый вами.\n'
printf 'Чат: кнопка «Поддержка» на сайте. Задачи и данные сохраняются при перезапуске VPS.\n'
printf 'Для видео: MANN W 914/2 — есть; Brembo P 85 020 — нет.\n'
printf 'Телефон …01 — ограничение в день создания БД; …02 — создание TODO на проверку.\n'
