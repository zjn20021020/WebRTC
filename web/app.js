const connectButton = document.querySelector('#connect');
const statusElement = document.querySelector('#status');
const logElement = document.querySelector('#log');
const remoteAudio = document.querySelector('#remoteAudio');
const meterElement = document.querySelector('#meter');
const waveform = document.querySelector('#waveform');
const waveformContext = waveform.getContext('2d');
const disconnectButton = document.querySelector('#disconnect');
const asrStatusElement = document.querySelector('#asrStatus');
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
const latencyText = document.querySelector('#latencyText');
const latencyAudio = document.querySelector('#latencyAudio');
const latencyDuck = document.querySelector('#latencyDuck');
const turnLabel = document.querySelector('#turnLabel');
let responseEpoch = -1;
let responseFinished = false;
let activeConnection = null;

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
  stopResponseButton.disabled = !['thinking', 'synthesizing', 'speaking', 'ducking', 'listening'].includes(status);
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
  if (message.event === 'tool_status' && message.tool_call) {
    const names = { water: '浇水', plant: '种菜', harvest: '收菜', fertilize: '施肥', affection: '贴贴', general_qa: '问答' };
    const states = { running: '进行中', completed: '已结束', cancelled: '已取消', failed: '失败' };
    toolStatus.textContent = `${names[message.tool_call.name] || '任务'} · ${states[message.status] || message.status}`;
  } else if (message.event === 'action_result' && message.fallback) {
    toolStatus.textContent = '等待澄清';
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
  };
  asrStatusElement.textContent = labels[status] || status;
  asrStatusElement.dataset.state = status;
}

function handleControl(data) {
  let message;
  try { message = JSON.parse(data); } catch { log('收到无效服务端事件'); return; }
  if (!message || typeof message !== 'object') return;
  if (message.event === 'input_queue' && Number.isSafeInteger(message.queue_size) && message.queue_size >= 0) {
    queuedStatus.textContent = message.queue_size ? `待回答 ${message.queue_size} 条` : '';
  } else if (message.event === 'input_rejected') {
    queuedStatus.textContent = message.reason === 'queue_full' ? '待回答队列已满，本句未收录' : '识别未完成，本句未收录';
  } else if (message.response_epoch === responseEpoch && !responseFinished) {
    if (message.event === 'intent_status') interruptionStatus.textContent = '正在判断插话意图';
    if (message.event === 'intent_result') interruptionStatus.textContent = message.status === 'error'
      ? '意图判断暂不可用，延后处理' : message.interrupt ? '已确认打断' : '本句延后处理';
  }
  if (['response_status', 'response_text', 'response_metrics', 'tool_status', 'action_result'].includes(message.event)) {
    handleResponse(message);
    return;
  }
  if (message.event === 'asr_status' || message.event === 'asr_error') {
    setASRStatus(message.status);
    if (message.event === 'asr_error') partialTranscript.textContent = '';
    return;
  }
  if (message.event === 'asr_partial' && typeof message.text === 'string') {
    if (!finalized.has(message.utterance_id)) partialTranscript.textContent = message.text;
    return;
  }
  if (message.event === 'asr_final' && typeof message.text === 'string') {
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
  const connection = activeConnection;
  activeConnection = null;
  if (connection) {
    if (connection.control?.readyState === 'open') {
      connection.control.send(JSON.stringify({ event: 'disconnect' }));
    }
    connection.abort.abort();
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
}

function drawWaveform(analyser, data, connection) {
  if (activeConnection !== connection) return;
  analyser.getByteTimeDomainData(data);
  const width = waveform.clientWidth || 720;
  const height = waveform.clientHeight || 180;
  waveformContext.clearRect(0, 0, width, height);
  waveformContext.fillStyle = '#101820';
  waveformContext.fillRect(0, 0, width, height);
  waveformContext.strokeStyle = '#55c2a3';
  waveformContext.lineWidth = 2;
  waveformContext.beginPath();
  const slice = width / data.length;
  let sum = 0;
  for (let i = 0; i < data.length; i += 1) {
    const normalized = data[i] / 128 - 1;
    sum += normalized * normalized;
    const x = i * slice;
    const y = height / 2 + normalized * height * 0.42;
    if (i === 0) waveformContext.moveTo(x, y);
    else waveformContext.lineTo(x, y);
  }
  waveformContext.stroke();
  const rms = Math.sqrt(sum / data.length);
  meterElement.textContent = `麦克风 RMS: ${(rms * 100).toFixed(1)}%`;
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
  const connection = { abort: new AbortController() };
  activeConnection = connection;
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
    if (activeConnection !== connection) {
      stream.getTracks().forEach((track) => track.stop());
      return;
    }
    connection.stream = stream;
    const audioContext = new AudioContext();
    connection.audioContext = audioContext;
    await audioContext.resume();
    if (activeConnection !== connection) return;
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 1024;
    const source = audioContext.createMediaStreamSource(stream);
    source.connect(analyser);
    drawWaveform(analyser, new Uint8Array(analyser.fftSize), connection);
    const peerConnection = new RTCPeerConnection();
    connection.peerConnection = peerConnection;
    peerConnection.onconnectionstatechange = () => {
      if (activeConnection !== connection) return;
      statusElement.textContent = peerConnection.connectionState;
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
    for (const track of stream.getAudioTracks()) peerConnection.addTrack(track, stream);

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
    log('SDP offer/answer 完成');
  } catch (error) {
    if (activeConnection !== connection) return;
    disconnect();
    statusElement.textContent = '连接失败';
    log(`错误: ${error.message}`);
    connectButton.disabled = false;
  }
});
