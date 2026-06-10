# Notifications

Rollops notifies operators about rollout events that need attention or
record an outcome. Delivery is **best-effort**: a flaky or unreachable
notifier never blocks or fails a rollout — delivery errors are dropped and
the rollout proceeds. Verify a channel at setup time instead: `rollops
doctor` sends a test event to every configured channel and fails loudly on
a bad token, chat id, or webhook URL.

## Events

| Kind | When |
|---|---|
| `approval_needed` | A rollout reached an approval gate and waits for an operator. |
| `failed` | A rollout failed (deploy, verify, or gate error). |
| `rolled_back` | Auto-rollback or manual rollback completed. |
| `promoted` | A rollout was verified and promoted. |

Each event carries the target ref, the rollout id, and an optional detail
string (for example the failure reason).

## Telegram

Set both variables in the daemon environment:

```sh
ROLLOPS_TELEGRAM_TOKEN=<bot token from @BotFather>
ROLLOPS_TELEGRAM_CHAT=<chat id>
```

The daemon sends a one-line message per event via the Telegram Bot API
(`sendMessage`), for example:

```
⏳ Rollops: prod/web needs approval (ro-01J...)
❌ Rollops: prod/web failed (ro-01J...) — health gate: 2/3 checks failing
```

To find the chat id, add the bot to the chat and call
`https://api.telegram.org/bot<token>/getUpdates`.

## Generic webhook

```sh
ROLLOPS_WEBHOOK_URL=https://example.com/hooks/rollops
ROLLOPS_WEBHOOK_SECRET=<optional HMAC secret>
```

The daemon POSTs the event as JSON:

```json
{"kind":"failed","target_ref":"prod/web","rollout_id":"ro-01J...","detail":"..."}
```

When `ROLLOPS_WEBHOOK_SECRET` is set, each request includes an
`X-Rollops-Signature: sha256=<hex>` header — the HMAC-SHA256 of the raw
body keyed with the secret. Verify it before trusting the payload.

## Verifying a channel

With the variables exported, run:

```sh
rollops doctor
```

Doctor sends a `test` event to every configured channel and reports
`notify: ok (telegram, webhook)` or the delivery error. Expect one test
message in the chat / one POST to the webhook per run.

## Multiple channels

Telegram and the webhook can be enabled together; every event fans out to
all configured channels. With neither configured, notifications are off
(no-op) and doctor reports `notify: skipped`.

Systemd deployments: both variable pairs are listed in
`deploy/systemd/rollopsd.env.example`.
