const fs = require('node:fs');

function distribution(values) {
  const sorted = values.filter(v => typeof v === 'number' && Number.isFinite(v) && v >= 0).sort((a, b) => a - b);
  const percentile = p => sorted.length ? sorted[Math.ceil(p * sorted.length) - 1] : null;
  return { n: sorted.length, p50: percentile(0.50), p95: percentile(0.95), max: sorted.at(-1) ?? null };
}

const rate = (count, total) => total ? count / total : null;
const expectedValue = c => c.kind === 'action' ? (c.expected_plan ?? c.expected_action) : c.expected_interrupt;
const label = value => Array.isArray(value) ? JSON.stringify(value) : String(value);
const sameValue = (a, b) => Array.isArray(a) && Array.isArray(b)
  ? a.length === b.length && a.every((item, i) => item === b[i]) : a === b;
const matrix = () => ({ true_positive: 0, false_positive: 0, true_negative: 0, false_negative: 0 });
function addDecision(m, expected, actual) {
  m[expected ? (actual ? 'true_positive' : 'false_negative') : (actual ? 'false_positive' : 'true_negative')]++;
}

function summarizeText(suite, repeat, records) {
  const byKey = new Map();
  for (const r of records) {
    const key = `${r.id}/${r.round}`;
    if (byKey.has(key) || !suite.cases.some(c => c.id === r.id) || !Number.isInteger(r.round) || r.round < 1 || r.round > repeat) {
      throw new Error(`Unexpected or duplicate result: ${key}`);
    }
    byKey.set(key, r);
  }
  const summarize = cases => {
    const s = { planned: cases.length * repeat, attempted: 0, passed: 0, errors: 0, missing: 0, failures: [], latency_ms: null };
    const latencies = [], validLatencies = [];
    for (const c of cases) for (let round = 1; round <= repeat; round++) {
      const r = byKey.get(`${c.id}/${round}`);
      if (!r) { s.missing++; s.failures.push({ id: c.id, round, reason: 'not_run' }); continue; }
      s.attempted++;
      latencies.push(r.latency_ms);
      const expected = expectedValue(c);
      if (r.error || r.fallback) s.errors++;
      else validLatencies.push(r.latency_ms);
      if (!r.error && !r.fallback && sameValue(r.actual, expected)) s.passed++;
      else s.failures.push({ id: c.id, round, expected, actual: r.actual, reason: r.error || (r.fallback ? 'fallback' : 'wrong_label'), validation: r.validation });
    }
    s.pass_rate = rate(s.passed, s.planned);
    s.error_rate = rate(s.errors, s.attempted);
    s.latency_ms = distribution(latencies);
    s.valid_response_latency_ms = distribution(validLatencies);
    return s;
  };
  const actions = summarize(suite.cases.filter(c => c.kind === 'action'));
  const interruptions = summarize(suite.cases.filter(c => c.kind === 'interrupt'));
  const valid = matrix(), effective = matrix(), confusion = {};
  for (const c of suite.cases) for (let round = 1; round <= repeat; round++) {
    const r = byKey.get(`${c.id}/${round}`);
    if (c.kind === 'action') {
      const actual = !r ? 'not_run' : r.error || r.fallback ? 'error' : label(r.actual);
      const expected = label(expectedValue(c));
      confusion[expected] ||= {};
      confusion[expected][actual] = (confusion[expected][actual] || 0) + 1;
    } else if (r) {
      const failed = !!(r.error || r.fallback);
      // Provider failures keep playback running, but never become successful labels.
      addDecision(effective, c.expected_interrupt, failed ? false : r.actual);
      if (!failed) addDecision(valid, c.expected_interrupt, r.actual);
    }
  }
  actions.confusion_matrix = confusion;
  interruptions.valid_response_confusion = valid;
  interruptions.effective_confusion_with_false_fallback = effective;
  interruptions.false_interrupt_rate = rate(effective.false_positive, effective.false_positive + effective.true_negative);
  interruptions.missed_interrupt_rate = rate(effective.false_negative, effective.false_negative + effective.true_positive);
  const cases = suite.cases.map(c => ({ id: c.id, kind: c.kind, category: c.category, ...summarize([c]),
    outcomes: [...new Set(records.filter(r => r.id === c.id).map(r => r.error || (r.fallback ? 'fallback' : label(r.actual))))] }));
  return { version: suite.version, unique_cases: suite.cases.length, repeat, ...summarize(suite.cases), actions, interruptions,
    unstable_cases: cases.filter(c => c.outcomes.length > 1).map(c => c.id),
    categories: Object.fromEntries([...new Set(suite.cases.map(c => c.category))].map(category => [category, summarize(suite.cases.filter(c => c.category === category))])), cases };
}

