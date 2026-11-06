import {spawn, spawnSync} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {homedir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
const executable = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || path.join(homedir(), 'work/software/playwright-browsers/chromium-1237/chrome-linux64/chrome');
if (!existsSync(executable)) throw Error('需要已安装的 Chromium，请设置 PLAYWRIGHT_CHROMIUM_EXECUTABLE');
if (!existsSync('dist/platform-linux-amd64')) throw Error('请先执行 npm run build');
const evidence = path.resolve(process.env.DCONSOLE_EVIDENCE_DIR || 'docs/evidence/xiaomi-' + new Date().toISOString().replace(/[:.]/g, '-'));
mkdirSync('.cache', {recursive: true}); mkdirSync(evidence, {recursive: true});
function run(cmd, args, input) {
  const result = spawnSync(cmd, args, {cwd: root, input, encoding: 'utf8', maxBuffer: 8 * 1024 * 1024});
  if (result.error) throw result.error;
  if (result.status !== 0) throw Error((result.stderr + result.stdout).slice(0, 6000));
  return result.stdout;
}
run('go', ['build', '-o', '.cache/xiaomi-browser-server', 'scripts/xiaomi-browser-server.go']);
const results = [];
for (const mode of ['desktop', 'mobile-narrow']) {
  const work = mkdtempSync(path.join(root, '.cache/xiaomi-browser-'));
  const baseURL = 'http://127.0.0.1:18482';
  const password = randomBytes(24).toString('base64url');
  const session = 'xiaomi-' + mode + '-' + Date.now();
  const viewport = mode === 'desktop' ? {width: 1440, height: 900} : {width: 360, height: 800};
  let server;
  try {
    run('./dist/platform-linux-amd64', ['admin', 'bootstrap', '--data-dir', path.join(work, 'data'), '--username', 'xiaomi-fixture', '--password-stdin'], password + '\n');
    server = spawn('./.cache/xiaomi-browser-server', ['--data-dir', path.join(work, 'data')], {cwd: root, stdio: 'ignore'});
    let ready = false;
    for (let n = 0; n < 50; n++) {
      try { if ((await fetch(baseURL + '/healthz')).ok) { ready = true; break; } } catch {}
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    if (!ready) throw Error('隔离米家验收服务未启动');
    const config = path.join(work, 'playwright.json');
    writeFileSync(config, JSON.stringify({browser: {browserName: 'chromium', launchOptions: {executablePath: executable, headless: true, chromiumSandbox: false}, contextOptions: {viewport, isMobile: mode !== 'desktop', hasTouch: mode !== 'desktop'}}, outputDir: path.join(work, 'browser-output')}));
    run('playwright-cli', ['-s=' + session, 'open', baseURL, '--config', config]);
    const fixture = {baseURL, password, mode, evidence};
    const flow = readFileSync('scripts/xiaomi-browser-flow.js', 'utf8').replace('__FIXTURE__', JSON.stringify(fixture));
    const file = path.join(work, 'flow.js'); writeFileSync(file, flow, {mode: 0o600});
    const output = run('playwright-cli', ['-s=' + session, 'run-code', '--filename=' + file]).replaceAll(password, '<REDACTED>');
    const match = output.match(/### Result\s*\n([^\n]+)/);
    if (!match) throw Error('浏览器未返回测试结果：' + output.slice(-3000));
    const result = JSON.parse(match[1]); if (!result.passed) throw Error('米家页面验收失败');
    results.push(result); console.log(mode + '：米家扫码、预览、导入与开关调用通过');
  } catch (e) {
    throw Error(String(e.message).replaceAll(password, '<REDACTED>'));
  } finally {
    try { run('playwright-cli', ['-s=' + session, 'close']); } catch {}
    if (server && server.exitCode === null) { server.kill('SIGTERM'); await new Promise(resolve => server.once('exit', resolve)); }
    rmSync(work, {recursive: true, force: true});
  }
}
writeFileSync(path.join(evidence, 'report.json'), JSON.stringify({finished_at: new Date().toISOString(), scope: '真实核心、HTTP与嵌入Vue页面；米家云、扫码确认、设备与二维码图片均为隔离模拟；未访问真实账号或设备', browser_executable: executable, results}, null, 2) + '\n');
console.log('结果保存在 ' + path.relative(root, evidence));
