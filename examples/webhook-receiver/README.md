# Webhook receiver

A small receiver of Pandokos events, written with the standard library only. It shows what a
receiver has to do and is used by the end-to-end tests of the event destinations.

```sh
export WEBHOOK_SECRETS='whsec_...'          # the secret printed by "event-destination create"
go run ./examples/webhook-receiver -addr 127.0.0.1:8090 -store events-seen.txt
pandokos event-destination create --tenant acme --url https://receiver.example.com/ billing
```

The secret comes from an environment variable, never from an argument (an argument is visible to
every user of the machine) and the receiver never prints it. During a rotation, put the new secret
first and the previous one after it, separated by a space: a delivery is accepted if it is signed
with either.

## The three checks

Every delivery carries the headers `webhook-id`, `webhook-timestamp` and `webhook-signature`
(Standard Webhooks). The handler refuses with `401` unless:

1. all three are present and the timestamp is a number;
2. the timestamp is within five minutes of the clock, either way, which limits the replay of a
   captured request;
3. one of the entries `v1,<base64>` of the signature is the HMAC-SHA256, with a secret, of
   `webhook-id`, `.`, `webhook-timestamp`, `.` and the body exactly as received. The comparison is
   in constant time.

A request that passes is a document from the sender. One that is not a CloudEvents document with
the ID of the header is `400`; a store that fails is `500`, so that the sender tries again.

## Why the ID is the key

Delivery is at least once: a response that is lost, a timeout or a restart make the sender send the
same event again, with the same ID. The receiver stores the ID of each event it has handled and
answers `204` to a repeat without handling it again. The ID is the key because it does not change
between attempts, and neither does anything else a receiver could use: the timestamp does. A
receiver that does its work first and records the ID afterwards must make the work idempotent, or
record both in one transaction.
