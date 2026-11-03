// 使用已有playwright-cli和指定Chromium验证最终单程序页面，不下载浏览器或修改全局配置。
import {spawn,spawnSync} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {mkdirSync,mkdtempSync,readFileSync,writeFileSync,rmSync,existsSync,symlinkSync} from 'node:fs';
import {networkInterfaces,homedir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);
const executable=process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE||path.join(homedir(),'work/software/playwright-browsers/chromium-1237/chrome-linux64/chrome');
if(!existsSync(executable))throw Error('缺少指定Chromium：'+executable+'；请配置PLAYWRIGHT_CHROMIUM_EXECUTABLE指向指定目录中的已有浏览器');
function run(cmd,args,input){const r=spawnSync(cmd,args,{cwd:root,input,encoding:'utf8',maxBuffer:8*1024*1024});if(r.error)throw r.error;if(r.status!==0)throw Error((r.stderr+r.stdout).slice(0,6000));return r.stdout}
run('playwright-cli',['--version']);if(!existsSync('dist/platform-linux-amd64'))throw Error('请先执行npm run build -- --target linux/amd64');
mkdirSync('.cache',{recursive:true});mkdirSync('docs/evidence',{recursive:true});run('go',['build','-o','.cache/browser-simulator','./cmd/simulator']);
const localIP=Object.values(networkInterfaces()).flat().find(a=>a&&a.family==='IPv4'&&!a.internal)?.address||'127.0.0.1';
const results=[];
const allSecrets=[];
function redact(text){for(const secret of allSecrets)text=text.replaceAll(secret,'<REDACTED>');return text.replace(/dc_[a-f0-9]{64}/g,'<REDACTED_TOKEN>')}

for(const mode of ['desktop','mobile','mobile-narrow']){
 const work=mkdtempSync(path.join(root,'.cache/browser-suite-'));const port=18480;const baseURL=`http://${localIP}:${port}`;const mobile=mode!=='desktop';const viewport=mobile?{width:mode==='mobile'?390:360,height:mode==='mobile'?844:800}:{width:1440,height:900};
 const secrets=[randomBytes(24).toString('base64url'),randomBytes(24).toString('base64url'),randomBytes(24).toString('base64url')];
 allSecrets.push(...secrets);
 const session='devconsole-'+mode+'-'+Date.now();let server,simulator;
 try{
  simulator=spawn('./.cache/browser-simulator',['-protocol','scpi','-delay','80ms'],{cwd:root,stdio:['ignore','pipe','pipe']});
  const serial=await new Promise((resolve,reject)=>{simulator.stdout.once('data',b=>resolve(b.toString().trim()));simulator.once('error',reject);simulator.once('exit',code=>reject(Error('模拟器退出：'+code)))});
  const aliasDir=path.join(work,'serial','by-path');mkdirSync(aliasDir,{recursive:true});const stableSerial=path.join(aliasDir,'fixed-port');const reselectedSerial=path.join(aliasDir,'reselected-port');symlinkSync(serial,stableSerial);symlinkSync(serial,reselectedSerial);
  run('./dist/platform-linux-amd64',['admin','bootstrap','--data-dir',path.join(work,'data'),'--username','browser-admin','--password-stdin'],secrets[0]+'\n');
  const config=path.join(work,'server.yaml');writeFileSync(config,`listen: "0.0.0.0:${port}"\npublic_base_url: "${baseURL}"\ndata_dir: "${path.join(work,'data')}"\n`);
  server=spawn('./dist/platform-linux-amd64',['serve','--config',config],{cwd:root,stdio:'ignore'});
  let ready=false;for(let n=0;n<50;n++){try{if((await fetch(baseURL+'/healthz')).ok){ready=true;break}}catch{};await new Promise(r=>setTimeout(r,100))};if(!ready)throw Error('验收服务启动失败');
  const browserConfig=path.join(work,'playwright.json');writeFileSync(browserConfig,JSON.stringify({browser:{browserName:'chromium',launchOptions:{executablePath:executable,headless:true,chromiumSandbox:false},contextOptions:{viewport,isMobile:mobile,hasTouch:mobile,deviceScaleFactor:1}},outputDir:path.join(work,'playwright-output')}));
  run('playwright-cli',['-s='+session,'open',baseURL,'--config',browserConfig]);
  const fixture={mode,mobile,serial,stableSerial,reselectedSerial,username:'browser-admin',password:secrets[0],memberPassword:secrets[1],resetPassword:secrets[2],baseURL,evidence:path.join(root,'docs/evidence')};
  const code=readFileSync('scripts/browser-flow.js','utf8').replace('__FIXTURE__',JSON.stringify(fixture));const filename=path.join(work,'flow.js');writeFileSync(filename,code,{mode:0o600});
  let output;try{output=run('playwright-cli',['-s='+session,'run-code','--filename='+filename])}catch(e){let message=e.message;for(const s of secrets)message=message.replaceAll(s,'<REDACTED>');throw Error(message.replace(/dc_[a-f0-9]{64}/g,'<REDACTED_TOKEN>'))}
  writeFileSync(path.join(work,'cli-output-redacted.log'),redact(output));
  const match=output.match(/### Result\s*\n([^\n]+)/);if(!match)throw Error('页面测试未返回可确认结果：'+redact(output.slice(-6000)));const result=JSON.parse(match[1]);if(!result.passed)throw Error('页面测试失败');results.push(result);console.log(mode+'：页面流程通过');
 }finally{
  try{run('playwright-cli',['-s='+session,'close'])}catch{}
  if(server&&server.exitCode===null){server.kill('SIGTERM');await new Promise(r=>server.once('exit',r))}
  if(simulator&&simulator.exitCode===null){simulator.kill('SIGTERM');await new Promise(r=>simulator.once('exit',r))}
  rmSync(work,{recursive:true,force:true});
 }
}
writeFileSync('docs/evidence/browser-report.json',JSON.stringify({finished_at:new Date().toISOString(),browser_executable:executable,scope:'Chromium桌面与手机视口及触摸模拟；未使用实体手机或第二台终端',results},null,2));
console.log('已保存浏览器结果与脱敏截图：docs/evidence/');
