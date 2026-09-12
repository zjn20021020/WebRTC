const { chromium } = require('playwright');
const fs = require('node:fs');
const assert = require('node:assert/strict');

// Only the microphone is substituted. Signaling, ASR, both classifiers, TTS
// and cancellation run through the application and real cloud services.
(async () => {
  const waitScenario = process.argv.includes('--wait');
  const replacementScenario = process.argv.includes('--replacement');
  const praiseScenario = process.argv.includes('--praise');
  const clearBufferScenario = process.argv.includes('--clear-buffer');
  const switchScenario = process.argv.includes('--switch') || clearBufferScenario;
  assert.ok([waitScenario, replacementScenario, praiseScenario, switchScenario].filter(Boolean).length <= 1, 'Select one scenario');
  const evidenceName = clearBufferScenario ? 'home-clear-buffer' : waitScenario ? 'home-wait' : replacementScenario ? 'home-replacement' : praiseScenario ? 'home-praise' : switchScenario ? 'home-switch' : 'home';
  const initialTool = praiseScenario || switchScenario ? 'plant' : 'water';
  const lastTool = waitScenario ? 'plant' : switchScenario ? 'general_qa' : 'affection';
  const browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'],
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  try {
    await page.route('**/fixture-home-*.wav', route => {
      const name = new URL(route.request().url()).pathname.replace('/fixture-', '');
      if (!['home-water.wav', 'home-fertilize.wav', 'home-praise.wav', 'home-water-direct.wav', 'home-wait-plant.wav', 'home-replace-fertilize.wav', 'home-plant.wav', 'home-good-job.wav', 'home-plant-direct.wav', 'home-fertilize-direct.wav', 'home-question.wav'].includes(name)) return route.abort();
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
    await page.evaluate(name => playFixture(name), switchScenario ? 'home-plant-direct' : praiseScenario ? 'home-plant' : waitScenario || replacementScenario ? 'home-water-direct' : 'home-water');
    await page.waitForFunction(name => voiceEvents.some(e => e.tool_call?.name === name && e.status === 'running')
      && voiceEvents.some(e => e.status === 'speaking'), initialTool, { timeout: 20000 });
    const initialEpoch = await page.evaluate(() => responseEpoch);
    let discardedInputID;
    if (clearBufferScenario) {
      await page.evaluate(() => playFixture('home-question'));
      await page.waitForFunction(epoch => voiceEvents.some(e => e.event === 'input_buffered' && e.response_epoch === epoch), initialEpoch, { timeout: 15000 });
      discardedInputID = await page.evaluate(epoch => voiceEvents.find(e => e.event === 'input_buffered' && e.response_epoch === epoch).utterance_id, initialEpoch);
      assert.equal(await page.evaluate(() => responseEpoch), initialEpoch);
    }
    let fertilizerEpoch;
    if (waitScenario || praiseScenario) {
      await page.evaluate(name => playFixture(name), praiseScenario ? 'home-good-job' : 'home-wait-plant');
    } else {
      await page.evaluate(name => playFixture(name), switchScenario ? 'home-fertilize-direct' : replacementScenario ? 'home-replace-fertilize' : 'home-fertilize');
      await page.waitForFunction(() => voiceEvents.some(e => e.tool_call?.name === 'fertilize' && e.status === 'running')
        && document.querySelector('#replyStatus').dataset.state === 'speaking', null, { timeout: 20000 });
      fertilizerEpoch = await page.evaluate(() => responseEpoch);
      await page.evaluate(name => playFixture(name), switchScenario ? 'home-question' : 'home-praise');
    }
    const preservedEpoch = waitScenario || praiseScenario ? initialEpoch : fertilizerEpoch;
    await page.waitForFunction(epoch => voiceEvents.some(e => e.event === 'input_buffered' && e.response_epoch === epoch), preservedEpoch, { timeout: 15000 });
    assert.equal(await page.evaluate(() => responseEpoch), preservedEpoch);
    await page.screenshot({ path: `bin/${evidenceName}-buffered-desktop.png`, fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: `bin/${evidenceName}-buffered-mobile.png`, fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.waitForFunction(name => voiceEvents.some(e => e.event === 'tool_status' && e.tool_call?.name === name && e.status === 'completed'), lastTool, { timeout: 90000 });
    const result = await page.evaluate(async () => {
      const reports = await activeConnection.peerConnection.getStats();
      const media = [];
      reports.forEach(r => {
        if (['inbound-rtp', 'outbound-rtp'].includes(r.type) && r.kind === 'audio') media.push({
          type: r.type, packetsReceived: r.packetsReceived, packetsSent: r.packetsSent,
          totalAudioEnergy: r.totalAudioEnergy, bytesReceived: r.bytesReceived, bytesSent: r.bytesSent,
        });
      });
      return { events: voiceEvents, media, reply: document.querySelector('#replyText').textContent };
    });
    const events = result.events;
    if (replacementScenario) assert.ok(events.some(e => e.event === 'asr_final' && /先别浇水.*施肥/.test(e.text)), 'Expected the reported replacement request in ASR final');
    if (switchScenario) {
      assert.ok(events.some(e => e.event === 'asr_final' && /^去施肥[。.!！]?$/.test(e.text.trim())), 'Expected a direct action without stop wording');
      assert.ok(!events.some(e => e.event === 'action_result' && e.fallback), 'Switch classification failed');
    }
    const index = predicate => events.findIndex(predicate);
    if (clearBufferScenario) {
      const approved = index(e => e.event === 'intent_result' && e.interrupt === true && e.response_epoch === initialEpoch);
      const nextTurn = index(e => e.event === 'turn_transition' && e.previous_epoch === initialEpoch);
      assert.ok(approved >= 0 && nextTurn > approved);
      assert.ok(events.slice(approved + 1, nextTurn).some(e => e.event === 'input_queue' && e.queue_size === 0), 'Confirmed interruption did not clear the old text queue');
      assert.ok(!events.some(e => e.event === 'input_dispatched' && e.utterance_id === discardedInputID), 'Discarded question was answered after interruption');
      assert.equal(events.filter(e => e.event === 'tool_status' && e.tool_call?.name === 'general_qa' && e.status === 'running').length, 1);
      assert.ok(events.some(e => e.event === 'input_dispatched' && e.utterance_id !== discardedInputID), 'Fresh post-interruption question was not answered');
    }
    if (!waitScenario && !praiseScenario) {
      const approved = index(e => e.event === 'intent_result' && e.interrupt === true && e.response_epoch === initialEpoch && !e.fallback);
      const cancelled = index(e => e.event === 'response_cancelled' && e.response_epoch === initialEpoch);
      assert.ok(approved >= 0 && cancelled > approved);
      assert.ok(events[cancelled].queue_dropped > 0);
      assert.ok(events.some(e => e.event === 'tool_status' && e.tool_call?.name === initialTool && e.status === 'cancelled'));
      assert.ok(!events.slice(cancelled + 1).some(e => e.response_epoch === initialEpoch && ['speaking', 'completed'].includes(e.status)));
    } else if (waitScenario) {
      assert.ok(events.some(e => e.event === 'asr_final' && /等.*再.*种地/.test(e.text)), 'Expected the reported deferred request to reach ASR final');
      assert.ok(!events.some(e => e.event === 'intent_result' && e.interrupt === true && e.response_epoch === initialEpoch), 'A wait prefix authorized a stop');
    } else {
      assert.equal(events.filter(e => e.event === 'asr_final' && /干[得的]不错/.test(e.text)).length, 1, 'Praise must succeed on its first utterance');
      assert.ok(!events.some(e => e.event === 'action_result' && e.fallback), 'Praise hit the task-unavailable fallback');
      assert.ok(!events.some(e => e.event === 'action_retry'), 'Strict-schema regression should succeed without repair');
    }
    assert.ok(events.some(e => e.event === 'intent_result' && e.interrupt === false && e.response_epoch === preservedEpoch && !e.fallback));
    assert.ok(!events.some(e => e.event === 'response_cancelled' && e.response_epoch === preservedEpoch));
    const completed = index(e => e.event === 'tool_status' && e.tool_call?.name === (waitScenario ? 'water' : praiseScenario ? 'plant' : 'fertilize') && e.status === 'completed');
    const next = index(e => e.event === 'tool_status' && e.tool_call?.name === lastTool && e.status === 'running');
    assert.ok(completed >= 0 && next > completed);
    const preservedReply = events.filter(e => e.event === 'response_text' && e.response_epoch === preservedEpoch).at(-1).text;
    assert.equal((preservedReply.match(waitScenario ? /我正在浇水。/g : praiseScenario ? /我正在种菜。/g : /我正在施肥。/g) || []).length, 10);
    if (switchScenario) assert.match(result.reply, /2|二|两/, 'The deferred general question was not answered');
    else assert.equal((result.reply.match(waitScenario ? /我正在种菜。/g : /贴贴。/g) || []).length, 10);
    for (const epoch of new Set([initialEpoch, preservedEpoch])) assert.ok(events.some(e => e.status === 'ducking' && e.response_epoch === epoch));
    assert.ok(result.media.some(r => r.type === 'inbound-rtp' && r.totalAudioEnergy > 0));
    assert.ok(result.media.some(r => r.type === 'outbound-rtp' && r.packetsSent > 0));
    assert.deepEqual(errors, []);
    const evidence = { recorded_at: new Date().toISOString(), passed: true, providers: ['Tencent ASR 8k_zh', 'deepseek-v4-pro', 'Tencent TextToStreamAudioWS'],
      input: 'Pre-generated speech through WebAudio virtual microphone; physical output muted', duck_gain: 0.5, initial_tool: initialTool, initial_epoch: initialEpoch, water_epoch: initialTool === 'water' ? initialEpoch : undefined, fertilizer_epoch: fertilizerEpoch, discarded_input_id: discardedInputID, ...result };
    fs.mkdirSync('docs/evidence', { recursive: true });
    fs.writeFileSync(`docs/evidence/${evidenceName}-voice.json`, `${JSON.stringify(evidence, null, 2)}\n`);
    console.log(JSON.stringify({ passed: true, tools: events.filter(e => e.event === 'tool_status'), decisions: events.filter(e => e.event === 'intent_result'), media: result.media }));
    await page.locator('#disconnect').click();
    await page.evaluate(() => testMic.context.close());
  } catch (error) {
    console.error(await page.evaluate(() => window.voiceEvents?.filter(e => e.event !== 'response_text')).catch(() => []));
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