function mediaObservations(evidence) {
  const events = evidence.events || [], metrics = new Map(), groups = new Map(), buffered = new Set();
  const confirmations = new Map(), cancelled = new Set(), confirmToCancel = [];
  let nextGroup = null, residual = 0;
  for (const e of events) {
    if (e.event === 'input_buffered') buffered.add(e.utterance_id);
    if (e.event === 'input_dispatched') nextGroup = buffered.has(e.utterance_id) ? 'deferred' : 'direct';
    if (e.event === 'response_status' && e.status === 'listening' && nextGroup) { groups.set(e.response_epoch, nextGroup); nextGroup = null; }
    if (e.event === 'turn_transition') groups.set(e.response_epoch, 'interrupted');
    if (e.event === 'plan_step_transition') groups.set(e.response_epoch, 'planned');
    if (e.event === 'response_metrics') metrics.set(e.response_epoch, { ...metrics.get(e.response_epoch), ...e.metrics });
    if (e.event === 'intent_result' && e.interrupt === true && !e.fallback) confirmations.set(e.response_epoch, Date.parse(e.at));
    if (e.event === 'response_cancelled') {
      const start = confirmations.get(e.response_epoch);
      if (start !== undefined) confirmToCancel.push(Date.parse(e.at) - start);
      cancelled.add(e.response_epoch);
    } else if (cancelled.has(e.response_epoch) && ['response_status', 'tool_status'].includes(e.event) && ['speaking', 'completed'].includes(e.status)) residual++;
  }
  const byGroup = {};
  for (const [epoch, m] of metrics) {
    const group = groups.get(epoch) || 'unknown';
    byGroup[group] ||= {};
    for (const [key, value] of Object.entries(m)) (byGroup[group][key] ||= []).push(value);
  }
  return { metrics: byGroup, confirm_to_cancel_ms: confirmToCancel, old_turn_resumed_events: residual,
    action_latency_ms: events.filter(e => e.event === 'action_result').map(e => e.latency_ms),
    intent_latency_ms: events.filter(e => e.event === 'intent_result').map(e => e.latency_ms),
    action_fallbacks: events.filter(e => e.event === 'action_result' && e.fallback).length,
    intent_fallbacks: events.filter(e => e.event === 'intent_result' && e.fallback).length,
    action_retries: events.filter(e => e.event === 'action_retry').length,
    audio_received: evidence.media?.some(r => r.type === 'inbound-rtp' && r.totalAudioEnergy > 0) || false };
}

function summarizeMedia(runs) {
  const merged = {}, confirm = [], actions = [], intents = [];
  let resumed = 0, actionFallbacks = 0, intentFallbacks = 0, retries = 0;
  const results = runs.map(run => {
    const evidence = run.evidence && fs.existsSync(run.evidence) ? JSON.parse(fs.readFileSync(run.evidence, 'utf8')) : {};
    const o = mediaObservations(evidence);
    for (const [group, metrics] of Object.entries(o.metrics)) {
      merged[group] ||= {};
      for (const [key, values] of Object.entries(metrics)) (merged[group][key] ||= []).push(...values);
    }
    confirm.push(...o.confirm_to_cancel_ms); actions.push(...o.action_latency_ms); intents.push(...o.intent_latency_ms);
    resumed += o.old_turn_resumed_events; actionFallbacks += o.action_fallbacks; intentFallbacks += o.intent_fallbacks; retries += o.action_retries;
    return { id: run.id, round: run.round, passed: run.exit_code === 0 && evidence.passed === true && o.audio_received && o.old_turn_resumed_events === 0,
      exit_code: run.exit_code, error: evidence.error || run.error, evidence: run.relative_evidence,
      audio_received: o.audio_received, old_turn_resumed_events: o.old_turn_resumed_events };
  });
  return { planned: runs.length, passed: results.filter(r => r.passed).length, pass_rate: rate(results.filter(r => r.passed).length, runs.length),
    old_turn_resumed_events: resumed, physical_audio_residual: 'not_measured', action_fallbacks: actionFallbacks, intent_fallbacks: intentFallbacks, action_retries: retries,
    confirm_to_cancel_ms: distribution(confirm), action_latency_ms: distribution(actions), intent_latency_ms: distribution(intents),
    response_metrics_ms: Object.fromEntries(Object.entries(merged).map(([group, metrics]) => [group, Object.fromEntries(Object.entries(metrics).map(([key, values]) => [key, distribution(values)]))])), runs: results };
}

