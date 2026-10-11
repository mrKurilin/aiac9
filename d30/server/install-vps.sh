#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "Запустите скрипт от root." >&2
    exit 1
fi

if [ ! -r /etc/os-release ]; then
    echo "Не удалось определить дистрибутив Linux." >&2
    exit 1
fi
. /etc/os-release
case "$ID" in
    debian|ubuntu) ;;
    *) echo "Поддерживаются Ubuntu и Debian с systemd." >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|aarch64) ;;
    *) echo "Поддерживаются архитектуры x86_64 и aarch64." >&2; exit 1 ;;
esac

command -v systemctl >/dev/null 2>&1 || {
    echo "Для постоянного запуска требуется systemd." >&2
    exit 1
}

service_bind=${SERVICE_BIND:-0.0.0.0:8780}
service_token=${SERVICE_API_TOKEN:-}
model=${OLLAMA_MODEL:-qwen3:4b-instruct}
if [ "${#service_token}" -lt 16 ]; then
    echo "Задайте SERVICE_API_TOKEN длиной от 16 символов перед установкой." >&2
    exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/../.." && pwd)
if [ ! -f "$repo_dir/week6/go.mod" ] || [ ! -f "$repo_dir/terminal/go.mod" ]; then
    echo "Запустите скрипт из полного репозитория aiac9, а не из отдельной папки server." >&2
    exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
fi
systemctl enable --now docker

docker build -f "$script_dir/Dockerfile" -t aiac9-d30:local "$repo_dir"
docker volume create aiac9-d30-ollama >/dev/null

if docker container inspect aiac9-d30-ollama >/dev/null 2>&1; then
    docker start aiac9-d30-ollama >/dev/null || true
else
    docker run -d --name aiac9-d30-ollama --restart unless-stopped \
        -p 127.0.0.1:11434:11434 \
        -v aiac9-d30-ollama:/root/.ollama \
        ollama/ollama:latest >/dev/null
fi

ready=0
attempt=0
while [ "$attempt" -lt 30 ]; do
    if docker exec aiac9-d30-ollama ollama list >/dev/null 2>&1; then
        ready=1
        break
    fi
    attempt=$((attempt + 1))
    sleep 2
done
if [ "$ready" -ne 1 ]; then
    echo "Ollama не запустилась. Проверьте: docker logs aiac9-d30-ollama" >&2
    exit 1
fi
docker exec aiac9-d30-ollama ollama pull "$model"

if docker container inspect aiac9-d30-api >/dev/null 2>&1; then
    docker stop aiac9-d30-api >/dev/null
    docker container rm aiac9-d30-api >/dev/null
fi
export SERVICE_BIND="$service_bind" SERVICE_API_TOKEN="$service_token" OLLAMA_MODEL="$model"
docker run -d --name aiac9-d30-api --restart unless-stopped --network host \
    --log-driver json-file --log-opt max-size=10m --log-opt max-file=3 \
    --env SERVICE_BIND --env SERVICE_API_TOKEN --env OLLAMA_MODEL \
    aiac9-d30:local >/dev/null

sleep 2
if [ "$(docker inspect --format '{{.State.Running}}' aiac9-d30-api)" != "true" ]; then
    echo "HTTP-сервис не запустился. Проверьте: docker logs aiac9-d30-api" >&2
    exit 1
fi
echo "Готово: HTTP API слушает $service_bind. Откройте входящий TCP-порт 8780 в панели VPS."
echo "Публичный адрес клиента: http://PUBLIC_IP:8780 (замените PUBLIC_IP на адрес VPS)."
echo "Контейнеры перезапускаются после reboot."
