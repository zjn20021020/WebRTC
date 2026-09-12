const { chromium } = require('playwright');
const fs = require('node:fs');
const assert = require('node:assert/strict');

// Only the microphone is substituted. Signaling, ASR, both classifiers, TTS
// and cancellation run through the application and real cloud services.
(async () => {
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
      if (!['home-water.wav', 'home-fertilize.wav', 'home-praise.wav'].includes(name)) return route.abort();
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
    await page.evaluate(() => playFixture('home-water'));
    await page.waitForFunction(() => voiceEvents.some(e => e.tool_call?.name === 'water' && e.status === 'running')
      && voiceEvents.some(e => e.status === 'speaking'), null, { timeout: 20000 });
    const waterEpoch = await page.evaluate(() => responseEpoch);
    await page.evaluate(() => playFixture('home-fertilize'));
    await page.waitForFunction(() => voiceEvents.some(e => e.tool_call?.name === 'fertilize' && e.status === 'running')
      && document.querySelector('#replyStatus').dataset.state === 'speaking', null, { timeout: 20000 });
    const fertilizerEpoch = await page.evaluate(() => responseEpoch);
    await page.evaluate(() => playFixture('home-praise'));
    await page.waitForFunction(epoch => voiceEvents.some(e => e.event === 'input_buffered' && e.response_epoch === epoch), fertilizerEpoch, { timeout: 15000 });
    assert.equal(await page.evaluate(() => responseEpoch), fertilizerEpoch);
    await page.screenshot({ path: 'bin/home-buffered-desktop.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: 'bin/home-buffered-mobile.png', fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.waitForFunction(() => voiceEvents.some(e => e.event === 'tool_status' && e.tool_call?.name === 'affection' && e.status === 'completed'), null, { timeout: 90000 });
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
    const index = predicate => events.findIndex(predicate);
    const approved = index(e => e.event === 'intent_result' && e.interrupt === true && e.response_epoch === waterEpoch && !e.fallback);
    const cancelled = index(e => e.event === 'response_cancelled' && e.response_epoch === waterEpoch);
    assert.ok(approved >= 0 && cancelled > approved);
    assert.ok(events[cancelled].queue_dropped > 0);
    assert.ok(events.some(e => e.event === 'tool_status' && e.tool_call?.name === 'water' && e.status === 'cancelled'));
    assert.ok(!events.slice(cancelled + 1).some(e => e.response_epoch === waterEpoch && ['speaking', 'completed'].includes(e.status)));
    assert.ok(events.some(e => e.event === 'intent_result' && e.interrupt === false && e.response_epoch === fertilizerEpoch && !e.fallback));
    assert.ok(!events.some(e => e.event === 'response_cancelled' && e.response_epoch === fertilizerEpoch));
    const completed = index(e => e.event === 'tool_status' && e.tool_call?.name === 'fertilize' && e.status === 'completed');
    const affection = index(e => e.event === 'tool_status' && e.tool_call?.name === 'affection' && e.status === 'running');
    assert.ok(completed >= 0 && affection > completed);
    const fertilizerReply = events.filter(e => e.event === 'response_text' && e.response_epoch === fertilizerEpoch).at(-1).text;
    assert.equal((fertilizerReply.match(/我正在施肥。/g) || []).length, 10);
    assert.equal((result.reply.match(/贴贴。/g) || []).length, 10);
    for (const epoch of [waterEpoch, fertilizerEpoch]) assert.ok(events.some(e => e.status === 'ducking' && e.response_epoch === epoch));
    assert.ok(result.media.some(r => r.type === 'inbound-rtp' && r.totalAudioEnergy > 0));
    assert.ok(result.media.some(r => r.type === 'outbound-rtp' && r.packetsSent > 0));
    assert.deepEqual(errors, []);
    const evidence = { recorded_at: new Date().toISOString(), passed: true, providers: ['Tencent ASR 8k_zh', 'deepseek-v4-pro', 'Tencent TextToStreamAudioWS'],
      input: 'Pre-generated speech through WebAudio virtual microphone; physical output muted', duck_gain: 0.5, water_epoch: waterEpoch, fertilizer_epoch: fertilizerEpoch, ...result };
    fs.mkdirSync('docs/evidence', { recursive: true });
    fs.writeFileSync('docs/evidence/home-voice.json', `${JSON.stringify(evidence, null, 2)}\n`);
    console.log(JSON.stringify({ passed: true, tools: events.filter(e => e.event === 'tool_status'), decisions: events.filter(e => e.event === 'intent_result'), media: result.media }));
    await page.locator('#disconnect').click();
    await page.evaluate(() => testMic.context.close());
  } catch (error) {
    console.error(await page.evaluate(() => window.voiceEvents?.filter(e => e.event !== 'response_text')).catch(() => []));
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
