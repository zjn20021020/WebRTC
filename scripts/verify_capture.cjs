const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

// Stress the local audio graph independently of cloud recognition. A continuous
// test signal must survive dev event rendering, GC and a suspend/resume cycle.
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true,
    args: ['--js-flags=--expose-gc', '--autoplay-policy=no-user-gesture-required', '--mute-audio', '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(process.env.DEMO_URL || 'http://localhost:8081');
    const result = await page.evaluate(async () => {
      const context = new AudioContext();
      await context.resume();
      const oscillator = context.createOscillator(), gain = context.createGain(), destination = context.createMediaStreamDestination();
      gain.gain.value = 0.15;
      oscillator.connect(gain).connect(destination);
      oscillator.start();
      const source = context.createMediaStreamSource(destination.stream), analyser = context.createAnalyser();
      source.connect(analyser);
      analyser.fftSize = 1024;
      const connection = { audioContext: context, stream: destination.stream, source, analyser, abort: new AbortController() };
      activeConnection = connection;
      context.onstatechange = () => { if (activeConnection === connection && context.state === 'suspended') context.resume(); };
      drawWaveform(analyser, new Float32Array(analyser.fftSize), connection);
      const readings = [];
      for (let round = 0; round < 30; round++) {
        for (let i = 0; i < 30; i++) {
          handleControl(JSON.stringify({ event: 'input_merge', response_epoch: 1, status: 'collecting', text: '给我讲个故事，要和夜晚和月亮相关的。', source_utterance_ids: ['s:1', 's:2'] }));
          handleControl(JSON.stringify({ event: 'plan_status', response_epoch: 1, plan_id: 'stress', status: 'running', step_index: 1, step_count: 2,
            steps: [{ action: 'water', text: '去浇水', status: 'running' }, { action: 'general_qa', text: '给我讲个故事', status: 'pending' }] }));
        }
        if (window.gc) window.gc();
        await new Promise(resolve => setTimeout(resolve, 100));
        readings.push(connection.localRMS);
      }
      await context.suspend();
      await new Promise(resolve => setTimeout(resolve, 150));
      const resumed = context.state === 'running' && connection.localRMS > 0.05;
      const output = { minimum_rms: Math.min(...readings), samples: readings.length, frame_gap_ms: connection.frameGapMS, control_events: connection.controlEvents, resumed, meter: meterElement.textContent };
      disconnect();
      oscillator.stop();
      return output;
    });
    assert.equal(errors.length, 0, errors.join('\n'));
    assert.ok(result.minimum_rms > 0.05, 'Capture graph went silent under dev events or GC');
    assert.ok(result.resumed, 'AudioContext did not resume');
    const settings = await page.evaluate(async () => {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints() });
      activeConnection = { stream, abort: new AbortController() };
      logMicrophoneSettings(stream.getAudioTracks()[0]);
      return stream.getAudioTracks()[0].getSettings();
    });
    for (const key of ['echoCancellation', 'noiseSuppression', 'autoGainControl']) assert.equal(settings[key], false, `${key} was not disabled by headphone mode`);
    await page.evaluate(() => disconnect());
    assert.equal(await page.locator('input[name="audioMode"]').count(), 2, 'Missing manual audio mode fallback');
    const reconnectedSettings = await page.evaluate(async () => {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints() });
      const settings = stream.getAudioTracks()[0].getSettings();
      stream.getTracks().forEach(track => track.stop());
      return settings;
    });
    for (const key of ['echoCancellation', 'noiseSuppression', 'autoGainControl']) assert.equal(reconnectedSettings[key], false, `${key} changed on reconnect`);
    result.fixed_headphone_constraints = true;
    fs.mkdirSync('bin/asr-probe', { recursive: true });
    await page.screenshot({ path: 'bin/asr-probe/capture-desktop.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: 'bin/asr-probe/capture-mobile.png', fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    fs.writeFileSync('bin/asr-probe/capture-stress.json', JSON.stringify({ ...result, passed: true, physical_microphone: false }, null, 2));
    console.log(result);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
