const connectButton = document.querySelector('#connect');
const statusElement = document.querySelector('#status');
const logElement = document.querySelector('#log');
const remoteAudio = document.querySelector('#remoteAudio');
const audioModeInputs = document.querySelectorAll('input[name="audioMode"]');
const audioModeStatus = document.querySelector('#audioModeStatus');
const audioModeAuto = document.querySelector('#audioModeAuto');
const meterElement = document.querySelector('#meter');
const waveform = document.querySelector('#waveform');
const waveformContext = waveform.getContext('2d');
const disconnectButton = document.querySelector('#disconnect');
const asrStatusElement = document.querySelector('#asrStatus');
const asrError = document.querySelector('#asrError');
const partialTranscript = document.querySelector('#partialTranscript');
const finalTranscript = document.querySelector('#finalTranscript');
const finalized = new Map();
const replyStatus = document.querySelector('#replyStatus');
const replyText = document.querySelector('#replyText');
const replyError = document.querySelector('#replyError');
const stopResponseButton = document.querySelector('#stopResponse');
const interruptionStatus = document.querySelector('#interruptionStatus');
const queuedStatus = document.querySelector('#queuedStatus');
const toolStatus = document.querySelector('#toolStatus');
const mergedInput = document.querySelector('#mergedInput');
const taskPlan = document.querySelector('#taskPlan');
const planStatus = document.querySelector('#planStatus');
const planSteps = document.querySelector('#planSteps');
const actionNames = { water: '浇水', plant: '种菜', harvest: '收菜', fertilize: '施肥', affection: '贴贴', general_qa: '问答' };
const taskStates = { pending: '待执行', running: '进行中', completed: '已结束', cancelled: '已取消', failed: '失败' };
let currentPlanID = '';
let mergeCollecting = false;
let queuedInputCount = 0;
const latencyText = document.querySelector('#latencyText');
const latencyAudio = document.querySelector('#latencyAudio');
const latencyDuck = document.querySelector('#latencyDuck');
const turnLabel = document.querySelector('#turnLabel');
let responseEpoch = -1;
let responseFinished = false;
let activeConnection = null;
let manualAudioMode = null;
let detectedAudioRoute = { kind: 'unknown', aec: false, source: 'browser', reason: 'output_type_unavailable', fingerprint: 'initial' };

function renderAudioMode(state = 'ready') {
  const selected = manualAudioMode || (detectedAudioRoute.kind === 'speakers' ? 'speakers' : 'headphones');
  for (const input of audioModeInputs) input.checked = input.value === selected;
  audioModeStatus.dataset.state = state;
  audioModeStatus.textContent = state === 'pending' ? '切换中' : state === 'failed' ? '设置未生效' :
    manualAudioMode ? '手动设置' : detectedAudioRoute.kind === 'unknown' ? '默认耳机' : '自动识别';
}

async function detectInitialAudioRoute() {
  try {
    const { detectAudioRoute } = await import('./audio-route.js');
    const detected = await detectAudioRoute(remoteAudio, new AbortController().signal);
    if (activeConnection) return;
    detectedAudioRoute = detected;
    renderAudioMode();
  } catch { if (!activeConnection) renderAudioMode(); }
}

for (const input of audioModeInputs) input.addEventListener('change', () => {
  manualAudioMode = input.value;
  renderAudioMode(activeConnection ? 'pending' : 'ready');
  activeConnection?.refreshAudioRoute?.({ type: 'modechange' });
});
audioModeAuto.addEventListener('click', () => {
  manualAudioMode = null;
  renderAudioMode(activeConnection ? 'pending' : 'ready');
  if (activeConnection) activeConnection.refreshAudioRoute?.({ type: 'modechange' });
  else detectInitialAudioRoute();
});
detectInitialAudioRoute();

function microphoneConstraints() {
  return { echoCancellation: false, noiseSuppression: false, autoGainControl: false };
}

function logMicrophoneSettings(track) {
  const settings = track?.getSettings() || {};
  const mode = activeConnection?.audioRoute?.kind || 'unknown';
  log(`麦克风: ${JSON.stringify({ label: track?.label, mode, sampleRate: settings.sampleRate, channelCount: settings.channelCount, echoCancellation: settings.echoCancellation, noiseSuppression: settings.noiseSuppression, autoGainControl: settings.autoGainControl })}`);
}

