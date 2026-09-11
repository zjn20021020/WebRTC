const connectButton = document.querySelector('#connect');
const statusElement = document.querySelector('#status');
const logElement = document.querySelector('#log');
const remoteAudio = document.querySelector('#remoteAudio');

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