function markdown(report) {
  const t = report.text, m = report.media;
  const pct = n => n === null ? '--' : `${(n * 100).toFixed(2)}%`;
  const value = n => n ?? '--';
  const lines = ['# 固定验收报告', '', `- 时间：${report.recorded_at}`, `- 结果：${report.passed ? '通过' : '未通过'}`,
    `- 验收集：${t.version}；${t.unique_cases} 个样例，每例 ${t.repeat} 次`, `- 模型：${report.model || '未取得'}`,
    `- 提交：${report.git_commit}；实现摘要：${report.source_sha256}`,
    `- TTS 音色：${report.audio_config?.tts_voice_type ?? '未测量'}；采样率：${report.audio_config?.sample_rate ?? '未测量'}`, '',
    '## 文本分类', '', '| 项目 | 通过 / 计划 | 严格通过率 | 请求错误 | 未执行 | 延迟 P50 / P95（ms） |', '| --- | ---: | ---: | ---: | ---: | ---: |'];
  for (const [name, s] of [['动作分类', t.actions], ['打断分类', t.interruptions]]) lines.push(`| ${name} | ${s.passed} / ${s.planned} | ${pct(s.pass_rate)} | ${s.errors} | ${s.missing} | ${value(s.latency_ms.p50)} / ${value(s.latency_ms.p95)} |`);
  const c = t.interruptions.effective_confusion_with_false_fallback;
  lines.push('', `误打断：${c.false_positive} / ${c.false_positive + c.true_negative}（${pct(t.interruptions.false_interrupt_rate)}）；漏打断：${c.false_negative} / ${c.false_negative + c.true_positive}（${pct(t.interruptions.missed_interrupt_rate)}）。`,
    '', '分母分别为已执行的预期 false / true 样本。请求异常按运行时 false 兜底计入有效决策，但严格通过率始终将异常计为失败。未执行样本另列，不进入误/漏率分母。',
    '', '## 真实语音闭环', '', m.planned ? `通过 ${m.passed} / ${m.planned}；旧轮取消后恢复事件 ${m.old_turn_resumed_events}；动作兜底 ${m.action_fallbacks}；打断兜底 ${m.intent_fallbacks}；动作重试 ${m.action_retries}。` : '本次未运行媒体验收。',
    '', '| 场景 | 轮次 | 结果 | 证据 |', '| --- | ---: | --- | --- |');
  for (const r of m.runs) lines.push(`| ${r.id} | ${r.round} | ${r.passed ? '通过' : '失败'} | [JSON](${r.evidence}) |`);
  lines.push('', '## 延迟', '', '| 指标 | 样本数 | P50（ms） | P95（ms） | 最大值（ms） |', '| --- | ---: | ---: | ---: | ---: |');
  const latencyRow = (name, d) => lines.push(`| ${name} | ${d.n} | ${value(d.p50)} | ${value(d.p95)} | ${value(d.max)} |`);
  latencyRow('浏览器确认事件到取消事件', m.confirm_to_cancel_ms);
  latencyRow('媒体链路动作分类（含内部重试）', m.action_latency_ms);
  latencyRow('媒体链路打断分类请求', m.intent_latency_ms);
  for (const [group, metrics] of Object.entries(m.response_metrics_ms)) for (const [key, d] of Object.entries(metrics)) latencyRow(`${group}: ${key}`, d);
  lines.push('', '## 未通过样例', '');
  for (const f of t.failures) lines.push(`- ${f.id} / round ${f.round}: ${f.reason}; expected=${f.expected ?? '--'}, actual=${f.actual ?? '--'}${f.validation ? `; validation=${f.validation}` : ''}`);
  for (const r of m.runs.filter(r => !r.passed)) lines.push(`- ${r.id} / round ${r.round}: ${String(r.error || '场景断言或进程失败').replaceAll('\n', ' ')}`);
  if (!t.failures.length && m.runs.every(r => r.passed)) lines.push('无。');
  lines.push('', '## 测量边界', '',
    '- 文本层直接调用实际分类客户端，每次只请求一次，不包含 ASR、partial 调度门槛或动作内部重试。媒体层运行完整应用和实际云服务。',
    '- P50/P95 使用 nearest-rank；请求耗时包含错误和超时。JSON 另列合法响应耗时、分类混淆矩阵、分类别和逐样例结果。',
    '- 重复输入用于检查回归和波动，不代表独立用户样本，也不承诺线上准确率。',
    '- 响应指标在每次媒体运行中按轮次取最后一个已知值，避免累计快照重复计数；direct 为直接响应，interrupted 为打断后响应，deferred 为缓存派发，planned 为计划后续步骤，后两者包含有意等待。speech_to_duck 每轮仅保留最后一次已知值。',
    '- 首音频指服务端首个非静音 RTP 发送；确认到取消指浏览器收到控制事件的间隔。均不是物理耳机延迟。',
    '- 媒体输入为固定合成音频经 WebAudio 虚拟麦克风进入真实 WebRTC；物理输出静音。旧轮恢复检查不等于耳机尾音测量。',
    '- 跨 final 合并仅在 cross-final-wait 场景通过且记录至少两个来源 final 时计为已测；文本分类测试不代表跨 final 调度已验证。未覆盖真人噪声/回声、多说话人和长期弱网。', '');
  return lines.join('\n');
}

module.exports = { distribution, summarizeText, mediaObservations, summarizeMedia, markdown };