function observeMicrophone(connection, track) {
  track.onmute = () => { if (activeConnection === connection) log('麦克风音轨暂时无数据'); };
  track.onunmute = () => { if (activeConnection === connection) log('麦克风音轨恢复'); };
  track.onended = () => { if (activeConnection === connection) { log('麦克风已被系统停止'); disconnect(); } };
}

async function startAudioRouting(connection) {
  const { detectAudioRoute, prepareAudioRoute, resolveAudioRoute } = await import('./audio-route.js');
  const signal = connection.abort.signal;
  signal.throwIfAborted();
  let checking = false, repeat = false, revision = 0;
  const refresh = async event => {
    if (['devicechange', 'modechange'].includes(event?.type)) revision++;
    if (checking) { repeat = true; return; }
    checking = true;
    try {
      do {
        repeat = false;
        const version = revision;
        const detected = manualAudioMode ? detectedAudioRoute : await detectAudioRoute(remoteAudio, signal);
        signal.throwIfAborted();
        if (version !== revision) { repeat = true; continue; }
        detectedAudioRoute = detected;
        const route = resolveAudioRoute(detected, manualAudioMode);
        if (connection.audioRoute?.fingerprint === route.fingerprint) {
          renderAudioMode(connection.audioRoute.aec === route.aec ? 'ready' : 'failed');
          continue;
        }
        renderAudioMode('pending');
        const prepared = await prepareAudioRoute(connection.stream, route, signal);
        const next = prepared.stream, previous = connection.stream;
        if (activeConnection !== connection || version !== revision) {
          if (next !== previous) next.getTracks().forEach(track => track.stop());
          repeat = true;
          continue;
        }
        if (next !== previous) {
          let source;
          try {
            if (connection.audioContext) {
              source = connection.audioContext.createMediaStreamSource(next);
              source.connect(connection.analyser);
            }
            await connection.microphoneSender?.replaceTrack(next.getAudioTracks()[0]);
            if (activeConnection !== connection) { source?.disconnect(); next.getTracks().forEach(track => track.stop()); return; }
            if (source) {
              connection.source.disconnect();
              connection.source = source;
            }
            connection.stream = next;
            observeMicrophone(connection, next.getAudioTracks()[0]);
            previous.getTracks().forEach(track => { track.onended = null; track.stop(); });
          } catch (error) {
            source?.disconnect();
            next.getTracks().forEach(track => track.stop());
            throw error;
          }
        }
        const applied = prepared.route;
        connection.audioRoute = applied;
        if (version === revision) renderAudioMode(applied.aec === route.aec ? 'ready' : 'failed');
        const mode = { headphones: '耳机', speakers: '扬声器', unknown: '输出类型不确定' }[applied.kind];
        log(`音频路由: ${mode}; AEC=${applied.aec}; reason=${applied.reason}; source=${applied.source}`);
        if (applied.kind === 'unknown' || ['aec_not_enabled', 'constraints_failed', 'raw_processing_not_disabled'].includes(applied.reason)) {
          log('音频处理未能确定，优先保留麦克风；外放回声保护可能不可用');
        }
        logMicrophoneSettings(connection.stream.getAudioTracks()[0]);
      } while (repeat && !signal.aborted);
    } catch (error) {
      if (!signal.aborted && activeConnection === connection) { renderAudioMode('failed'); log(`音频路由检测失败: ${error.name}`); }
    } finally { checking = false; }
  };
  connection.refreshAudioRoute = refresh;
  await refresh();
  signal.throwIfAborted();
  navigator.mediaDevices.addEventListener('devicechange', refresh, { signal });
  connection.routeTimer = setInterval(refresh, 2000);
}

