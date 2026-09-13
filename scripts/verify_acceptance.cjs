const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const net = require('node:net');
const { spawn, execFileSync } = require('node:child_process');
const { summarizeText, summarizeMedia, markdown } = require('./acceptance_stats.cjs');

const root = path.resolve(__dirname, '..');
const readJSON = file => JSON.parse(fs.readFileSync(file, 'utf8'));
const digest = data => crypto.createHash('sha256').update(data).digest('hex');
const writeJSON = (file, value) => fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);

function options(args) {
  const o = { repeat: 3, mediaRepeat: 1, textOnly: false };
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--text-only') o.textOnly = true;
    else if (['--repeat', '--media-repeat'].includes(args[i])) {
      const key = args[i] === '--repeat' ? 'repeat' : 'mediaRepeat';
      o[key] = Number(args[++i]);
      if (!Number.isInteger(o[key]) || o[key] < 1 || o[key] > 20) throw new Error('Repeat must be between 1 and 20');
    } else throw new Error(`Unknown option: ${args[i]}`);
  }
  return o;
}

function sourceHash() {
  const h = crypto.createHash('sha256');
  function visit(relative) {
    const file = path.join(root, relative);
    if (fs.statSync(file).isDirectory()) for (const name of fs.readdirSync(file).sort()) visit(`${relative}/${name}`);
    else { h.update(relative); h.update('\0'); h.update(fs.readFileSync(file)); h.update('\0'); }
  }
  for (const dir of ['cmd', 'internal', 'scripts', 'testdata', 'web']) visit(dir);
  for (const file of ['go.mod', 'go.sum']) visit(file);
  return h.digest('hex');
}

