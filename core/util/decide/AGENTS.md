# decide

Structured questions to a decision provider: yes/no, pick one, place on a scale, answered with probabilities. `Decider` is the seam; `Client` speaks a decision gateway's `POST /ai/decide`.

It is not a chat client, a model router or a budget. A change that makes it generate text, pick a provider, or keep spend is the wrong change; the gateway does those.

## Start Here

- `decide.go` — `Question`, `Request`, `Answer`, `Response`, `Decider`, `Scripted`, validation.
- `client.go` — `Client`: the dialects, answer checks, retries, error envelope. `jev.go` — JEV's System-1 body and answers.
- `confidence.go` — concentration statistics (not correctness). `scrub.go` — `Scrub`.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- Standard library only.
- An invalid request (no purpose, no question, a malformed question, a repeated key) is refused before any call. Guarded by `TestClientRefusesBeforeCalling`.
- Every answer is checked against its question: present, the asked type, an offered option, probabilities in 0..1. Guarded by `TestClientRejectsAnswersThatDoNotFit`.
- Only 429, 503 and 504 are retried, never an auth failure, an open breaker or an unavailable provider; the token never appears in an error. Guarded by `TestClientRetriesOnlyWhatCanSucceed`.
- The JEV body keeps question, option and level order byte for byte, and needs a model. Guarded by `TestJEVDialectWritesTheNativeBodyInOrder`.
