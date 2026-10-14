// ledgerd load test.
//
// Two scenarios run one after the other against a running stack:
//
//   spread  many transfers between disjoint pairs of accounts (little contention)
//   hot     every transfer credits ONE account (all writers queue on its row lock)
//
// The difference between them is the "hot account" ceiling documented in the
// README. When the run ends, a deep reconciliation verifies the whole ledger and
// the run FAILS if anything is inconsistent. No latency or throughput target is
// asserted: the goal is to measure and publish, not to promise a number.
//
// Configuration (environment variables):
//   BASE_URL   default http://api:8085 (the compose network)
//   API_KEY    required
//   VUS        concurrent virtual users per scenario            (default 20)
//   DURATION   how long each scenario runs                      (default 20s)
//   PAIRS      source/sink account pairs for the spread scenario (default 200)
//   SCENARIOS  comma list of spread,hot                         (default both)
import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://api:8085';
const API_KEY = __ENV.API_KEY;
const VUS = parseInt(__ENV.VUS || '20', 10);
const DURATION = __ENV.DURATION || '20s';
const PAIRS = parseInt(__ENV.PAIRS || '200', 10);
const WANTED = (__ENV.SCENARIOS || 'spread,hot').split(',').map((s) => s.trim());

if (!API_KEY) {
  fail('API_KEY is required');
}

// Seconds of a duration like "20s" or "1m" (only s and m are supported here).
function seconds(d) {
  const m = /^(\d+)(s|m)$/.exec(d);
  if (!m) fail(`DURATION must look like 20s or 2m, got ${d}`);
  return parseInt(m[1], 10) * (m[2] === 'm' ? 60 : 1);
}
const SCENARIO_SECONDS = seconds(DURATION);
const GAP_SECONDS = 5; // let the previous scenario drain before the next one starts

const scenarios = {};
let offset = 0;
for (const name of ['spread', 'hot']) {
  if (!WANTED.includes(name)) continue;
  scenarios[name] = {
    executor: 'constant-vus',
    exec: name,
    vus: VUS,
    duration: DURATION,
    startTime: `${offset}s`,
    gracefulStop: '10s',
  };
  offset += SCENARIO_SECONDS + GAP_SECONDS;
}

const sent = new Counter('transfers_sent');

export const options = {
  scenarios,
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    // Correctness: every request must succeed and every check must pass.
    checks: ['rate==1'],
    'http_req_failed{scenario:spread}': ['rate==0'],
    'http_req_failed{scenario:hot}': ['rate==0'],
    // Declared only so that per-scenario latency appears in the summary
    // (k6 records a tagged series only when a threshold refers to it).
    'http_req_duration{scenario:spread}': ['max>=0'],
    'http_req_duration{scenario:hot}': ['max>=0'],
    'iterations{scenario:spread}': ['count>=0'],
    'iterations{scenario:hot}': ['count>=0'],
  },
};

const RUN_ID = `${Date.now().toString(36)}${Math.floor(Math.random() * 1e6).toString(36)}`;

function headers(extra) {
  return Object.assign({ Authorization: `Bearer ${API_KEY}`, 'Content-Type': 'application/json' }, extra || {});
}

function createAccounts(specs) {
  const reqs = specs.map((s) => ['POST', `${BASE_URL}/v1/accounts`, JSON.stringify(s), { headers: headers() }]);
  const out = [];
  // Batches keep setup fast without opening hundreds of connections at once.
  for (let i = 0; i < reqs.length; i += 50) {
    const responses = http.batch(reqs.slice(i, i + 50));
    for (const r of responses) {
      if (r.status !== 201) fail(`account creation failed: ${r.status} ${r.body}`);
      out.push(r.json('id'));
    }
  }
  return out;
}

export function setup() {
  const need = (name) => WANTED.includes(name);
  const specs = [];
  const add = (code, type, normal) =>
    specs.push({ code: `bench-${RUN_ID}-${code}`, currency: 'USD', type, normal_balance: normal, allow_negative: true });

  if (need('spread')) {
    for (let i = 0; i < PAIRS; i++) {
      add(`src-${i}`, 'ASSET', 'DEBIT');
      add(`dst-${i}`, 'LIABILITY', 'CREDIT');
    }
  }
  if (need('hot')) {
    for (let i = 0; i < VUS * 4; i++) add(`hot-src-${i}`, 'ASSET', 'DEBIT');
    add('collector', 'LIABILITY', 'CREDIT');
  }
  const ids = createAccounts(specs);

  let k = 0;
  const data = { spread: { src: [], dst: [] }, hot: { src: [], collector: null } };
  if (need('spread')) {
    for (let i = 0; i < PAIRS; i++) {
      data.spread.src.push(ids[k++]);
      data.spread.dst.push(ids[k++]);
    }
  }
  if (need('hot')) {
    for (let i = 0; i < VUS * 4; i++) data.hot.src.push(ids[k++]);
    data.hot.collector = ids[k++];
  }
  data.accounts = ids.length;
  return data;
}

