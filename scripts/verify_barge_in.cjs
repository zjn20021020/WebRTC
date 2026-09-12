const { chromium } = require('playwright');
const fs = require('node:fs');
const assert = require('node:assert/strict');

// This uses real cloud providers behind the running server. Only microphone
// input is controlled: fixed Chinese speech and a short broadband noise burst.
(async () => {
  const browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'],
  });
  let page;
  try {
    page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.route('**/fixture-*.wav', route => {
      const name = new URL(route.request().url()).pathname.replace('/fixture-', '');
      if (!['ask-story.wav', 'interrupt-question.wav'].includes(name)) return route.abort();
      return route.fulfill({ contentType: 'audio/wav', body: fs.readFileSync(`bin/${name}`) });
    });
    await page.addInitScript(() => {
      navigator.mediaDevices.getUserMedia = async () => {
        const context = new AudioContext();
        await context.resume();
        const destination = context.createMediaStreamDestination();
        const keepAlive = context.createOscillator();
        const gain = context.createGain();
        gain.gain.value = 0.00001;
        keepAlive.connect(gain).connect(destination);
        keepAlive.start();
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
        const buffer = await testMic.context.decodeAudioData(await response.arrayBuffer());
        const source = testMic.context.createBufferSource();
        source.buffer = buffer;
        source.connect(testMic.destination);
        source.start();
      };
    });
    const mediaStats = () => page.evaluate(async () => {
      const stats = [...(await activeConnection.peerConnection.getStats()).values()];
      const inbound = stats.find(s => s.type === 'inbound-rtp');
      const outbound = stats.find(s => s.type === 'outbound-rtp');
      return { at: new Date().toISOString(), packetsReceived: inbound?.packetsReceived, packetsSent: outbound?.packetsSent,
        audioEnergy: inbound?.totalAudioEnergy, micLive: activeConnection.stream.getAudioTracks().every(t => t.readyState === 'live' && t.enabled) };
    });
    await page.locator('#connect').click();
    await page.waitForFunction(() => document.querySelector('#asrStatus').dataset.state === 'listening', null, { timeout: 15000 });
    await page.evaluate(() => playFixture('ask-story'));
    await page.waitForFunction(() => voiceEvents.some(e => e.metrics?.speech_end_to_first_audio_ms >= 0), null, { timeout: 45000 });
    await page.waitForTimeout(350);
    const oldEpoch = await page.evaluate(() => responseEpoch);
    const beforeNoise = await mediaStats();
    await page.evaluate(() => {
      const buffer = testMic.context.createBuffer(1, Math.floor(testMic.context.sampleRate * 0.32), testMic.context.sampleRate);
      const samples = buffer.getChannelData(0);
      let seed = 12345;
      for (let i = 0; i < samples.length; i++) { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; samples[i] = (seed / 4294967296 - 0.5) * 0.5; }
      const source = testMic.context.createBufferSource();
      source.buffer = buffer;
      source.connect(testMic.destination);
      source.start();
    });
    await page.waitForFunction(() => document.querySelector('#replyStatus').dataset.state === 'ducking', null, { timeout: 5000 });
    await page.screenshot({ path: 'bin/ducking-desktop.png', fullPage: true });
    await page.waitForTimeout(250);
    const duringDuck = await mediaStats();
    await page.waitForTimeout(350);
    const afterDuckAudio = await mediaStats();
    assert.ok(afterDuckAudio.audioEnergy > duringDuck.audioEnergy, 'Ducking must keep audible downlink audio');
    assert.ok(afterDuckAudio.packetsSent > beforeNoise.packetsSent && afterDuckAudio.micLive, 'Microphone must stay live while AI speaks and ducks');
    await page.waitForFunction(() => voiceEvents.some(e => e.reason === 'unconfirmed'), null, { timeout: 6000 });
    assert.equal(await page.evaluate(() => responseEpoch), oldEpoch, 'Noise must restore volume on the same turn');
    await page.evaluate(() => playFixture('interrupt-question'));
    await page.waitForFunction(old => voiceEvents.some(e => e.event === 'turn_transition' && e.previous_epoch === old), oldEpoch, { timeout: 15000 });
    await page.waitForFunction(old => responseEpoch > old && document.querySelector('#replyStatus').dataset.state === 'completed', oldEpoch, { timeout: 45000 });
    const result = await page.evaluate(() => ({ events: voiceEvents, reply: document.querySelector('#replyText').textContent }));
    const transitionIndex = result.events.findIndex(e => e.event === 'turn_transition' && e.previous_epoch === oldEpoch);
    const transition = result.events[transitionIndex];
    const finalIndex = result.events.findIndex(e => e.event === 'asr_final' && e.utterance_id === transition.utterance_id);
    assert.ok(transitionIndex >= 0 && finalIndex > transitionIndex, 'New turn must exist before its final transcript');
    assert.ok(result.events.some(e => e.event === 'response_cancelled' && e.response_epoch === oldEpoch && e.queue_dropped > 0), 'Old pending audio must be cleared on backend');
    const interruptedIndex = result.events.findIndex(e => e.response_epoch === oldEpoch && e.status === 'interrupted');
    assert.ok(!result.events.slice(interruptedIndex + 1).some(e => e.response_epoch === oldEpoch && ['speaking', 'completed'].includes(e.status)), 'Old response must never resume');
    assert.ok(result.reply.includes('2') || result.reply.includes('\u4e8c'), 'New question must receive a real answer');
    const afterNewReply = await mediaStats();
    await page.screenshot({ path: 'bin/stream-barge-in-desktop.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: 'bin/stream-barge-in-mobile.png', fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.locator('#disconnect').click();
    await page.evaluate(() => testMic.context.close());
    assert.deepEqual(errors, []);
    const evidence = { recordedAt: new Date().toISOString(), input: 'Pre-generated Tencent speech through a WebAudio virtual microphone; seeded 320ms broadband noise',
      providers: { asr: 'Tencent 8k_zh', llm: 'deepseek-v4-pro', tts: 'Tencent TextToStreamAudioWS' },
      checks: ['audible duck', 'continuous microphone uplink', 'noise recovery', 'backend queue cleared', 'new epoch before final', 'new answer', 'no old playback revival', 'desktop/mobile layout'],
      media: { beforeNoise, duringDuck, afterDuckAudio, afterNewReply }, ...result };
    fs.mkdirSync('docs/evidence', { recursive: true });
    fs.writeFileSync('docs/evidence/barge-in.json', `${JSON.stringify(evidence, null, 2)}\n`);
    console.log(JSON.stringify({ passed: evidence.checks, reply: result.reply, transition, media: evidence.media }));
  } catch (error) {
    if (page) console.error('Observed events:', await page.evaluate(() => window.voiceEvents?.filter(e => e.event !== 'response_text')).catch(() => []));
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