function setReplyStatus(status) {
  const labels = {
    disconnected: '未连接', waiting: '连接中', ready: '等待提问',
    thinking: '生成回答中', synthesizing: '合成语音中', speaking: '播放中',
    ducking: '已降低音量，确认插话中', listening: '等待新问题说完',
    completed: '回答结束', interrupted: '已停止', failed: '回答失败',
    llm_unconfigured: '未配置 DeepSeek', tts_unconfigured: '未配置腾讯语音',
  };
  replyStatus.textContent = labels[status] || status;
  replyStatus.dataset.state = status;
  updateStopButton();
}

function updateStopButton() {
  stopResponseButton.disabled = !mergeCollecting && queuedInputCount === 0 && !['thinking', 'synthesizing', 'speaking', 'ducking', 'listening'].includes(replyStatus.dataset.state);
}

function resetPlan() {
  currentPlanID = '';
  taskPlan.hidden = true;
  planStatus.textContent = '';
  planSteps.replaceChildren();
}

function handlePlan(message) {
  if (!Number.isSafeInteger(message.response_epoch) || message.response_epoch < responseEpoch || typeof message.plan_id !== 'string' || !Array.isArray(message.steps) || message.steps.length > 6) return;
  currentPlanID = message.plan_id;
  taskPlan.hidden = false;
  planStatus.textContent = `任务计划 · ${taskStates[message.status] || message.status} · ${message.step_index}/${message.step_count}`;
  planSteps.replaceChildren(...message.steps.map((step, index) => {
    const item = document.createElement('li');
    item.dataset.state = Object.hasOwn(taskStates, step.status) ? step.status : 'pending';
    const label = document.createElement('span');
    label.textContent = `${index + 1}. ${actionNames[step.action] || '任务'} · ${step.text}`;
    const state = document.createElement('span');
    state.textContent = taskStates[step.status] || '待执行';
    item.append(label, state);
    return item;
  }));
}

function handleResponse(message) {
  const epoch = message.response_epoch;
  if (!Number.isSafeInteger(epoch) || epoch < responseEpoch) return;
  if (epoch > responseEpoch) {
    responseEpoch = epoch;
    turnLabel.textContent = epoch > 0 ? `第 ${epoch} 轮` : '';
    responseFinished = false;
    replyText.textContent = '';
    replyError.hidden = true;
    replyError.textContent = '';
    interruptionStatus.textContent = '';
    toolStatus.textContent = '';
    latencyText.textContent = '--';
    latencyAudio.textContent = '--';
    latencyDuck.textContent = '--';
  }
  if (responseFinished) return;
  if (message.event === 'action_result' && message.plan_id !== currentPlanID) resetPlan();
  if (message.event === 'tool_status' && message.tool_call) {
    toolStatus.textContent = `${actionNames[message.tool_call.name] || '任务'} · ${taskStates[message.status] || message.status}`;
  } else if (message.event === 'action_retry') {
    toolStatus.textContent = '正在重新确认任务';
  } else if (message.event === 'action_result' && message.fallback) {
    toolStatus.textContent = '任务未能启动';
  } else if (message.event === 'response_metrics' && message.metrics && typeof message.metrics === 'object') {
    const display = (element, value) => {
      if (Number.isSafeInteger(value) && value >= 0) element.textContent = `${value} ms`;
    };
    display(latencyText, message.metrics.speech_end_to_first_text_ms);
    display(latencyAudio, message.metrics.speech_end_to_first_audio_ms);
    display(latencyDuck, message.metrics.speech_to_duck_ms);
  } else if (message.event === 'response_text' && typeof message.text === 'string') {
    replyText.textContent = message.text;
  } else if (message.event === 'response_status') {
    setReplyStatus(message.status);
    if (message.status === 'ducking') {
      interruptionStatus.textContent = '等待语音确认';
      latencyDuck.textContent = '--';
    } else if (message.reason === 'unconfirmed') {
      interruptionStatus.textContent = '未确认插话，已恢复音量';
    } else if (message.reason === 'asr_unavailable') {
      interruptionStatus.textContent = '识别服务不可用，已恢复音量';
    } else if (message.reason === 'intent_deferred') {
      interruptionStatus.textContent = '继续当前回答';
    } else if (message.status === 'interrupted') {
      interruptionStatus.textContent = '旧回答已取消';
    }
    if (message.status === 'failed' && typeof message.detail === 'string') {
      replyError.textContent = message.detail;
      replyError.hidden = false;
    }
    responseFinished = ['completed', 'interrupted', 'failed'].includes(message.status);
  }
}

