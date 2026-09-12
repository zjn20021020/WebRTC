const test = require('node:test');
const assert = require('node:assert/strict');
const { distribution, summarizeText, mediaObservations } = require('./acceptance_stats.cjs');

test('nearest-rank includes zero, excludes missing, and does not invent empty metrics', () => {
  assert.deepEqual(distribution([null, undefined, NaN, -1]), { n: 0, p50: null, p95: null, max: null });
  assert.deepEqual(distribution([0, 30, 10, 20]), { n: 4, p50: 10, p95: 30, max: 30 });
  assert.equal(distribution(Array.from({ length: 100 }, (_, i) => i + 1)).p95, 95);
});

test('false fallback is a failure even when the expected label is false', () => {
  const suite = { version: 'test', cases: [
    { id: 'no', kind: 'interrupt', category: 'c', expected_interrupt: false },
    { id: 'yes', kind: 'interrupt', category: 'c', expected_interrupt: true },
    { id: 'action', kind: 'action', category: 'c', expected_action: 'plant' },
  ] };
  const records = [
    { id: 'no', round: 1, actual: false, passed: true, fallback: true, error: 'timeout', latency_ms: 2000 },
    { id: 'yes', round: 1, actual: false, fallback: true, error: 'invalid_output', latency_ms: 20 },
    { id: 'action', round: 1, actual: 'plant', latency_ms: 10 },
  ];
  const s = summarizeText(suite, 2, records);
  assert.equal(s.passed, 1);
  assert.equal(s.planned, 6);
  assert.equal(s.missing, 3);
  assert.equal(s.errors, 2);
  assert.equal(s.interruptions.missed_interrupt_rate, 1);
  assert.equal(s.interruptions.false_interrupt_rate, 0);
  assert.equal(s.interruptions.effective_confusion_with_false_fallback.true_negative, 1);
  assert.equal(s.interruptions.valid_response_confusion.true_negative, 0);
  assert.equal(s.interruptions.valid_response_latency_ms.n, 0);
  assert.throws(() => summarizeText(suite, 1, [...records, records[0]]), /duplicate/);
});

test('false positives use negative cases as denominator and valid wrong labels remain failures', () => {
  const suite = { version: 'test', cases: [{ id: 'no', kind: 'interrupt', category: 'c', expected_interrupt: false }] };
  const s = summarizeText(suite, 2, [{ id: 'no', round: 1, actual: true, latency_ms: 10 }, { id: 'no', round: 2, actual: false, latency_ms: 20 }]);
  assert.equal(s.interruptions.false_interrupt_rate, 0.5);
  assert.equal(s.interruptions.missed_interrupt_rate, null);
  assert.equal(s.pass_rate, 0.5);
  assert.deepEqual(s.unstable_cases, ['no']);
});

test('media snapshots count once per turn and deferred waits stay separate', () => {
  const events = [
    { event: 'input_dispatched', utterance_id: 'a' },
    { event: 'response_status', status: 'listening', response_epoch: 1 },
    { event: 'response_metrics', response_epoch: 1, metrics: { asr_final_to_first_text_ms: 0 } },
    { event: 'response_metrics', response_epoch: 1, metrics: { asr_final_to_first_text_ms: 0, asr_final_to_first_audio_ms: 100 } },
    { event: 'input_buffered', utterance_id: 'b' },
    { event: 'input_dispatched', utterance_id: 'b' },
    { event: 'response_status', status: 'listening', response_epoch: 2 },
    { event: 'response_metrics', response_epoch: 2, metrics: { asr_final_to_first_audio_ms: 10000 } },
    { event: 'intent_result', response_epoch: 2, interrupt: true, at: '2026-09-12T00:00:00.000Z' },
    { event: 'response_cancelled', response_epoch: 2, at: '2026-09-12T00:00:00.120Z' },
    { event: 'response_status', response_epoch: 2, status: 'speaking' },
    { event: 'turn_transition', response_epoch: 3 },
    { event: 'response_metrics', response_epoch: 3, metrics: { asr_final_to_first_audio_ms: 200 } },
  ];
  const m = mediaObservations({ events });
  assert.deepEqual(m.metrics.direct.asr_final_to_first_text_ms, [0]);
  assert.deepEqual(m.metrics.direct.asr_final_to_first_audio_ms, [100]);
  assert.deepEqual(m.metrics.deferred.asr_final_to_first_audio_ms, [10000]);
  assert.deepEqual(m.metrics.interrupted.asr_final_to_first_audio_ms, [200]);
  assert.deepEqual(m.confirm_to_cancel_ms, [120]);
  assert.equal(m.old_turn_resumed_events, 1);
});
