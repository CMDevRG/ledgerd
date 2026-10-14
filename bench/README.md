# Benchmark

`make bench` load-tests the running Docker Compose stack with [k6](https://k6.io)
and then verifies the whole ledger.

```bash
make up        # start PostgreSQL, run migrations, start the API
make bench     # 20 virtual users per scenario, 20 s each
VUS=40 DURATION=60s make bench        # heavier
SCENARIOS=hot make bench              # only the hot-account scenario
```

## Scenarios

| Scenario | What it does | What it shows |
|---|---|---|
| `spread` | Transfers between 200 disjoint pairs of accounts | Throughput when writers rarely touch the same account |
| `hot` | Every transfer credits **one** account | The ceiling when all writers queue on a single row lock |

A *transfer* is one transaction with two postings, an idempotency key, the full
validation, locking and overdraft logic, and a commit. It is the same code path
as production traffic. The benchmark uses accounts that may go negative so that
the run measures the write path rather than refusals.

## What decides pass or fail

The run exits non-zero if **any** of these is false:

- every request returned `201` (`checks` and `http_req_failed` thresholds);
- the final **deep reconciliation** reports no balance mismatch, no broken
  balance chain, and debits equal to credits in every currency.

There are deliberately **no latency or throughput thresholds**. Numbers depend
on the machine; they are measured and published, not promised.

## Reading the output

```
scenario      transfers/s   iterations   p50 ms   p90 ms   p95 ms   p99 ms   max ms   failed
spread              228.5         3427     77.3    132.5    158.5    236.6    688.0    0.00%
hot                  81.8         1227    292.1    473.4    535.8    640.7    839.6    0.00%
```

Latency is the time of the `POST /v1/transactions` request as seen by k6 and
includes queueing: with more virtual users than the database can serve, latency
grows while throughput stays flat (that is saturation, not a slowdown of the
code). Raw k6 output is written to `bench/results/summary.json`, and the
environment (CPU, memory, Docker, PostgreSQL, commit) to `bench/results/environment.txt`;
both are git-ignored. Quote the environment together with any number you publish.

## Why the hot scenario is slower

All transfers in `hot` need the same account row locked until their transaction
commits, so they run one after another. The ceiling is roughly
`1 / (time to commit + a few round trips)`. Commit time is dominated by the
storage's flush latency (`fsync`), so faster storage raises the ceiling.
Turning `synchronous_commit` off would raise it too, but could lose the most
recent commits in a crash, which is not acceptable for a ledger. The usual way
around a hot account is to split it into several sub-accounts and sum them.
