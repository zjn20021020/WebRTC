const { chromium } = require('playwright');
const fs = require('node:fs');
const assert = require('node:assert/strict');

// Real WebRTC and cloud services; only the microphone source is a speech file.
(async () => {
  const browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'],
  });
  const scenarios = [];
  try {
    for (const mode of ['deferred', 'interrupt']) {
      const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
      const errors = [];
      page.on('pageerror', e => errors.push(e.message));
      try {
        await page.route('**/fixture-*.wav', route => {
          const name = new URL(route.request().url()).pathname.replace('/fixture-', '');
          if (!['ask-story.wav', 'interrupt-question.wav', 'deferred-question.wav'].includes(name)) return route.abort();
          return route.fulfill({ contentType: 'audio/wav', body: fs.readFileSync(`bin/${name}`) });
        });
        await page.addInitScript(() => {
          navigator.mediaDevices.getUserMedia = async () => {
            const context = new AudioContext();
            await context.resume();
            const destination = context.createMediaStreamDestination();
            const oscillator = context.createOscillator();
            const gain = context.createGain();
            gain.gain.value = 0.00001;
            oscillator.connect(gain).connect(destination);
            oscillator.start();
            window.testMic = { context, destination };
            return destination.stream;
          };
        });
        await page.goto(process.env.DEMO_URL || 'http://localhost:8080');
        await page.evaluate(() => {
          window.voiceEvents = [];
          const original = handleControl;
          handleControl = data => { voiceEvents.push({ at: new Date().toISOString(), ...JSON.parse(data) }); original(data); };
          window.playFixture = async name => {
            const response = await fetch(`/fixture-${name}.wav`);
            const source = testMic.context.createBufferSource();
            source.buffer = await testMic.context.decodeAudioData(await response.arrayBuffer());
            source.connect(testMic.destination);
            source.start();
          };
        });
        await page.locator('#connect').click();
        await page.waitForFunction(() => document.querySelector('#asrStatus').dataset.state === 'listening', null, { timeout: 15000 });
        await page.evaluate(() => playFixture('ask-story'));
        await page.waitForFunction(() => voiceEvents.some(e => e.metrics?.speech_end_to_first_audio_ms >= 0), null, { timeout: 45000 });
        const oldEpoch = await page.evaluate(() => responseEpoch);
        await page.evaluate(name => playFixture(name), `${mode}-question`);
        if (mode === 'deferred') {
          await page.waitForFunction(old => voiceEvents.some(e => e.event === 'input_buffered' && e.response_epoch === old), oldEpoch, { timeout: 15000 });
          assert.equal(await page.evaluate(() => responseEpoch), oldEpoch, 'False intent must preserve the old turn');
          assert.match(await page.locator('#queuedStatus').textContent(), /\d/);
          await page.screenshot({ path: 'bin/intent-buffered-desktop.png', fullPage: true });
        } else {
          await page.waitForFunction(old => voiceEvents.some(e => e.event === 'response_cancelled' && e.response_epoch === old), oldEpoch, { timeout: 15000 });
        }
        await page.waitForFunction(old => responseEpoch > old && document.querySelector('#replyStatus').dataset.state === 'completed'
          && /[2二]/.test(document.querySelector('#replyText').textContent), oldEpoch, { timeout: 90000 });
        const result = await page.evaluate(() => ({ events: voiceEvents, reply: document.querySelector('#replyText').textContent }));
        assert.ok(result.events.some(e => e.event === 'intent_result' && e.interrupt === (mode === 'interrupt')), 'Expected a validated model decision');
        assert.ok(result.events.some(e => e.status === 'ducking' && e.response_epoch === oldEpoch), 'Missing suspected-speech duck');
        if (mode === 'deferred') {
          assert.ok(!result.events.some(e => e.event === 'response_cancelled' && e.response_epoch === oldEpoch), 'False intent cancelled old playback');
          const completed = result.events.findIndex(e => e.status === 'completed' && e.response_epoch === oldEpoch);
          const nextThinking = result.events.findIndex(e => e.status === 'thinking' && e.response_epoch > oldEpoch);
          assert.ok(completed >= 0 && nextThinking > completed, 'Cached question started before previous playback completed');
        } else {
          const cancelled = result.events.findIndex(e => e.event === 'response_cancelled' && e.response_epoch === oldEpoch);
          const decision = result.events.findIndex(e => e.event === 'intent_result' && e.interrupt === true && e.response_epoch === oldEpoch);
          assert.ok(decision >= 0 && cancelled > decision, 'Hard stop bypassed model approval');
          assert.ok(result.events[cancelled].queue_dropped > 0, 'Old audio queue was not cleared');
          assert.ok(!result.events.slice(cancelled + 1).some(e => e.response_epoch === oldEpoch && ['speaking', 'completed'].includes(e.status)), 'Old playback revived');
        }
        assert.deepEqual(errors, []);
        await page.setViewportSize({ width: 390, height: 844 });
        await page.screenshot({ path: `bin/intent-${mode}-mobile.png`, fullPage: true });
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        scenarios.push({ mode, ...result });
        await page.locator('#disconnect').click();
        await page.evaluate(() => testMic.context.close());
      } catch (error) {
        console.error(mode, await page.evaluate(() => window.voiceEvents?.filter(e => e.event !== 'response_text')).catch(() => []));
        throw error;
      } finally { await page.close(); }
    }
    const evidence = { recordedAt: new Date().toISOString(), providers: ['Tencent ASR 8k_zh', 'deepseek-v4-pro', 'Tencent TextToStreamAudioWS'],
      input: 'Pre-generated Tencent speech through a WebAudio virtual microphone; physical output muted', duckGain: 0.5, scenarios };
    fs.mkdirSync('docs/evidence', { recursive: true });
    fs.writeFileSync('docs/evidence/intent-barge-in.json', `${JSON.stringify(evidence, null, 2)}\n`);
    console.log(JSON.stringify({ passed: scenarios.map(s => s.mode), replies: scenarios.map(s => s.reply),
      decisions: scenarios.flatMap(s => s.events.filter(e => e.event === 'intent_result')) }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
