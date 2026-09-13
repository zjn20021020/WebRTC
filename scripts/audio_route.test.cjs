const assert = require('node:assert/strict');
const { test } = require('node:test');
const { pathToFileURL } = require('node:url');
const path = require('node:path');
const modulePromise = import(pathToFileURL(path.resolve(__dirname, '../web/audio-route.js')));
const output = (label, deviceId = 'default') => ({ kind: 'audiooutput', label, deviceId });

test('output route decisions prioritize headphones and ignore input names', async () => {
  const { selectAudioRoute } = await modulePromise;
  const cases = [
    { devices: [output('Default - Speakers (HECATE G2 GAMING HEADSET)')], system: { kind: 'speakers', name: 'Speakers (HECATE G2 GAMING HEADSET)' }, want: 'headphones' },
    { devices: [output('默认 - 扬声器 (HECATE G2 GAMING HEADSET)')], system: { kind: 'speakers', name: '扬声器 (HECATE G2 GAMING HEADSET)' }, want: 'headphones' },
    { devices: [output('AirPods Pro')], want: 'headphones' },
    { devices: [output('耳麦')], want: 'headphones' },
    { devices: [output('Headphones (USB)')], want: 'headphones' },
    { devices: [output('默认 - 扬声器 (Realtek)')], system: { kind: 'speakers', name: '扬声器 (Realtek)' }, want: 'speakers' },
    { devices: [output('Speakers (USB)')], want: 'speakers' },
    { devices: [output('Device 123')], system: { kind: 'headphones', name: 'Device 123' }, want: 'headphones' },
    { devices: [], system: { kind: 'headphones', name: 'Device 123' }, want: 'headphones' },
    { devices: [], want: 'unknown' },
    { devices: [output('')], want: 'unknown' },
    { devices: [output('Monitor (HDMI)')], want: 'unknown' },
    { devices: [{ kind: 'audioinput', label: 'Headset microphone' }, output('Speakers')], want: 'speakers' },
    { devices: [output('Device B')], system: { kind: 'speakers', name: 'Device A' }, want: 'unknown' },
    { devices: [output('Headphones'), output('Speakers', 'custom')], sink: 'custom', system: { kind: 'headphones', name: 'Headphones' }, want: 'speakers' },
    { devices: [output('Speakers'), output('Headphones', 'custom')], sink: 'custom', system: { kind: 'speakers', name: 'Speakers' }, want: 'headphones' },
    { devices: [output('Speakers')], sink: 'unavailable', system: { kind: 'speakers', name: 'Speakers' }, want: 'unknown' },
  ];
  for (const value of cases) {
    const route = selectAudioRoute(value.devices, value.sink, value.system);
    assert.equal(route.kind, value.want, JSON.stringify(value));
    assert.equal(route.aec, value.want === 'speakers');
  }
});

function streamWith(settings) {
  const track = { getSettings: () => settings, stopped: false, stop() { this.stopped = true; } };
  return { getAudioTracks: () => [track], getTracks: () => [track] };
}

test('route changes reacquire the mode and preserve the selected microphone', async () => {
  const { prepareAudioRoute } = await modulePromise;
  let stream = streamWith({ echoCancellation: false, deviceId: 'chosen' }), calls = [];
  const getUserMedia = async value => { calls.push(value); return streamWith({ ...value.audio, deviceId: 'chosen' }); };
  const signal = new AbortController().signal;
  assert.equal((await prepareAudioRoute(stream, { aec: false }, signal, getUserMedia)).stream, stream);
  assert.equal(calls.length, 0);
  for (const aec of [true, false]) {
    const prepared = await prepareAudioRoute(stream, { aec }, signal, getUserMedia);
    assert.equal(prepared.route.aec, aec);
    assert.equal(stream.getTracks()[0].stopped, false, 'Old audio must remain alive until replaceTrack succeeds');
    stream = prepared.stream;
    assert.deepEqual(calls.at(-1), { audio: { echoCancellation: aec, noiseSuppression: false, autoGainControl: false, deviceId: { exact: 'chosen' } } });
  }
});

test('constraint rejection falls back to raw capture and is reported', async () => {
  const { prepareAudioRoute } = await modulePromise;
  const stream = streamWith({ echoCancellation: false });
  const result = await prepareAudioRoute(stream, { aec: true }, new AbortController().signal, async () => { throw new Error('not supported'); });
  assert.equal(result.stream, stream);
  assert.equal(result.route.aec, false);
  assert.equal(result.route.reason, 'constraints_failed');
});

test('ignored constraints are visible and aborted requests do not change capture', async () => {
  const { prepareAudioRoute } = await modulePromise;
  const signal = new AbortController();
  const stream = streamWith({ echoCancellation: false }), candidate = streamWith({ echoCancellation: false });
  const getUserMedia = async () => candidate;
  assert.equal((await prepareAudioRoute(stream, { aec: true }, signal.signal, getUserMedia)).route.reason, 'aec_not_enabled');
  assert.equal(candidate.getTracks()[0].stopped, true);
  assert.equal(stream.getTracks()[0].stopped, false);
  signal.abort();
  await assert.rejects(prepareAudioRoute(stream, { aec: true }, signal.signal, getUserMedia), { name: 'AbortError' });
});

test('disconnect during capture acquisition closes the late stream', async () => {
  const { prepareAudioRoute } = await modulePromise;
  const controller = new AbortController(), candidate = streamWith({ echoCancellation: true });
  await assert.rejects(prepareAudioRoute(streamWith({ echoCancellation: false }), { aec: true }, controller.signal,
    async () => { controller.abort(); return candidate; }), { name: 'AbortError' });
  assert.equal(candidate.getTracks()[0].stopped, true);
});
