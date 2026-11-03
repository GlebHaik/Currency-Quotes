# Currency Quotes

Сервис котировок валютных пар с асинхронным обновлением. Клиент запрашивает обновление котировки, получает его ID и позже забирает результат. Само обновление выполняется в фоне, не в HTTP-обработчике.

**Стек:** Go 1.26, PostgreSQL 16, chi, pgx v5, golang-migrate, golang.org/x/time/rate, gomock, Docker. Источник цен — [Frankfurter API](https://frankfurter.dev) (без ключа).

## Быстрый старт

```bash
docker compose up --build
```

Поднимаются Postgres и сервис на `http://localhost:8080`, миграции применяются при старте. Если порт занят: `APP_PORT=8085 docker compose up --build`.

## API

Спецификация: [`api/openapi.yaml`](api/openapi.yaml).

| Метод | Путь | Что делает | Ответ |
|---|---|---|---|
| `POST` | `/api/v1/quotes/updates` | Запросить обновление пары | `202` + `updateId` |
| `GET` | `/api/v1/quotes/updates/{updateId}` | Результат обновления по ID | `200`: статус, цена, время обновления |
| `GET` | `/api/v1/quotes/latest?currencyPair=EUR/MXN` | Последняя известная котировка | `200`: цена, время обновления |

```bash
# 1. Запросить обновление (Idempotency-Key необязателен)
curl -X POST localhost:8080/api/v1/quotes/updates \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"currencyPair":"EUR/MXN"}'
# {"status":"pending","updateId":"622c4e90-..."}

# 2. Через какое-то время — результат
curl localhost:8080/api/v1/quotes/updates/622c4e90-...
# {"currencyPair":"EUR/MXN","status":"completed","price":"20.956000",
#  "updatedAt":"2026-11-03T10:55:28Z","sourceDate":"2026-11-03",...}

# 3. Последняя котировка
curl 'localhost:8080/api/v1/quotes/latest?currencyPair=EUR%2FMXN'
```

## Архитектура

```
HTTP ─▶ handler ─▶ QuoteService ─▶ Postgres (строка pending) ─▶ Notify
                                                                  │
background:  dispatcher ─(claim)─▶ канал ─▶ workers ─▶ QuoteUpdater.Process
                                                         ├─▶ Frankfurter (rate limit)
                                                         └─▶ Postgres (completed / failed / retry)
```

```
cmd/server/                 точка входа: флаги, сигналы, конфиг
internal/app/               сборка зависимостей, HTTP-сервер, фоновая обработка (dispatcher, workers)
internal/handler/           HTTP: разбор запросов, коды ответов
internal/service/           бизнес-логика: QuoteService (API), QuoteUpdater (фоновое обновление)
internal/domain/            модели, CurrencyPair, доменные ошибки
internal/repository/        Postgres
internal/clients/frankfurter/  клиент внешнего API
internal/config/            конфигурация (YAML + env)
migrations/                 схема БД
```

## Команды

```bash
make            # список всех команд
make up         # только Postgres в Docker (localhost:5442)
make run        # сервис локально на :8085
make check      # gofmt + goimports + vet + golangci-lint + тесты с -race
make docker-up  # сервис и Postgres в Docker
make updates    # последние 10 обновлений из БД
```

## Тесты

| Пакет | Что проверяется | Как |
|---|---|---|
| `service` | создание обновлений, идемпотентность, claim, обработка: успех / ошибка / повтор / `Retry-After` / остановка, backoff | табличные тесты на моках gomock |
| `handler` | коды ответов и JSON всех эндпоинтов, разбор ключа | `httptest` + мок сервиса |
| `app` | фоновая обработка: параллельность, паника и ошибка не останавливают воркер, остановка, Notify, лимит claim; таймаут запросов | фейки + `-race` |
| `clients/frankfurter` | разбор ответов, 422/429/5xx, общий лимит RPS | `httptest` |
| `domain` | разбор `CurrencyPair` | табличный тест |

## Архитектурные решения и trade-off

- **Postgres как очередь.** Задачи забираются через `FOR UPDATE SKIP LOCKED` с арендой `locked_until`. Брокер не нужен, проблемы dual write нет, задачи переживают рестарт, а воркеры и реплики не берут одну задачу дважды. Цена: аренда отсчитывается с момента claim, включая ожидание в канале и лимитере. Если обработка дольше `lock_ttl`, задачу возьмёт второй воркер, и старый может перезаписать статус. Решение — fencing token (`lease_id` в финальных `UPDATE`) или heartbeat.
- **`Notify` + опрос по таймеру.** Обработка стартует сразу после POST, таймер страхует. Цена: `Notify` будит только свой процесс, для нескольких реплик нужен `LISTEN/NOTIFY`.
- **Склейка по паре и общий rate limiter.** На пару уходит один запрос к провайдеру, RPS не превышается. Цена: лимит считается на процесс, а не на кластер.
- **Повторы** с экспоненциальной задержкой, jitter и `Retry-After`, не больше `max_attempts`. Ошибки 4xx не повторяются. Цена: `attempts` растёт при claim, поэтому рестарты тратят попытки, а задачу, на которой воркер паникует, подхватывают бесконечно. Нужен фильтр `attempts < max` в claim, а при большой очереди — колонка `available_at` с индексом.
- **Идемпотентность** через `Idempotency-Key`, уникальный индекс и `ON CONFLICT`. Повторы POST безопасны, гонку решает БД. Цена: ключи глобальные и хранятся вечно. Нужны TTL и привязка к ID клиента.
- **Две таблицы.** `quote_updates` — журнал и очередь, `quotes` — текущие цены. `/latest` ищет по первичному ключу, запись идёт в одной транзакции, старый результат не затирает свежий. Цена: журнал растёт, нужна очистка или партиционирование по `created_at`. Время берётся из часов приложения, при нескольких репликах лучше `now()` из БД.
- **Graceful shutdown и таймаут запросов.** Текущие запросы и пачки дорабатываются, зависшая БД даёт 504, а не висящее соединение.
