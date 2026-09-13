const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

// Synthetic microphone audio traverses real WebRTC, ASR, planning and TTS.
// The content checks catch postponement regressions; story quality still needs review.
(async () => {
  const interrupt = process.argv.includes('--interrupt');
  const amendment = process.argv.includes('--amendment'), independent = process.argv.includes('--independent');
  const pair = amendment || independent;
  const name = amendment ? 'story-amendment' : independent ? 'story-independent' : interrupt ? 'story-interrupted' : 'story-deferred';
  const directory = process.env.EVIDENCE_DIR || 'bin/story-evidence';
  fs.mkdirSync(directory, { recursive: true });
  const save = result => fs.writeFileSync(path.join(directory, `${name}.json`), `${JSON.stringify(result, null, 2)}\n`);
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'] });
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  try {
    const fixtures = ['home-water', 'home-story', 'home-stop-story', 'home-story-theme', 'home-question'];
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
      window.mediaStats = async () => {
        const media = [];
        (await activeConnection.peerConnection.getStats()).forEach(r => {
          if (['inbound-rtp', 'outbound-rtp'].includes(r.type) && r.kind === 'audio') media.push({ type: r.type,
            packetsReceived: r.packetsReceived, packetsSent: r.packetsSent, totalAudioEnergy: r.totalAudioEnergy });
        });
        return media;
      };
    });
    await page.locator('#connect').click();
    await page.waitForFunction(() => document.querySelector('#asrStatus').dataset.state === 'listening', null, { timeout: 15000 });
    await page.evaluate(() => playFixture('home-water'));
    await page.waitForFunction(() => voiceEvents.some(e => e.event === 'response_status' && e.status === 'speaking'), null, { timeout: 25000 });
    assert.equal(await page.evaluate(() => voiceEvents.find(e => e.event === 'tool_status' && e.status === 'running')?.tool_call.name), 'water');
    await page.evaluate(name => playFixture(name), interrupt ? 'home-stop-story' : 'home-story');
    if (pair) {
      await page.waitForFunction(() => voiceEvents.some(e => e.event === 'input_buffered' && /讲.*故事/.test(e.text)), null, { timeout: 12000 });
      // Deliberately exceed the ASR collector's time and audio-gap windows.
      await page.waitForTimeout(3000);
      await page.evaluate(name => playFixture(name), amendment ? 'home-story-theme' : 'home-question');
      await page.waitForFunction(() => voiceEvents.filter(e => e.event === 'input_buffered').length === 2, null, { timeout: 12000 });
    }
    await page.waitForFunction(() => voiceEvents.some(e => e.event === 'tool_status' && e.tool_call?.name === 'general_qa' && e.status === 'running'), null, { timeout: 45000 });
    const beforeStory = await page.evaluate(() => mediaStats());
    await page.waitForFunction(() => voiceEvents.some(e => e.event === 'tool_status' && e.tool_call?.name === 'general_qa' && e.status === 'completed'), null, { timeout: 120000 });
    if (independent) await page.waitForFunction(() => voiceEvents.filter(e => e.event === 'tool_status' && e.tool_call?.name === 'general_qa' && e.status === 'completed').length === 2, null, { timeout: 45000 });
    const result = await page.evaluate(async () => ({ events: voiceEvents, media: await mediaStats() }));
    const events = result.events, running = events.filter(e => e.event === 'tool_status' && e.status === 'running');
    assert.deepEqual(running.map(e => e.tool_call.name), independent ? ['water', 'general_qa', 'general_qa'] : ['water', 'general_qa'], 'Old farming work restarted or story misrouted');
    assert.ok(!events.some(e => ['action_result', 'intent_result', 'input_relation'].includes(e.event) && e.fallback), 'Cloud request used fallback');
    const decisions = events.filter(e => e.event === 'intent_result');
    assert.equal(decisions.length, pair ? 2 : 1);
    assert.ok(decisions.every(e => e.interrupt === interrupt), 'Ordinary story must wait; explicit stopping must interrupt');
    const waterEpoch = running[0].response_epoch, storyEpoch = running[1].response_epoch;
    const merged = events.find(e => e.event === 'input_merge' && e.status === 'committed' && /讲.*故事/.test(e.text));
    assert.ok(merged, 'Expected story request was not recognized');
    assert.equal(/先别.*浇水/.test(merged.text), interrupt, 'ASR lost or invented explicit cancellation');
    const context = events.find(e => e.event === 'response_context' && e.response_epoch === storyEpoch);
    assert.ok(context, 'Missing evidence of actual model context');
    const state = JSON.parse(context.detail.slice(context.detail.lastIndexOf('\n') + 1));
    assert.equal(state.current_action, 'general_qa');
    assert.equal(state.previous_responses.find(e => e.response_epoch === waterEpoch)?.status, interrupt ? 'cancelled' : 'completed');
    if (pair) {
      const relations = events.filter(e => e.event === 'input_relation' && e.continuation !== undefined);
      assert.equal(relations.length, 1);
      assert.equal(relations[0].continuation, amendment);
      const originalInputs = events.filter(e => e.event === 'input_merge' && e.status === 'committed' && e.reason !== 'queued_continuation');
      assert.equal(originalInputs.length, 3, 'Test did not produce three separately committed ASR inputs');
      assert.ok(Date.parse(originalInputs[2].at) - Date.parse(originalInputs[1].at) >= 3000);
      assert.ok(events.findIndex(e => e.event === 'tool_status' && e.tool_call?.name === 'water' && e.status === 'completed') > events.indexOf(decisions[1]), 'Supplement arrived after story dispatch');
      if (amendment) {
        const joined = events.find(e => e.event === 'input_merge' && e.reason === 'queued_continuation');
        assert.ok(joined && joined.source_utterance_ids.length === 2 && /讲.*故事/.test(joined.text) && /夜晚.*月亮/.test(joined.text), 'Original request and constraint were not joined');
        assert.ok(/讲.*故事/.test(context.text) && /月亮/.test(context.text) && /夜晚|夜间|晚上/.test(context.text), 'Planner dropped the qualifier from the actual answer-model input');
        assert.ok(!events.some(e => e.event === 'input_dispatched' && e.utterance_id === originalInputs[2].utterance_id), 'Qualifier was independently dispatched');
        assert.equal(events.filter(e => e.event === 'action_result').length, 2, 'Qualifier caused another planner invocation');
        assert.ok(!events.some(e => e.response_epoch > storyEpoch), 'Second response epoch was reserved');
        assert.match(await page.locator('#mergedInput').textContent(), /2 段合并.*月亮/);
      } else {
        assert.ok(!events.some(e => e.event === 'input_merge' && e.reason === 'queued_continuation'), 'Independent request was merged');
        assert.equal(events.filter(e => e.event === 'input_dispatched').length, 3);
        const secondAnswer = events.filter(e => e.event === 'response_text' && e.response_epoch === running[2].response_epoch).at(-1)?.text;
        assert.match(secondAnswer, /二|2/, 'Independent question was never answered');
      }
      assert.equal(events.filter(e => e.event === 'input_queue').at(-1)?.queue_size, 0, 'A duplicate request remains queued');
    }
    const waterTerminal = events.findIndex(e => e.event === 'tool_status' && e.response_epoch === waterEpoch && e.status === (interrupt ? 'cancelled' : 'completed'));
    assert.ok(waterTerminal >= 0 && waterTerminal < events.indexOf(running[1]), 'Story ran before old water lifecycle ended');
    if (interrupt) {
      assert.ok(events.some(e => e.event === 'response_cancelled' && e.response_epoch === waterEpoch && e.queue_dropped > 0));
      assert.ok(!events.some(e => e.response_epoch === waterEpoch && e.status === 'completed'), 'Cancelled water revived');
    } else {
      assert.ok(!events.some(e => e.event === 'response_cancelled'));
      const buffered = events.find(e => e.event === 'input_buffered' && e.utterance_id === merged.utterance_id);
      assert.ok(buffered, 'Story skipped the queue');
      const dispatched = events.findIndex(e => e.event === 'input_dispatched' && e.utterance_id === buffered.utterance_id);
      assert.ok(dispatched > waterTerminal, 'Story dispatched before water completed');
      const waterText = events.filter(e => e.event === 'response_text' && e.response_epoch === waterEpoch).at(-1)?.text;
      assert.equal((waterText?.match(/我正在浇水。/g) || []).length, 10);
    }
    const answer = events.filter(e => e.event === 'response_text' && e.response_epoch === storyEpoch).at(-1)?.text || '';
    if (amendment) assert.ok(/月亮|月光/.test(answer) && /夜|晚/.test(answer), 'Story ignored the requested topic');
    assert.ok(answer.length >= 20, `Empty reply or short acknowledgement instead of a story: ${answer}`);
    assert.ok(!/(?:我(?:先|正|还|要|现在|这就|马上|待会|一会|去)){1,4}[^。！？]{0,12}(?:浇水|种菜|施肥|收菜)|(?:等你|等我|等忙完|等会儿?|稍后|待会儿?|以后|回头)[^。！？]{0,30}(?:再讲|再说|给你讲)|Server execution|previous_responses/.test(answer), `Stale scheduling or runtime instructions leaked into story: ${answer}`);
    assert.ok(result.media.some(r => r.type === 'inbound-rtp' && r.totalAudioEnergy > (beforeStory.find(b => b.type === r.type)?.totalAudioEnergy || 0)), 'No new downstream audio during story');
    assert.ok(result.media.some(r => r.type === 'outbound-rtp' && r.packetsSent > 0));
    assert.deepEqual(errors, []);
    save({ recorded_at: new Date().toISOString(), passed: true, mode: name, answer,
      content_check: 'Minimum 20 characters to exclude short acknowledgements; no stale farming restart, postponement promise or execution-state leak. Narrative quality requires human review.',
      input: 'Fixed speech through WebAudio microphone, real cloud ASR/planner/TTS, physical audio muted', before_story_media: beforeStory, ...result });
    console.log(JSON.stringify({ passed: true, mode: name, interrupt: decisions[0].interrupt, answer }));
    await page.locator('#disconnect').click();
    await page.evaluate(() => testMic.context.close());
  } catch (error) {
    const events = await page.evaluate(() => window.voiceEvents || []).catch(() => []);
    save({ recorded_at: new Date().toISOString(), passed: false, mode: name, error: error.message, events, page_errors: errors });
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