stopResponseButton.addEventListener('click', () => {
  if (activeConnection?.control?.readyState === 'open') {
    activeConnection.control.send(JSON.stringify({ event: 'stop_response', response_epoch: responseEpoch }));
  }
});

function setASRStatus(status) {
  const labels = {
    disconnected: '未连接', waiting: '等待识别服务', unconfigured: '未配置凭证',
    connecting: '连接识别服务中', listening: '识别中', stopped: '识别已结束',
    failed: '识别连接失败', backlog: '识别网络拥堵',
    quota_exhausted: '识别额度不可用', auth_failed: '识别鉴权失败',
    service_unavailable: '识别服务未开通', account_overdue: '识别账号欠费',
    concurrency_limit: '识别并发已满', invalid_request: '识别参数错误',
  };
  asrStatusElement.textContent = labels[status] || status;
  asrStatusElement.dataset.state = status;
  asrError.hidden = true;
  asrError.textContent = '';
}

function handleControl(data) {
  let message;
  try { message = JSON.parse(data); } catch { log('收到无效服务端事件'); return; }
  if (!message || typeof message !== 'object') return;
  if (activeConnection) activeConnection.controlEvents = (activeConnection.controlEvents || 0) + 1;
  if (message.event === 'asr_warning') {
    asrError.textContent = message.detail || '尚未收到完整识别结果';
    asrError.hidden = false;
    log(`识别诊断: ${JSON.stringify(message)}`);
    return;
  }
  if (message.event === 'plan_status') { handlePlan(message); return; }
  if (message.event === 'input_merge') {
    if (!Number.isSafeInteger(message.response_epoch) || message.response_epoch < responseEpoch) return;
    mergeCollecting = message.status === 'collecting';
    const states = { collecting: '等你说完', committed: '本次输入', discarded: '本句已取消' };
    const count = Array.isArray(message.source_utterance_ids) ? message.source_utterance_ids.length : 1;
    mergedInput.textContent = `${states[message.status] || '本次输入'}${count > 1 ? ` · ${count} 段合并` : ''}：${message.text || ''}`;
    updateStopButton();
    return;
  }
  if (message.event === 'input_queue' && Number.isSafeInteger(message.queue_size) && message.queue_size >= 0) {
    if (!Number.isSafeInteger(message.response_epoch) || message.response_epoch < responseEpoch) return;
    queuedInputCount = message.queue_size;
    queuedStatus.textContent = message.queue_size ? `待回答 ${message.queue_size} 条` : '';
    updateStopButton();
  } else if (message.event === 'input_rejected') {
    const reasons = { queue_full: '待回答队列已满，本句未收录', merge_limit: '本次输入过长，请分次说', input_too_long: '本次输入过长，请分次说' };
    queuedStatus.textContent = reasons[message.reason] || '识别未完成，本句未收录';
  } else if (message.response_epoch === responseEpoch && !responseFinished) {
    if (message.event === 'intent_status') interruptionStatus.textContent = message.status === 'waiting_final'
      ? '等待这句话说完' : '正在判断插话意图';
    if (message.event === 'intent_result') interruptionStatus.textContent = message.status === 'error'
      ? '意图判断暂不可用，延后处理' : message.interrupt ? '已确认打断' : '本句延后处理';
  }
  if (['response_status', 'response_text', 'response_metrics', 'tool_status', 'action_result', 'action_retry'].includes(message.event)) {
    handleResponse(message);
    return;
  }
  if (message.event === 'asr_status' || message.event === 'asr_error') {
    setASRStatus(message.status);
    if (message.event === 'asr_error') {
      partialTranscript.textContent = '';
      asrError.textContent = typeof message.detail === 'string' ? message.detail : '识别服务不可用，请断开重连。';
      asrError.hidden = false;
      log(`识别错误: ${JSON.stringify(message)}`);
    }
    return;
  }
  if (message.event === 'asr_partial' && typeof message.text === 'string') {
    asrError.hidden = true;
    if (!finalized.has(message.utterance_id)) partialTranscript.textContent = message.text;
    return;
  }
  if (message.event === 'asr_final' && typeof message.text === 'string') {
    asrError.hidden = true;
    log(`ASR 原文: ${message.text}`);
    let line = finalized.get(message.utterance_id);
    if (!line) {
      line = document.createElement('li');
      finalTranscript.appendChild(line);
      finalized.set(message.utterance_id, line);
    }
    line.textContent = message.text;
    partialTranscript.textContent = '';
    if (finalized.size > 100) {
      const oldest = finalized.keys().next().value;
      finalized.get(oldest).remove();
      finalized.delete(oldest);
    }
    finalTranscript.scrollTop = finalTranscript.scrollHeight;
    return;
  }
  log(`服务端事件: ${data}`);
}