function stopChild(child) {
  if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
  if (process.platform === 'win32') {
    try { execFileSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' }); } catch {}
  } else child.kill('SIGTERM');
}

function run(command, args, logFile, env = process.env, timeoutMS = 600000) {
  return new Promise(resolve => {
    const log = fs.createWriteStream(logFile);
    const child = spawn(command, args, { cwd: root, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.pipe(log, { end: false }); child.stderr.pipe(log, { end: false });
    let error;
    const timer = setTimeout(() => { error = 'process_timeout'; stopChild(child); }, timeoutMS);
    child.on('error', () => { error = 'process_start_failed'; });
    child.on('close', code => { clearTimeout(timer); log.end(); resolve({ exit_code: code ?? 2, error }); });
  });
}

async function availablePort() {
  const socket = net.createServer();
  await new Promise((resolve, reject) => { socket.once('error', reject); socket.listen(0, '127.0.0.1', resolve); });
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  return port;
}

async function main() {
  const o = options(process.argv.slice(2));
  process.chdir(root);
  const go = process.env.GO_BIN || 'go';
  const suiteFile = path.join(root, 'testdata/acceptance/cases.json');
  const suite = readJSON(suiteFile), mediaSuite = readJSON(path.join(root, 'testdata/acceptance/media.json'));
  const out = path.join(root, 'bin', 'acceptance', `${new Date().toISOString().replaceAll(':', '-').replaceAll('.', '-')}-${process.pid}`);
  fs.mkdirSync(out, { recursive: true });
  fs.copyFileSync(suiteFile, path.join(out, 'cases.json'));
  writeJSON(path.join(out, 'media-cases.json'), mediaSuite);
  const report = { recorded_at: new Date().toISOString(), git_commit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', windowsHide: true }).trim(),
    source_sha256: sourceHash(), suite_sha256: digest(fs.readFileSync(suiteFile)), options: o, fixture_sha256: {}, media_version: mediaSuite.version };
  console.log(`Acceptance output: ${out}`);
  let server, serverLog;
  const mediaRuns = [];
  const textPath = path.join(out, 'text-results.json');
  try {
    if (!o.textOnly) {
      require('playwright');
      const chrome = process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
      if (!fs.existsSync(chrome)) throw new Error('Chrome not found; set CHROME_PATH');
    }
    const executable = name => path.join(out, name + (process.platform === 'win32' ? '.exe' : ''));
    for (const target of o.textOnly ? ['verify-acceptance'] : ['verify-acceptance', 'server', 'demo-fixtures']) {
      const built = await run(go, ['build', '-o', executable(target), `./cmd/${target}`], path.join(out, `build-${target}.log`));
      if (built.exit_code !== 0) throw new Error(`Build failed: ${target}; see build log`);
    }
    console.log(`Text: ${suite.cases.length} cases x ${o.repeat} rounds`);
    report.text_process = await run(executable('verify-acceptance'), ['-cases', path.join(out, 'cases.json'), '-repeat', String(o.repeat), '-output', textPath], path.join(out, 'text.log'), process.env, o.repeat * suite.cases.length * 6000 + 30000);
    if (!o.textOnly) {
      for (const scene of [...new Set(mediaSuite.scenarios.flatMap(s => s.fixture_scenes))]) {
        const required = mediaSuite.scenarios.filter(s => s.fixture_scenes.includes(scene)).flatMap(s => s.fixtures);
        if (required.some(name => !fs.existsSync(path.join(root, 'bin', `${name}.wav`)))) {
          console.log(`Preparing fixed microphone fixtures: ${scene}`);
          const prepared = await run(executable('demo-fixtures'), ['-scene', scene], path.join(out, `fixtures-${scene}.log`), { ...process.env, TENCENT_TTS_VOICE_TYPE: '1001' });
          if (prepared.exit_code !== 0) throw new Error(`Fixture synthesis failed: ${scene}`);
        }
      }
      for (const name of [...new Set(mediaSuite.scenarios.flatMap(s => s.fixtures))]) report.fixture_sha256[name] = digest(fs.readFileSync(path.join(root, 'bin', `${name}.wav`)));
      const port = await availablePort(), url = `http://127.0.0.1:${port}`;
      serverLog = fs.createWriteStream(path.join(out, 'server.log'));
      server = spawn(executable('server'), ['-addr', `127.0.0.1:${port}`], { cwd: root, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
      server.stdout.pipe(serverLog, { end: false }); server.stderr.pipe(serverLog, { end: false });
      let startError;
      server.on('error', () => { startError = true; });
      let ready = false;
      for (let i = 0; i < 50; i++) {
        if (startError || server.exitCode !== null) throw new Error('Isolated server failed to start; see server.log');
        try { if ((await fetch(url, { signal: AbortSignal.timeout(1000) })).ok) { ready = true; break; } } catch {}
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      if (!ready) throw new Error('Isolated server did not become ready');
      report.media_server = 'Isolated local process built from this source snapshot';
      for (let round = 1; round <= o.mediaRepeat; round++) for (const scenario of mediaSuite.scenarios) {
        const dir = path.join(out, 'media', `${scenario.id}-${round}`);
        fs.mkdirSync(dir, { recursive: true });
        console.log(`Media: ${scenario.id}, round ${round}/${o.mediaRepeat}`);
        const result = await run(process.execPath, [scenario.script || 'scripts/verify_home.cjs', scenario.flag], path.join(dir, 'run.log'),
          { ...process.env, DEMO_URL: url, EVIDENCE_DIR: dir, SCREENSHOT_DIR: dir }, 180000);
        mediaRuns.push({ id: scenario.id, round, ...result, evidence: path.join(dir, scenario.evidence), relative_evidence: `media/${scenario.id}-${round}/${scenario.evidence}` });
        writeJSON(path.join(out, 'media-runs.json'), mediaRuns.map(({ evidence, ...r }) => r));
        console.log(`Media: ${scenario.id} ${result.exit_code === 0 ? 'passed' : 'FAILED (continuing)'}`);
      }
    }
  } catch (error) {
    report.run_error = error.message;
    console.error(error.message);
  } finally {
    if (server) {
      server.stdout.unpipe(serverLog); server.stderr.unpipe(serverLog);
      if (server.exitCode === null && server.signalCode === null) {
        const closed = new Promise(resolve => server.once('close', resolve));
        stopChild(server);
        await closed;
      }
    }
    if (serverLog) await new Promise(resolve => serverLog.end(resolve));
    const serverLogPath = path.join(out, 'server.log');
    if (fs.existsSync(serverLogPath)) {
      const startup = fs.readFileSync(serverLogPath, 'utf8');
      const voice = startup.match(/TTS configured=true voice=(\d+) sample_rate=(\d+)/);
      if (voice) report.audio_config = { tts_voice_type: Number(voice[1]), sample_rate: Number(voice[2]) };
    }
    const raw = fs.existsSync(textPath) ? readJSON(textPath) : { records: [] };
    report.model = raw.model;
    report.text = summarizeText(suite, o.repeat, raw.records);
    if (!o.textOnly) for (let round = 1; round <= o.mediaRepeat; round++) for (const s of mediaSuite.scenarios) {
      if (!mediaRuns.some(r => r.id === s.id && r.round === round)) mediaRuns.push({ id: s.id, round, exit_code: 2, error: 'not_run', relative_evidence: `media/${s.id}-${round}/${s.evidence}` });
    }
    report.media = summarizeMedia(mediaRuns);
    report.passed = !report.run_error && raw.completed === true && report.text.passed === report.text.planned && report.media.passed === report.media.planned;
    writeJSON(path.join(out, 'summary.json'), report);
    fs.writeFileSync(path.join(out, 'report.md'), markdown(report));
    console.log(JSON.stringify({ passed: report.passed, text: `${report.text.passed}/${report.text.planned}`, media: `${report.media.passed}/${report.media.planned}`, report: path.join(out, 'report.md') }));
  }
  process.exitCode = report.passed ? 0 : 1;
}

main().catch(error => { console.error(error.message); process.exitCode = 2; });
