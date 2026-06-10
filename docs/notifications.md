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

## Briefkasten (preferred for mail)

[Briefkasten](https://github.com/klarlabs-studio/briefkasten) is a mailbox
as an MCP server with a durable outbox: `email.send` queues the message,
retries failed sends, and exposes delivery status. That makes it a better
mail channel than direct SMTP here — the engine drops notifier errors by
design, so with briefkasten a transient SMTP failure delays the mail
instead of losing it.

```sh
ROLLOPS_BRIEFKASTEN_URL=http://127.0.0.1:8090
ROLLOPS_BRIEFKASTEN_TO=ops@example.com,oncall@example.com
ROLLOPS_BRIEFKASTEN_TOKEN=<optional bearer token>
```

Rollops calls the `email.send` MCP tool per event; briefkasten's outbox
handles SMTP delivery and retries (`email.send_status` / `email.retry` on
the briefkasten side).

## Email (direct SMTP)

No extra service required. Set the server, sender, and recipients in the
daemon environment:

```sh
ROLLOPS_SMTP_ADDR=smtp.example.com:587
ROLLOPS_SMTP_FROM=rollops@example.com
ROLLOPS_SMTP_TO=ops@example.com,oncall@example.com
ROLLOPS_SMTP_USER=<optional auth user>
ROLLOPS_SMTP_PASS=<optional auth password>
```

The daemon sends one plain-text mail per event. The subject is an
ASCII-safe summary, the body the full message line:

```
Subject: Rollops: prod/web failed

❌ Rollops: prod/web failed (ro-01J...) — health gate: 2/3 checks failing
```

`ROLLOPS_SMTP_TO` takes a comma-separated recipient list. When
`ROLLOPS_SMTP_USER` is set, PLAIN auth is used; delivery upgrades to
STARTTLS when the server supports it.

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
`notify: ok (email, webhook)` or the delivery error. Expect one test mail
/ one POST to the webhook per run.

## Multiple channels

Briefkasten, direct SMTP, and the webhook can be enabled together; every
event fans out to all configured channels. With none configured,
notifications are off (no-op) and doctor reports `notify: skipped`.

Systemd deployments: both variable pairs are listed in
`deploy/systemd/rollopsd.env.example`.
