# Notification System API

Created an event driven notification system that accepts the notification request through REST API, stores the notifications in PostgreSQL, and deliver them asyncgronously through SMS, email and push channels with retries, priorities and per-channel rate limiting.

## Quick Start

Requirements: Docker with Compose

```bash
docker compose up --build
```

this starts the db, applies the migrations, starts a mock provider, the api, the delivery worker and a Swagger UI

Send a notification:

```bash
curl -s -X POST localhost:8080/notifications \
  -H "Content-Type: application/json" \
  -d '{"recipient":"+905551234567","channel":"sms","content":"Hello","priority":"high"}'
```

By default the worker delivers to the bundled mock provider. To use your own webhook.site URL:

```bash
WEBHOOK_URL="https://webhook.site/<your-uuid>" docker compose up --build
```

## Architecture

```mermaid
flowchart LR
    Client -->|REST| API
    API -->|insert pending| DB[(PostgreSQL)]
    Worker -->|claim with SKIP LOCKED| DB
    Worker -->|POST per channel, rate limited| Provider[Provider / webhook.site]
    Worker -->|sent / retry / failed| DB
```

- **API** (`cmd/api`): validets and stores the requests, serves status, listening, cancel, batch and metrics.
- **Worker**(`cmd/worker`): Claims due notifications, sends them through a worker pool, and records the results
- **PostgreSQL**: source of truth and the queue.
- **Mock provider** (`cmd/mockprovider`): a stand-in for the external provider, used for demos

## Design choices

- **PosrgreSQL** as the queue: Notifications are stored in PostgreSQL and same table doubles as the queue. Saving a notification and enqueueing it is one atomic insert, so two never can diverge .

- **Lease**: Claimed notification gets a 30s lease. If a worker crashes, lease expires and another worker retries it

- **Retry Policy**: Network errors, timeouts, code 5xx/408/429 are retried others not. Because the same request would failagain. Waits grow exponentially

- **Rate limiting**: Each channel has its own token-bucket limiter at 100 messages per second. So a burst on one channel cannot starve others

- **Idempotency.**: Uniqueness of the `Idempotency-Key` is enforced by a unique index with `ON CONFLICT DO NOTHING`, not by a check in code, because two simultaneous requests could both pass a "look up, then insert" check. The repeated request returns the original notification.

- **Priority**: The prioritiy of the notificaions are stored as number (0->High, 1->normal, 2->low) so sorting gives the right order

- **Observability.** A correlation ID is logged by the API, stored with the notification and logged again by the worker, so one request can be traced end to end. Metrics are computed from the database because the API and worker are separate processes.

## API

The full specification is in [`openapi.yaml`](openapi.yaml) and served at local ->`http://localhost:8081`.

| Method | Path                         | Description                                                                |
| ------ | ---------------------------- | -------------------------------------------------------------------------- |
| POST   | `/notifications`             | Create a notification (`Idempotency-Key` optional)                         |
| POST   | `/notifications/batch`       | Create up to 1000 notifications atomically                                 |
| GET    | `/notifications/{id}`        | Get status                                                                 |
| POST   | `/notifications/{id}/cancel` | Cancel a pending notification                                              |
| GET    | `/notifications`             | List with `status`, `channel`, `batch_id`, `from`, `to`, `limit`, `offset` |
| GET    | `/metrics`                   | Queue depth, success rate, delivery latency                                |
| GET    | `/health`                    | Liveness and database check                                                |

Examples:

```bash
curl -s -X POST localhost:8080/notifications -H "Content-Type: application/json" \
  -H "Idempotency-Key: order-1001" \
  -d '{"recipient":"+905551234567","channel":"sms","content":"Your order shipped"}'

#Batch
curl -s -X POST localhost:8080/notifications/batch -H "Content-Type: application/json" \
  -d '{"notifications":[{"recipient":"a@b.com","channel":"email","content":"Hi"},{"recipient":"token","channel":"push","content":"Hi"}]}'

# List failed SMS from the last day
curl -s "localhost:8080/notifications?status=failed&channel=sms&from=2026-11-21T00:00:00Z"
```

## Testing

Unit tests run without any dependency:

```bash
go test ./...
```

Integration tests need a db (the compose PostgreSQL works ok). They are skipped when `TEST_DATABASE_URL` is not set:

```bash
docker compose up -d postgres migrate
TEST_DATABASE_URL="postgres://notif:notif@localhost:5432/notifications?sslmode=disable" go test ./... -count=1
```

Please stop the worker while runnig the integration tests, it may pick up the test notifications

## Load test

500 SMS notifications were queued while the worker was stopped, then the worker was started against the mock provider with the 100 per second per channel limit.

```bash
seq 1 500 | xargs -P 20 -I% curl -s -o /dev/null -X POST localhost:8080/notifications \
  -H "Content-Type: application/json" \
  -d '{"recipient":"+905551234567","channel":"sms","content":"load %"}'
```

Notifications delivered per second (`sent_at`):

| Second      | Delivered |
| ----------- | --------- |
| 1 (partial) | 60        |
| 2           | 100       |
| 3           | 100       |
| 4           | 99        |
| 5           | 101       |
| 6 (partial) | 40        |

All 500 were delivered in about 5 seconds and the rate stayed at about 100 per second. The first and last seconds are partial, and counts of 99 and 101 come from `sent_at` being written just after each send. Delivery latency in `/metrics` includes queue wait, so it rises during bursts because of the rate limit.

## Known limitations

- No graceful shutdown: an interrupted job is retried after its 30 second lease expires, so it may be sent twice.
- The rate limit is per worker process; several workers would need a shared limiter (for example Redis).
- Pagination uses `OFFSET` and metrics are computed from the database; both would change at larger scale.
- Batch creation has no idempotency keys, and SMS length is counted in characters (real limits are lower for Turkish letters).
- Not implemented: scheduled notifications, templates, WebSocket updates, distributed tracing.

## AI usage

- In the development Claude used as a pair-programming partner and tutor. It help me to build the design, explained the not well known concept like testing and indempotency. The testing methos has been(also openapi.yaml) fixed and written by ai. I ran every step, fixed my own mistakes along the way. Carefully observed the error codes and integration tests,The ReadMe decisions and limitations reflect what I understand of the system.
