const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

// Fixed audio enters the normal browser microphone path. No backend ASR or
// planner events are injected; the split test waits for a real provider final.
(async () => {
  const mode = process.argv.includes('--merge') ? 'merge' : process.argv.includes('--cancel') ? 'cancel' : 'sequence';
  const name = `plan-${mode}`;
  const directory = process.env.EVIDENCE_DIR || 'bin/plan-evidence';
  fs.mkdirSync(directory, { recursive: true });
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const save = result => fs.writeFileSync(path.join(directory, `${name}.json`), `${JSON.stringify(result, null, 2)}\n`);
  try {
    const fixtures = ['home-plan', 'home-harvest-direct', 'home-question', 'home-water', 'home-wait-prefix', 'home-wait-tail'];
    await page.route('**/fixture-home-*.wav', route => {
      const filename = new URL(route.request().url()).pathname.slice('/fixture-'.length, -4);
      if (!fixtures.includes(filename)) return route.abort();
      return route.fulfill({ contentType: 'audio/wav', body: fs.readFileSync(`bin/${filename}.wav`) });
    });
    await page.addInitScript(() => {
      navigator.mediaDevices.getUserMedia = async () => {
        const context = new AudioContext();
        await context.resume();
        const destination = context.createMediaStreamDestination();
        const oscillator = context.createOscillator(), gain = context.createGain();
        gain.gain.value = 0.00001;
        oscillator.connect(gain).connect(destination); oscillator.start();
        window.testMic = { context, destination };
        return destination.stream;
      };
    });
    await page.goto(process.env.DEMO_URL || 'http://localhost:8081');
    await page.evaluate(() => {
      window.voiceEvents = [];
      const original = handleControl;
      handleControl = data => { voiceEvents.push({ at: new Date().toISOString(), ...JSON.parse(data) }); original(data); };
      window.playFixture = async name => {
        const response = await fetch(`/fixture-${name}.wav`);
        const source = testMic.context.createBufferSource();
        source.buffer = await testMic.context.decodeAudioData(await response.arrayBuffer());
        source.connect(testMic.destination); source.start();
      };
    });
    await page.locator('#connect').click();
    await page.waitForFunction(() => document.querySelector('#asrStatus').dataset.state === 'listening', null, { timeout: 15000 });
    await page.evaluate(name => playFixture(name), mode === 'merge' ? 'home-water' : 'home-plan');
    await page.waitForFunction(() => voiceEvents.some(e => e.event === 'response_status' && e.status === 'speaking'), null, { timeout: 25000 });
    const initialEpoch = await page.evaluate(() => responseEpoch);
    assert.equal(await page.evaluate(() => voiceEvents.find(e => e.event === 'tool_status' && e.status === 'running')?.tool_call.name), mode === 'merge' ? 'water' : 'plant', 'Initial ASR/planning did not select the expected action');
    if (mode === 'merge') {
      await page.evaluate(() => playFixture('home-wait-prefix'));
      await page.waitForFunction(() => voiceEvents.some(e => e.event === 'asr_final' && /等一下/.test(e.text)), null, { timeout: 10000 });
      await page.evaluate(() => playFixture('home-wait-tail'));
    } else {
      await page.evaluate(() => playFixture('home-question'));
    }
    await page.waitForFunction(epoch => voiceEvents.some(e => e.event === 'input_buffered' && e.response_epoch === epoch), initialEpoch, { timeout: 15000 });
    const bufferedID = await page.evaluate(epoch => voiceEvents.find(e => e.event === 'input_buffered' && e.response_epoch === epoch).utterance_id, initialEpoch);
    if (mode === 'cancel') await page.evaluate(() => playFixture('home-harvest-direct'));
    const last = mode === 'merge' ? 'plant' : mode === 'cancel' ? 'harvest' : 'general_qa';
    await page.waitForFunction(last => voiceEvents.some(e => e.event === 'tool_status' && e.tool_call?.name === last && e.status === 'completed'), last, { timeout: 120000 });
    const result = await page.evaluate(async () => {
      const media = [];
      (await activeConnection.peerConnection.getStats()).forEach(r => {
        if (['inbound-rtp', 'outbound-rtp'].includes(r.type) && r.kind === 'audio') media.push({ type: r.type,
          packetsReceived: r.packetsReceived, packetsSent: r.packetsSent, totalAudioEnergy: r.totalAudioEnergy });
      });
      return { events: voiceEvents, media };
    });
    const events = result.events, running = events.filter(e => e.event === 'tool_status' && e.status === 'running');
    assert.ok(!events.some(e => e.event === 'action_result' && e.fallback), 'Planner used error fallback');
    assert.ok(!events.some(e => e.event === 'intent_result' && e.fallback), 'Intent used error fallback');
    if (mode === 'merge') {
      assert.deepEqual(running.map(e => e.tool_call.name), ['water', 'plant']);
      const merged = events.find(e => e.event === 'input_merge' && e.status === 'committed' && e.source_utterance_ids.length >= 2 && /等一下.*再去种地/.test(e.text));
      assert.ok(merged, 'Provider finals did not form the expected merged request');
      assert.ok(!events.some(e => e.event === 'intent_result' && e.interrupt), 'Split wait interrupted the old task');
      assert.equal(events.filter(e => e.event === 'intent_result').length, 1, 'Classified raw fragments instead of merged input');
    } else if (mode === 'cancel') {
      assert.deepEqual(running.map(e => e.tool_call.name), ['plant', 'harvest']);
      const cancelled = events.find(e => e.event === 'plan_status' && e.status === 'cancelled');
      assert.ok(cancelled && cancelled.steps.length === 3 && cancelled.steps.every(step => step.status === 'cancelled'));
      assert.ok(events.some(e => e.event === 'response_cancelled' && e.response_epoch === initialEpoch && e.queue_dropped > 0));
      assert.ok(!events.some(e => e.event === 'input_dispatched' && e.utterance_id === bufferedID), 'Cancelled cache revived');
      assert.ok(events.some(e => e.event === 'intent_result' && e.interrupt === true && !e.fallback));
    } else {
      assert.deepEqual(running.map(e => e.tool_call.name), ['plant', 'water', 'fertilize', 'general_qa']);
      assert.equal(new Set(running.slice(0, 3).map(e => e.plan_id)).size, 1);
      assert.equal(new Set(running.map(e => e.tool_call.id)).size, 4);
      assert.equal(events.filter(e => e.event === 'action_result').length, 2, 'Replanned each step');
      const completed = events.findIndex(e => e.event === 'plan_status' && e.status === 'completed');
      const dispatched = events.findIndex(e => e.event === 'input_dispatched' && e.utterance_id === bufferedID);
      assert.ok(completed >= 0 && dispatched > completed, 'Cache ran before whole plan finished');
    }
    if (mode !== 'cancel') {
      for (let i = 1; i < running.length; i++) {
        const previous = events.findIndex(e => e.event === 'tool_status' && e.response_epoch === running[i - 1].response_epoch && e.status === 'completed');
        assert.ok(previous >= 0 && events.indexOf(running[i]) > previous, 'Next tool started before previous playback completed');
      }
      assert.ok(!events.some(e => e.event === 'response_cancelled'));
    }
    const cancelledEpochs = new Set();
    for (const e of events) {
      if (e.event === 'response_cancelled') cancelledEpochs.add(e.response_epoch);
      else assert.ok(!(cancelledEpochs.has(e.response_epoch) && ['speaking', 'completed'].includes(e.status)), 'Old epoch resumed');
    }
    for (const e of running.filter(e => e.tool_call.name !== 'general_qa' && !(mode === 'cancel' && e.response_epoch === initialEpoch))) {
      const text = events.filter(item => item.event === 'response_text' && item.response_epoch === e.response_epoch).at(-1)?.text;
      assert.equal((text?.match(/我正在(?:种菜|浇水|施肥|收菜)。/g) || []).length, 10);
    }
    assert.ok(result.media.some(r => r.type === 'inbound-rtp' && r.totalAudioEnergy > 0));
    assert.ok(result.media.some(r => r.type === 'outbound-rtp' && r.packetsSent > 0));
    assert.deepEqual(errors, []);
    await page.screenshot({ path: path.join(directory, `${name}-desktop.png`), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.screenshot({ path: path.join(directory, `${name}-mobile.png`), fullPage: true });
    save({ recorded_at: new Date().toISOString(), passed: true, mode, input: 'Fixed speech through WebAudio microphone, real cloud ASR/planner/TTS, physical audio muted', ...result });
    console.log(JSON.stringify({ passed: true, mode, tools: running.map(e => e.tool_call.name), merged_inputs: events.filter(e => e.event === 'input_merge' && e.status === 'committed').map(e => ({ text: e.text, sources: e.source_utterance_ids.length })) }));
    await page.locator('#disconnect').click();
    await page.evaluate(() => testMic.context.close());
  } catch (error) {
    const events = await page.evaluate(() => window.voiceEvents || []).catch(() => []);
    save({ recorded_at: new Date().toISOString(), passed: false, mode, error: error.message, events, page_errors: errors });
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
