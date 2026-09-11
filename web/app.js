const connectButton = document.querySelector('#connect');
const statusElement = document.querySelector('#status');
const logElement = document.querySelector('#log');
const remoteAudio = document.querySelector('#remoteAudio');
const meterElement = document.querySelector('#meter');
const waveform = document.querySelector('#waveform');
const waveformContext = waveform.getContext('2d');

function resizeWaveform() {
  const ratio = window.devicePixelRatio || 1;
  const width = waveform.clientWidth || 720;
  const height = waveform.clientHeight || 180;
  waveform.width = Math.floor(width * ratio);
  waveform.height = Math.floor(height * ratio);
  waveformContext.setTransform(ratio, 0, 0, ratio, 0, 0);
}

function drawWaveform(analyser, data) {
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
  requestAnimationFrame(() => drawWaveform(analyser, data));
}

resizeWaveform();
window.addEventListener('resize', resizeWaveform);

function log(message) {
  const line = `${new Date().toISOString()} ${message}`;
  logElement.textContent += `${line}\n`;
  console.log(line);
}

function waitForIceGatheringComplete(peerConnection) {
  if (peerConnection.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    const check = () => {
      if (peerConnection.iceGatheringState === 'complete') {
        peerConnection.removeEventListener('icegatheringstatechange', check);
        resolve();
      }
    };
    peerConnection.addEventListener('icegatheringstatechange', check);
  });
}

connectButton.addEventListener('click', async () => {
  connectButton.disabled = true;
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const audioContext = new AudioContext();
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 1024;
    const source = audioContext.createMediaStreamSource(stream);
    source.connect(analyser);
    drawWaveform(analyser, new Uint8Array(analyser.fftSize));
    const peerConnection = new RTCPeerConnection();
    peerConnection.onconnectionstatechange = () => {
      statusElement.textContent = peerConnection.connectionState;
      log(`peer connection: ${peerConnection.connectionState}`);
    };
    peerConnection.ontrack = (event) => {
      remoteAudio.srcObject = event.streams[0] || new MediaStream([event.track]);
      log('收到服务端下行音轨');
    };
    const dataChannel = peerConnection.createDataChannel('control');
    dataChannel.onopen = () => log('DataChannel 已连接');
    dataChannel.onmessage = (event) => log(`服务端事件: ${event.data}`);
    for (const track of stream.getAudioTracks()) peerConnection.addTrack(track, stream);

    const offer = await peerConnection.createOffer();
    await peerConnection.setLocalDescription(offer);
    await waitForIceGatheringComplete(peerConnection);

    const response = await fetch('/api/offer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(peerConnection.localDescription),
    });
    if (!response.ok) throw new Error(await response.text());
    await peerConnection.setRemoteDescription(await response.json());
    statusElement.textContent = '已连接';
    log('SDP offer/answer 完成');
  } catch (error) {
    statusElement.textContent = '连接失败';
    log(`错误: ${error.message}`);
    connectButton.disabled = false;
  }
});