function disconnect() {
  resetPlan();
  mergeCollecting = false;
  queuedInputCount = 0;
  mergedInput.textContent = '';
  const connection = activeConnection;
  activeConnection = null;
  renderAudioMode();
  if (connection) {
    if (connection.control?.readyState === 'open') {
      connection.control.send(JSON.stringify({ event: 'disconnect' }));
    }
    connection.abort.abort();
    clearInterval(connection.healthTimer);
    clearInterval(connection.routeTimer);
    connection.longTaskObserver?.disconnect();
    connection.stream?.getTracks().forEach((track) => track.stop());
    cancelAnimationFrame(connection.animationFrame);
    connection.audioContext?.close().catch(() => {});
    // Give SCTP a chance to deliver the cancellation before closing transport.
    setTimeout(() => connection.peerConnection?.close(), 150);
  }
  remoteAudio.pause();
  remoteAudio.srcObject = null;
  meterElement.textContent = '麦克风未启用';
  partialTranscript.textContent = '';
  setASRStatus('disconnected');
  setReplyStatus('disconnected');
  interruptionStatus.textContent = '';
  queuedStatus.textContent = '';
  toolStatus.textContent = '';
  responseFinished = true;
  connectButton.disabled = false;
  disconnectButton.disabled = true;
  statusElement.textContent = '未连接';
  statusElement.dataset.state = 'disconnected';
  resizeWaveform();
}

disconnectButton.addEventListener('click', disconnect);
window.addEventListener('pagehide', disconnect);

function resizeWaveform() {
  const ratio = window.devicePixelRatio || 1;
  const width = waveform.clientWidth || 720;
  const height = waveform.clientHeight || 180;
  waveform.width = Math.floor(width * ratio);
  waveform.height = Math.floor(height * ratio);
  waveformContext.setTransform(ratio, 0, 0, ratio, 0, 0);
  paintWaveformBackground(width, height);
}

function paintWaveformBackground(width, height) {
  waveformContext.fillStyle = '#edf5ef';
  waveformContext.fillRect(0, 0, width, height);
  waveformContext.strokeStyle = '#d7e6da';
  waveformContext.lineWidth = 1;
  waveformContext.beginPath();
  for (let x = 24; x < width; x += 24) {
    waveformContext.moveTo(x, 0);
    waveformContext.lineTo(x, height);
  }
  waveformContext.moveTo(0, height / 2);
  waveformContext.lineTo(width, height / 2);
  waveformContext.stroke();
}

function drawWaveform(analyser, data, connection) {
  if (activeConnection !== connection) return;
  const now = performance.now();
  connection.frameGapMS = Math.max(connection.frameGapMS || 0, connection.lastFrame ? now - connection.lastFrame : 0);
  connection.lastFrame = now;
  analyser.getFloatTimeDomainData(data);
  const width = waveform.clientWidth || 720;
  const height = waveform.clientHeight || 180;
  waveformContext.clearRect(0, 0, width, height);
  paintWaveformBackground(width, height);
  waveformContext.strokeStyle = '#479479';
  waveformContext.lineWidth = 2;
  waveformContext.beginPath();
  const slice = width / data.length;
  let sum = 0;
  for (let i = 0; i < data.length; i += 1) {
    const normalized = data[i];
    sum += normalized * normalized;
    const x = i * slice;
    const y = height / 2 + normalized * height * 0.42;
    if (i === 0) waveformContext.moveTo(x, y);
    else waveformContext.lineTo(x, y);
  }
  waveformContext.stroke();
  const rms = Math.sqrt(sum / data.length);
  connection.localRMS = rms;
  meterElement.textContent = `麦克风 RMS: ${(rms * 100).toFixed(2)}%`;
  connection.animationFrame = requestAnimationFrame(() => drawWaveform(analyser, data, connection));
}

