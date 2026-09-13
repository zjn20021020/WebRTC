const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`); });
  try {
    await page.goto(process.env.DEMO_URL || 'http://localhost:8080');
    await page.evaluate(() => document.fonts.ready);
    const ids = ['status', 'connect', 'disconnect', 'meter', 'waveform', 'remoteAudio', 'asrStatus', 'asrError', 'finalTranscript', 'partialTranscript', 'turnLabel', 'replyStatus', 'replyText', 'replyError', 'toolStatus', 'interruptionStatus', 'queuedStatus', 'stopResponse', 'latencyText', 'latencyAudio', 'latencyDuck', 'log'];
    for (const id of ids) assert.equal(await page.locator(`#${id}`).count(), 1, `Missing or duplicated original control: ${id}`);
    assert.equal(await page.locator('#connect').isEnabled(), true);
    assert.equal(await page.locator('#disconnect').isEnabled(), false);
    assert.equal(await page.locator('#stopResponse').isEnabled(), false);
    await page.evaluate(() => {
      handleControl(JSON.stringify({ event: 'response_status', response_epoch: 0, status: 'ready' }));
      handleControl(JSON.stringify({ event: 'input_merge', response_epoch: 0, status: 'collecting', text: '先浇水', source_utterance_ids: ['s:1'] }));
    });
    assert.equal(await page.locator('#stopResponse').isEnabled(), true, 'Cannot cancel a pending initial input');
    await page.evaluate(() => handleControl(JSON.stringify({ event: 'input_merge', response_epoch: 0, status: 'discarded', text: '先浇水' })));
    assert.equal(await page.locator('#stopResponse').isEnabled(), false);
    assert.equal(await page.locator('#remoteAudio').getAttribute('controls'), '');
    assert.ok(await page.evaluate(() => [...document.images].every(image => image.complete && image.naturalWidth > 0)), 'Missing image asset');
    assert.ok(await page.evaluate(async () => {
      const image = new Image(); image.src = '/assets/home-landscape.webp'; await image.decode(); return image.naturalWidth > 1000;
    }));
    assert.ok(await page.evaluate(() => {
      const canvas = document.querySelector('#waveform');
      const pixels = canvas.getContext('2d').getImageData(0, 0, canvas.width, canvas.height).data;
      return pixels.some((value, index) => index % 4 === 3 && value > 0);
    }), 'Blank idle waveform');
    fs.mkdirSync('bin/ui', { recursive: true });
    await page.screenshot({ path: 'bin/ui/home-desktop-idle.png', fullPage: true });
    await page.evaluate(() => {
      statusElement.textContent = 'connected'; statusElement.dataset.state = 'connected';
      connectButton.disabled = true; disconnectButton.disabled = false;
      const send = message => handleControl(JSON.stringify(message));
      send({ event: 'asr_status', status: 'listening' });
      ['去种菜。', '干得不错。', '去施肥。', '一加一等于几？'].forEach((text, i) => send({ event: 'asr_final', utterance_id: `fixture-${i}`, text }));
      send({ event: 'response_status', response_epoch: 2, status: 'speaking' });
      send({ event: 'response_text', response_epoch: 2, text: '我正在施肥。'.repeat(10) });
      send({ event: 'tool_status', response_epoch: 2, status: 'running', tool_call: { name: 'fertilize' } });
      send({ event: 'input_merge', response_epoch: 2, status: 'committed', text: '先种菜。 再浇水，最后施肥。', source_utterance_ids: ['s:1', 's:2'] });
      send({ event: 'plan_status', response_epoch: 2, plan_id: 'ui-plan', status: 'running', step_index: 2, step_count: 3,
        steps: [{ action: 'plant', text: '种菜', status: 'completed' }, { action: 'water', text: '浇水', status: 'running' }, { action: 'fertilize', text: '施肥', status: 'pending' }] });
      send({ event: 'input_queue', response_epoch: 2, queue_size: 1 });
      send({ event: 'intent_result', response_epoch: 2, interrupt: false });
      send({ event: 'response_metrics', response_epoch: 2, metrics: { speech_end_to_first_text_ms: 986, speech_end_to_first_audio_ms: 1640, speech_to_duck_ms: 128 } });
      meterElement.textContent = '麦克风 RMS: 12.8%';
      logElement.textContent = '2026-09-12T08:10:33.619Z intent_result interrupt=true\n2026-09-12T08:10:33.752Z input_queue cleared=true\n2026-09-12T08:10:34.621Z tool_status fertilize running\n2026-09-12T08:10:37.599Z input_buffered queue_size=1';
    });
    for (const width of [1440, 1024, 768, 390, 320]) {
      await page.setViewportSize({ width, height: width >= 768 ? 1000 : 844 });
      await page.screenshot({ path: `bin/ui/home-active-${width}.png`, fullPage: true });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `Overflow at ${width}`);
      assert.ok(await page.evaluate(() => {
        const selectors = ['#connect', '#disconnect', '#stopResponse', '#transcriptHeading', '#replyHeading', '#latencyHeading', '#meter', '#status'];
        return selectors.every(selector => {
          const element = document.querySelector(selector), bounds = element.getBoundingClientRect();
          return bounds.left >= 0 && bounds.right <= innerWidth && element.scrollWidth <= element.clientWidth + 1;
        });
      }), `Clipped labels at ${width}`);
      await page.evaluate(() => {
        handleControl(JSON.stringify({ event: 'asr_error', status: 'quota_exhausted', code: 4004, model: '8k_zh', detail: '腾讯 ASR 4004（8k_zh）：当前引擎没有可用识别额度。普通实时识别、大模型 1.0 和 2.0 资源包分别计费，请核对资源包与引擎是否匹配。' }));
        setReplyStatus('ducking');
        replyError.hidden = false;
        replyError.textContent = '识别连接失败，请重新连接。'.repeat(12);
        partialTranscript.textContent = '等种完菜以后再去施肥，然后回答我刚刚的问题。'.repeat(4);
        replyText.textContent = '这是一条用于检查长文本换行的测试回答。'.repeat(40);
        handlePlan({ response_epoch: 2, plan_id: 'ui-plan', status: 'running', step_index: 2, step_count: 6,
          steps: Array.from({ length: 6 }, (_, i) => ({ action: 'general_qa', text: '<script>长文本</script>'.repeat(10), status: i === 1 ? 'running' : 'pending' })) });
      });
      assert.equal(await page.locator('#asrStatus').textContent(), '识别额度不可用');
      assert.equal(await page.locator('#asrError').isVisible(), true);
      assert.match(await page.locator('#asrError').textContent(), /4004.*8k_zh/);
      assert.match(await page.locator('#log').textContent(), /识别错误:.*4004/);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `Long text overflow at ${width}`);
      assert.ok(await page.evaluate(() => {
        const reply = document.querySelector('.reply').getBoundingClientRect();
        const stop = document.querySelector('#stopResponse').getBoundingClientRect();
        const latency = document.querySelector('.latency').getBoundingClientRect();
        return stop.bottom <= reply.bottom && reply.bottom <= latency.top + 1;
      }), `Overlapping content at ${width}`);
      await page.screenshot({ path: `bin/ui/home-long-${width}.png`, fullPage: true });
      await page.evaluate(() => {
        handleControl(JSON.stringify({ event: 'asr_status', status: 'listening' }));
        replyError.hidden = true; partialTranscript.textContent = '';
        replyText.textContent = '我正在施肥。'.repeat(10); setReplyStatus('speaking');
      });
      assert.equal(await page.locator('#asrError').isVisible(), false);
    }
    assert.equal(await page.locator('.dimo').evaluate(element => getComputedStyle(element).animationName), 'none');
    assert.equal(await page.locator('#planSteps script').count(), 0, 'Plan text became markup');
    await page.evaluate(() => {
      handleControl(JSON.stringify({ event: 'response_status', response_epoch: 2, status: 'completed' }));
      handleControl(JSON.stringify({ event: 'plan_status', response_epoch: 2, plan_id: 'ui-plan', status: 'completed', step_index: 1, step_count: 1,
        steps: [{ action: 'water', text: '浇水', status: 'completed' }] }));
    });
    assert.match(await page.locator('#planStatus').textContent(), /已结束/);
    await page.evaluate(() => {
      handleControl(JSON.stringify({ event: 'response_status', response_epoch: 3, status: 'thinking' }));
      handleControl(JSON.stringify({ event: 'action_result', response_epoch: 3, tool_call: { id: 'new', name: 'plant' } }));
      handleControl(JSON.stringify({ event: 'plan_status', response_epoch: 2, plan_id: 'stale', steps: [] }));
    });
    assert.equal(await page.locator('#taskPlan').isVisible(), false, 'Old plan revived after a new request');
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ passed: true, viewports: [1440, 1024, 768, 390, 320], controls: ids.length, checks: ['local assets', 'original controls', 'idle canvas pixels', 'long text', 'error state', 'reduced motion', 'no overflow or overlap'], screenshots: 'bin/ui' }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
