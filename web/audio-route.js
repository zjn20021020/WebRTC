const headphoneName = /headphones?|headsets?|earphones?|earbuds?|airpods|耳机|耳麦/i;
const speakerName = /speakers?|扬声器|音箱|内置喇叭/i;

function normalizedName(name = '') {
  return name.replace(/^(default|communications|默认|通訊|通信|通讯)\s*[-:：]?\s*/i, '').trim().toLowerCase();
}

export function selectAudioRoute(devices, sinkID = '', system = null) {
  const outputs = devices.filter(device => device.kind === 'audiooutput');
  const customSink = sinkID && sinkID !== 'default';
  const selected = outputs.find(device => device.deviceId === (customSink ? sinkID : 'default'));
  const label = selected?.label || '';
  // System information only describes its default endpoint. Never apply it
  // to an explicitly selected browser sink or a conflicting browser label.
  const systemMatches = !customSink && system && (!label || !system.name || normalizedName(label) === normalizedName(system.name));
  const systemName = systemMatches ? system.name || '' : '';
  const name = label || systemName;
  const result = (kind, reason) => ({ kind, aec: kind === 'speakers', reason, name,
    source: systemMatches ? 'system_and_browser' : 'browser', fingerprint: `${sinkID}|${label}|${systemName}|${systemMatches ? system.kind : ''}` });
  // USB gaming headsets can advertise the Windows Speakers form factor.
  if (headphoneName.test(name)) return result('headphones', 'headphone_name');
  if (systemMatches && system.kind === 'headphones') return result('headphones', 'headphone_form_factor');
  if (customSink && !selected) return result('unknown', 'selected_output_unavailable');
  if (systemMatches && system.kind === 'speakers') return result('speakers', 'speaker_form_factor');
  if (speakerName.test(name)) return result('speakers', 'speaker_name');
  return result('unknown', 'output_type_unavailable');
}

export async function detectAudioRoute(element, signal) {
  signal.throwIfAborted();
  const timeout = new AbortController();
  const abort = () => timeout.abort();
  signal.addEventListener('abort', abort, { once: true });
  const timer = setTimeout(abort, 1200);
  try {
    const readSystem = async () => {
      if (!['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)) return null;
      const response = await fetch('/api/audio-output', { signal: timeout.signal, cache: 'no-store' });
      if (!response.ok) return null;
      const value = await response.json();
      return value && ['headphones', 'speakers', 'unknown'].includes(value.kind) &&
        (value.name === undefined || typeof value.name === 'string') ? value : null;
    };
    const [devices, system] = await Promise.allSettled([navigator.mediaDevices.enumerateDevices(), readSystem()]);
    signal.throwIfAborted();
    return selectAudioRoute(devices.status === 'fulfilled' ? devices.value : [], element.sinkId, system.status === 'fulfilled' ? system.value : null);
  } finally {
    clearTimeout(timer);
    signal.removeEventListener('abort', abort);
  }
}

export async function prepareAudioRoute(stream, route, signal, getUserMedia = value => navigator.mediaDevices.getUserMedia(value)) {
  signal.throwIfAborted();
  const track = stream.getAudioTracks()[0];
  const settings = track.getSettings();
  if (settings.echoCancellation === route.aec && settings.noiseSuppression !== true && settings.autoGainControl !== true) {
    return { stream, route };
  }
  let candidate;
  try {
    // Chromium can accept applyConstraints without changing its audio processor.
    // Acquire the requested processing mode, then let the caller replaceTrack.
    const audio = { echoCancellation: route.aec, noiseSuppression: false, autoGainControl: false };
    if (settings.deviceId && settings.deviceId !== 'default') audio.deviceId = { exact: settings.deviceId };
    candidate = await getUserMedia({ audio });
    signal.throwIfAborted();
    const actual = candidate.getAudioTracks()[0].getSettings().echoCancellation === true;
    if (actual !== route.aec) {
      candidate.getTracks().forEach(value => value.stop());
      return { stream, route: { ...route, aec: settings.echoCancellation === true,
        reason: route.aec ? 'aec_not_enabled' : 'raw_processing_not_disabled' } };
    }
    return { stream: candidate, route };
  } catch (error) {
    candidate?.getTracks().forEach(value => value.stop());
    signal.throwIfAborted();
    return { stream, route: { ...route, aec: settings.echoCancellation === true, reason: 'constraints_failed' } };
  }
}