resizeWaveform();
window.addEventListener('resize', resizeWaveform);

function log(message) {
  const line = `${new Date().toISOString()} ${message}`;
  logElement.textContent += `${line}\n`;
  const lines = logElement.textContent.split('\n');
  if (lines.length > 200) logElement.textContent = lines.slice(-200).join('\n');
  console.log(line);
}

function waitForIceGatheringComplete(peerConnection, signal) {
  if (peerConnection.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timer);
      peerConnection.removeEventListener('icegatheringstatechange', check);
      signal.removeEventListener('abort', abort);
    };
    const abort = () => { cleanup(); reject(new Error('连接已取消')); };
    const check = () => {
      if (peerConnection.iceGatheringState === 'complete') {
        cleanup();
        resolve();
      }
    };
    const timer = setTimeout(() => { cleanup(); reject(new Error('ICE 连接超时')); }, 15000);
    peerConnection.addEventListener('icegatheringstatechange', check);
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
  });
}

connectButton.addEventListener('click', async () => {
  resetPlan();
  mergeCollecting = false;
  queuedInputCount = 0;
  mergedInput.textContent = '';
  connectButton.disabled = true;
  disconnectButton.disabled = false;
  finalTranscript.replaceChildren();
  finalized.clear();
  partialTranscript.textContent = '';
  setASRStatus('waiting');
  responseEpoch = -1;
  responseFinished = false;
  replyText.textContent = '';
  replyError.textContent = '';
  replyError.hidden = true;
  interruptionStatus.textContent = '';
  queuedStatus.textContent = '';
  toolStatus.textContent = '';
  for (const element of [latencyText, latencyAudio, latencyDuck]) element.textContent = '--';
  turnLabel.textContent = '';
  setReplyStatus('waiting');
  statusElement.textContent = '连接中';
  statusElement.dataset.state = 'connecting';
  const connection = { abort: new AbortController() };
  activeConnection = connection;
  try {
    let stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints() });
    if (activeConnection !== connection) {
      stream.getTracks().forEach((track) => track.stop());
      return;
    }
    connection.stream = stream;
    await startAudioRouting(connection);
    if (activeConnection !== connection) return;
    stream = connection.stream;
    const microphoneTrack = stream.getAudioTracks()[0];
    observeMicrophone(connection, microphoneTrack);
    const audioContext = new AudioContext();
    connection.audioContext = audioContext;
    await audioContext.resume();
    if (activeConnection !== connection) return;
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 1024;
    const source = audioContext.createMediaStreamSource(stream);
    source.connect(analyser);
    connection.source = source;
    connection.analyser = analyser;
    audioContext.onstatechange = () => {
      if (activeConnection !== connection) return;
      log(`麦克风音频上下文: ${audioContext.state}`);
      if (audioContext.state === 'suspended') audioContext.resume().catch(() => log('麦克风波形暂停，音频上下文未恢复'));
    };
    drawWaveform(analyser, new Float32Array(analyser.fftSize), connection);
    const peerConnection = new RTCPeerConnection();
    connection.peerConnection = peerConnection;
    peerConnection.onconnectionstatechange = () => {
      if (activeConnection !== connection) return;
      statusElement.textContent = peerConnection.connectionState;
      statusElement.dataset.state = peerConnection.connectionState;
      log(`peer connection: ${peerConnection.connectionState}`);
      if (['failed', 'closed', 'disconnected'].includes(peerConnection.connectionState)) disconnect();
    };
    peerConnection.ontrack = (event) => {
      if (activeConnection !== connection) return;
      remoteAudio.srcObject = event.streams[0] || new MediaStream([event.track]);
      log('收到服务端下行音轨');
      remoteAudio.play().then(() => log('远端音频播放中')).catch((error) => log(`远端音频播放失败: ${error.message}`));
    };
    const dataChannel = peerConnection.createDataChannel('control');
    connection.control = dataChannel;
    dataChannel.onopen = () => log('DataChannel 已连接');
    dataChannel.onmessage = (event) => {
      if (activeConnection === connection) handleControl(event.data);
    };
    connection.microphoneSender = peerConnection.addTrack(microphoneTrack, stream);

    const offer = await peerConnection.createOffer();
    await peerConnection.setLocalDescription(offer);
    await waitForIceGatheringComplete(peerConnection, connection.abort.signal);

    const response = await fetch('/api/offer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(peerConnection.localDescription),
      signal: connection.abort.signal,
    });
    if (!response.ok) throw new Error(await response.text());
    await peerConnection.setRemoteDescription(await response.json());
    startAudioDiagnostics(connection);
    log('SDP offer/answer 完成');
  } catch (error) {
    if (activeConnection !== connection) return;
    disconnect();
    statusElement.textContent = '连接失败';
    statusElement.dataset.state = 'failed';
    log(`错误: ${error.message}`);
    connectButton.disabled = false;
  }
});

