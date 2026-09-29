# День 22 — первый RAG-запрос

MrKai умеет отвечать в двух режимах. Обычный режим отправляет вопрос модели без локального поиска. RAG-режим ищет до трёх ближайших фрагментов в индексе `structure`, добавляет их к вопросу и просит модель указать источники. Команда сравнения делает два независимых запроса к одной модели с одним вопросом.

Для эмбеддингов d22 вызывает Ollama: она превращает чанки и вопрос в векторы одной модели, затем агент сравнивает их косинусной близостью. Поэтому поиск понимает смысл лучше, чем учебные частоты слов d21. Имя модели сохраняется в индексе: после её смены индекс нужно перестроить.

## Установка и запуск

Нужен Go 1.21+. В каталоге `aiac9/d22`:

```sh
export PATH="$PWD:$PATH"
mrkai
```

`mrkai` открывает терминальный чат. При старте он сообщает, задан ли `DEEPSEEK_API_KEY`, доступна ли локальная команда `ollama` и задан ли `OLLAMA_API_KEY` для Ollama Cloud; значения ключей не выводятся. При отсутствии зависимости выводятся ссылки на [ключ DeepSeek](https://platform.deepseek.com/api_keys), [установку Ollama](https://ollama.com/download) или [ключ Ollama Cloud](https://ollama.com/settings/keys). Планирование и RAG по умолчанию выключены. Для ответов модели задайте `DEEPSEEK_API_KEY` в окружении. Для индексации и RAG нужен Ollama с загруженной моделью `embeddinggemma`: по умолчанию используется локальный адрес `http://localhost:11434/api/embed`. Для Ollama Cloud задайте `OLLAMA_EMBED_URL=https://ollama.com/api/embed` и `OLLAMA_API_KEY`; ключ передаётся только в заголовке `Authorization`. `OLLAMA_EMBED_MODEL` меняет модель. После смены модели выполните `/index build` заново. `DEEPSEEK_BASE_URL` и `DEEPSEEK_MODEL` меняют адрес и модель чата. Данные сохраняются в `DATA_DIR` (по умолчанию `./data`). При необходимости доступны `KNOWLEDGE_DIR`, `GITHUB_TOKEN`, `GITLAB_API_TOKEN`, `GITLAB_API_URL`, `AGENT_SYSTEM_PROMPT`, `INVARIANTS_FILE`. Ключи храните только в переменных окружения.

## RAG

Сначала постройте индекс по базе, затем включите RAG:

```text
/index build knowledge
/rag on
Как включить режим планирования?
/rag compare Как включить режим планирования?
/rag off
```

`/rag status` показывает текущий режим. В RAG-режиме обычный вопрос передаётся агенту вместе с найденными чанками. `/rag compare ВОПРОС` открывает экран сравнения: слева ответ без RAG, справа — ответ с RAG, ниже показаны вопрос и найденные источники. Команда делает два запроса к API независимо от текущего режима и может расходовать средства на счёте провайдера. При отсутствии индекса сначала выполните `/index build knowledge` или `/index build КАТАЛОГ`.

Десять контрольных вопросов с ожиданиями и источниками находятся в `testdata/control-questions.json`. Они относятся к учебной базе `knowledge/rag-facts.md`.

Для демонстрации на внешнем корпусе используйте небольшой публичный набор внутренних документов `MysticalMachines/rag-explained`: скачайте его в `data/rag-explained`, выполните `/index build data/rag-explained`, затем сравните ответы на вопросы об отпуске и возмещении расходов. После индексации `/index status` должен показать README и четыре текстовых документа корпуса.

## Команды

- `/rag on`, `/rag off`, `/rag status`, `/rag compare ВОПРОС` — режим и сравнение.
- `/index build [КАТАЛОГ]`, `/index github ТЕМА`, `/index status`, `/index search fixed|structure ЗАПРОС`, `/index compare ЗАПРОС` — индексация и поиск.
- `/mcp`, `/mcp list`, `/mcp mrkgitlab tools|mrs|call`, `/mcp mrkscheduler tools|add|mrs|list|summary|cancel`, `/mcp mrkpipeline tools|run|call` — MCP-инструменты.
- `/plan-mode enable|disable`, `/state`, `/pause`, `/resume`, `/reset` — задачи; `/reset` также удаляет построенные индексы, но сохраняет исходные документы.
- `/invariants`, `/invariant add|remove|clear` — правила агента.
- `/info`, `/help`, `/exit`, `/quit` — справка и выход.

Все команды и подкоманды доступны через автодополнение. Ctrl+C или Esc останавливает текущую операцию, двойное нажатие завершает `mrkai`.