function transfer(scenario, debit, credit) {
  const amount = String(1 + Math.floor(Math.random() * 100));
  const body = JSON.stringify({
    description: `bench ${scenario}`,
    postings: [
      { account_id: debit, direction: 'DEBIT', amount, currency: 'USD' },
      { account_id: credit, direction: 'CREDIT', amount, currency: 'USD' },
    ],
  });
  const res = http.post(`${BASE_URL}/v1/transactions`, body, {
    headers: headers({ 'Idempotency-Key': `${RUN_ID}-${scenario}-${__VU}-${__ITER}` }),
    tags: { name: `POST /v1/transactions (${scenario})` },
  });
  const ok = check(res, { 'transfer accepted (201)': (r) => r.status === 201 });
  if (ok) sent.add(1);
}

export function spread(data) {
  const i = Math.floor(Math.random() * data.spread.src.length);
  transfer('spread', data.spread.src[i], data.spread.dst[i]);
}

export function hot(data) {
  const src = data.hot.src[Math.floor(Math.random() * data.hot.src.length)];
  transfer('hot', src, data.hot.collector);
}

// The decisive check: the ledger must be internally consistent after the load.
export function teardown(data) {
  const res = http.post(`${BASE_URL}/v1/reconciliations?deep=true`, null, { headers: headers(), timeout: '300s' });
  if (res.status !== 200) fail(`reconciliation request failed: ${res.status} ${res.body}`);
  const report = res.json();
  const consistent = report.ok === true && report.mismatches.length === 0 && report.chain_breaks.length === 0 &&
    report.currencies.every((c) => c.balanced);
  console.log(`reconciliation: ok=${report.ok} accounts_checked=${report.accounts_checked} ` +
    `mismatches=${report.mismatches.length} chain_breaks=${report.chain_breaks.length} ` +
    `currencies=${JSON.stringify(report.currencies)}`);
  if (!consistent) fail('LEDGER INCONSISTENT after the load test: ' + JSON.stringify(report));
  if (report.accounts_checked < data.accounts) fail(`reconciliation saw ${report.accounts_checked} accounts, expected at least ${data.accounts}`);
}

// ---- summary ----

function row(m, key) {
  return m && m.values && m.values[key] !== undefined ? m.values[key] : NaN;
}
const ms = (v) => (Number.isNaN(v) ? '   n/a' : v.toFixed(1).padStart(8));

export function handleSummary(data) {
  const lines = [];
  lines.push('');
  lines.push(`ledgerd benchmark  (VUS=${VUS} per scenario, DURATION=${DURATION}, run ${RUN_ID})`);
  lines.push('');
  lines.push('scenario      transfers/s   iterations   p50 ms   p90 ms   p95 ms   p99 ms   max ms   failed');
  for (const name of Object.keys(scenarios)) {
    const dur = data.metrics[`http_req_duration{scenario:${name}}`];
    const its = data.metrics[`iterations{scenario:${name}}`];
    const failed = data.metrics[`http_req_failed{scenario:${name}}`];
    const count = its ? its.values.count : 0;
    lines.push(
      `${name.padEnd(12)} ${(count / SCENARIO_SECONDS).toFixed(1).padStart(12)} ${String(count).padStart(12)} ` +
      `${ms(row(dur, 'med'))} ${ms(row(dur, 'p(90)'))} ${ms(row(dur, 'p(95)'))} ${ms(row(dur, 'p(99)'))} ${ms(row(dur, 'max'))} ` +
      `${(failed ? (failed.values.rate * 100).toFixed(2) : 'n/a').toString().padStart(7)}%`,
    );
  }
  lines.push('');
  lines.push('Throughput is accepted transfers per second over the scenario duration. A transfer is');
  lines.push('one transaction with two postings. See the README for the hardware and configuration.');
  lines.push('');
  return {
    stdout: lines.join('\n'),
    '/results/summary.json': JSON.stringify(data, null, 2),
  };
}