function startAudioDiagnostics(connection) {
  connection.diagnosticSession = crypto.randomUUID();
  if (typeof PerformanceObserver !== 'undefined' && PerformanceObserver.supportedEntryTypes?.includes('longtask')) {
    connection.longTaskObserver = new PerformanceObserver(list => {
      for (const entry of list.getEntries()) connection.longTaskMS = Math.max(connection.longTaskMS || 0, entry.duration);
    });
    connection.longTaskObserver.observe({ type: 'longtask' });
  }
  connection.healthTimer = setInterval(async () => {
    if (activeConnection !== connection || connection.healthPending) return;
    connection.healthPending = true;
    try {
      const reports = [...(await connection.peerConnection.getStats()).values()];
      if (activeConnection !== connection) return;
      const track = connection.stream.getAudioTracks()[0];
      const media = reports.find(r => r.type === 'media-source' && r.kind === 'audio');
      const outbound = reports.find(r => r.type === 'outbound-rtp' && r.kind === 'audio');
      const settings = track?.getSettings() || {};
      connection.healthData ||= new Float32Array(connection.analyser.fftSize);
      connection.analyser.getFloatTimeDomainData(connection.healthData);
      const measuredRMS = Math.sqrt(connection.healthData.reduce((sum, value) => sum + value * value, 0) / connection.healthData.length);
      const sample = { session: connection.diagnosticSession, context: connection.audioContext.state, track: track?.readyState || 'missing', muted: !!track?.muted, enabled: !!track?.enabled,
        echo_cancellation: settings.echoCancellation ?? null, noise_suppression: settings.noiseSuppression ?? null, auto_gain_control: settings.autoGainControl ?? null,
        output_kind: connection.audioRoute?.kind || 'unknown', output_reason: connection.audioRoute?.reason || '',
        playback: !remoteAudio.paused && !!remoteAudio.srcObject, response_state: replyStatus.dataset.state || '', page_visible: document.visibilityState === 'visible', frame_age_ms: connection.lastFrame ? performance.now() - connection.lastFrame : 0,
        rms: measuredRMS, energy: media?.totalAudioEnergy || 0, packets: outbound?.packetsSent || 0, bytes: outbound?.bytesSent || 0,
        frame_gap_ms: connection.frameGapMS || 0, long_task_ms: connection.longTaskMS || 0, control_events: connection.controlEvents || 0 };
      connection.frameGapMS = 0;
      connection.longTaskMS = 0;
      connection.lastAudioDiagnostic = sample;
      const response = await fetch('/api/diagnostics', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(sample), signal: connection.abort.signal });
      if (response.status === 404 || response.status === 405 || response.status === 403) clearInterval(connection.healthTimer);
    } catch { /* Connection shutdown or a diagnostics-disabled server. */ }
    finally { connection.healthPending = false; }
  }, 1000);
}
