const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

(async () => {
  const folder = 'bin/audio-routing';
  fs.mkdirSync(folder, { recursive: true });
  const samples = 48000 * 8, wav = Buffer.alloc(44 + samples * 2);
  wav.write('RIFF', 0); wav.writeUInt32LE(wav.length - 8, 4); wav.write('WAVEfmt ', 8);
  wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22);
  wav.writeUInt32LE(48000, 24); wav.writeUInt32LE(96000, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34);
  wav.write('data', 36); wav.writeUInt32LE(samples * 2, 40);
  for (let i = 0; i < samples; i++) wav.writeInt16LE(Math.round(4000 * Math.sin(2 * Math.PI * 397 * i / 48000)), 44 + i * 2);
  const fixture = path.resolve(folder, 'capture.wav');
  fs.writeFileSync(fixture, wav);
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio', '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', `--use-file-for-fake-audio-capture=${fixture}`] });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    page.setDefaultTimeout(15000);
    const errors = [], results = {};
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      window.outputName = 'Speakers (HECATE G2 GAMING HEADSET)';
      window.outputKind = 'speakers';
      const getUserMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
      navigator.mediaDevices.getUserMedia = options => {
        if (window.rejectAEC && options.audio?.echoCancellation === true) return Promise.reject(new DOMException('Test capture failure', 'NotReadableError'));
        return getUserMedia(options);
      };
      const enumerate = navigator.mediaDevices.enumerateDevices.bind(navigator.mediaDevices);
      navigator.mediaDevices.enumerateDevices = async () => [
        ...(await enumerate()).filter(device => device.kind !== 'audiooutput'),
        { kind: 'audiooutput', deviceId: 'default', label: `Default - ${window.outputName}` },
      ];
    });
    await page.route('**/api/audio-output', async route => {
      await route.fulfill({ json: await page.evaluate(() => ({ name: outputName, kind: outputKind, source: 'test' })) });
    });
    await page.route('**/api/offer', async route => {
      const answer = await page.evaluate(async offer => {
        const peer = window.testPeer = new RTCPeerConnection();
        const context = window.testRemoteContext = new AudioContext({ sampleRate: 48000 });
        await context.resume();
        const oscillator = context.createOscillator(), gain = context.createGain(), output = context.createMediaStreamDestination();
        oscillator.frequency.value = 659; gain.gain.value = 0.04;
        oscillator.connect(gain).connect(output); oscillator.start();
        peer.ontrack = event => {
          const analyser = window.testReceiver = context.createAnalyser();
          context.createMediaStreamSource(new MediaStream([event.track])).connect(analyser);
        };
        await peer.setRemoteDescription(offer);
        peer.addTrack(output.stream.getAudioTracks()[0], output.stream);
        await peer.setLocalDescription(await peer.createAnswer());
        if (peer.iceGatheringState !== 'complete') await new Promise((resolve, reject) => {
          const timeout = setTimeout(() => reject(new Error('Test peer ICE timed out')), 10000);
          peer.onicegatheringstatechange = () => {
            if (peer.iceGatheringState === 'complete') { clearTimeout(timeout); resolve(); }
          };
        });
        return peer.localDescription.toJSON();
      }, route.request().postDataJSON());
      await route.fulfill({ json: answer });
    });
    const change = async (name, kind) => {
      await page.evaluate(([name, kind]) => {
        window.outputName = name; window.outputKind = kind;
        navigator.mediaDevices.dispatchEvent(new Event('devicechange'));
      }, [name, kind]);
    };
    const wait = async (kind, aec) => page.waitForFunction(([kind, aec]) => activeConnection?.audioRoute?.kind === kind &&
      activeConnection.stream.getAudioTracks()[0].getSettings().echoCancellation === aec, [kind, aec]).catch(async error => {
        console.log(await page.evaluate(() => ({ route: activeConnection?.audioRoute, settings: activeConnection?.stream?.getAudioTracks()[0]?.getSettings(), log: logElement.textContent })));
        throw error;
      });
    const disconnect = async () => {
      await page.evaluate(async () => {
        window.oldTrack = activeConnection.stream.getAudioTracks()[0];
        window.oldRoute = activeConnection.audioRoute;
        disconnect(); testPeer.close(); await testRemoteContext.close();
      });
      await page.waitForFunction(() => !activeConnection && oldTrack.readyState === 'ended');
    };
    await page.goto(process.env.DEMO_URL || 'http://localhost:8083');
    await page.locator('#connect').click();
    await wait('headphones', false);
    await page.waitForFunction(() => activeConnection?.peerConnection?.connectionState === 'connected' && !remoteAudio.paused && window.testReceiver);
    results.headphones = await page.evaluate(async () => {
      const readings = [], data = new Float32Array(testReceiver.fftSize);
      await new Promise(resolve => setTimeout(resolve, 500));
      for (let round = 0; round < 20; round++) {
        for (let i = 0; i < 40; i++) {
          handleControl(JSON.stringify({ event: 'response_status', response_epoch: 1, status: 'speaking' }));
          handleControl(JSON.stringify({ event: 'input_merge', response_epoch: 1, status: 'collecting', text: '去施肥', source_utterance_ids: ['test:1'] }));
        }
        await new Promise(resolve => setTimeout(resolve, 100));
        testReceiver.getFloatTimeDomainData(data);
        readings.push(Math.sqrt(data.reduce((sum, value) => sum + value * value, 0) / data.length));
      }
      return { route: activeConnection.audioRoute.kind, aec: activeConnection.stream.getAudioTracks()[0].getSettings().echoCancellation,
        minimum_received_rms: Math.min(...readings), waveform_rms: activeConnection.localRMS,
        raw_sender: activeConnection.peerConnection.getSenders().some(sender => sender.track === activeConnection.stream.getAudioTracks()[0]) };
    });
    assert.equal(results.headphones.aec, false);
    assert.ok(results.headphones.minimum_received_rms > 0.04 && results.headphones.waveform_rms > 0.04 && results.headphones.raw_sender);
    await change('Speakers (USB)', 'speakers');
    await wait('speakers', true);
    results.speaker_aec_enabled = true;
    await change('Speakers (HECATE G2 GAMING HEADSET)', 'speakers');
    await wait('headphones', false);
    results.headphone_hot_switch_restores_raw = true;
    await change('Unknown Device', 'unknown');
    await wait('unknown', false);
    results.unknown_preserves_raw = true;
    await disconnect();
    await change('Speakers (USB)', 'speakers');
    await page.locator('#connect').click();
    await wait('speakers', true);
    await page.waitForFunction(() => activeConnection?.peerConnection?.connectionState === 'connected');
    results.initial_speaker_connection = true;
    await disconnect();
    await change('Unknown Device', 'unknown');
    await page.locator('#connect').click();
    await wait('unknown', false);
    await page.waitForFunction(() => activeConnection?.peerConnection?.connectionState === 'connected');
    results.reconnected = true;
    await page.evaluate(() => { window.rejectAEC = true; });
    await change('Speakers (USB)', 'speakers');
    await wait('speakers', false);
    assert.equal(await page.evaluate(() => activeConnection.audioRoute.reason), 'constraints_failed');
    results.acquisition_failure_keeps_raw = true;
    await page.screenshot({ path: `${folder}/desktop.png`, fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: `${folder}/mobile.png`, fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.equal(await page.locator('#audioMode, input[name="audioMode"]').count(), 0);
    await disconnect();
    assert.deepEqual(errors, []);
    fs.writeFileSync(`${folder}/results.json`, JSON.stringify({ ...results, errors, passed: true, physical_microphone: false, physical_echo_tested: false, cloud_calls: false }, null, 2));
    console.log(results);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
