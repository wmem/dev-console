// 使用指定Chromium复现HTTP200但浏览器拒绝10080，并验证最终Go程序的可访问端口。
import {spawn,spawnSync} from 'node:child_process';
import {existsSync,mkdirSync,mkdtempSync,writeFileSync,rmSync} from 'node:fs';
import {homedir} from 'node:os';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);
const executable=process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE||path.join(homedir(),'work/software/playwright-browsers/chromium-1237/chrome-linux64/chrome');
const binary=path.join(root,'dist/platform-linux-amd64');if(!existsSync(executable)||!existsSync(binary))throw Error('需要已有指定Chromium及已构建程序，不会自动安装');
mkdirSync('.cache',{recursive:true});mkdirSync('docs/evidence',{recursive:true});const work=mkdtempSync(path.join(root,'.cache/browser-port-'));const session=`dconsole-browser-port-${process.pid}`;let server,badServer;
function run(cmd,args){const r=spawnSync(cmd,args,{encoding:'utf8',maxBuffer:4*1024*1024});if(r.error)throw r.error;if(r.status!==0)throw Error(r.stderr+r.stdout);return r.stdout}
try{
 badServer=http.createServer((req,res)=>{res.writeHead(200,{'Content-Type':'text/plain'});res.end('HTTP服务可用，浏览器应在发送请求前拒绝10080')});await new Promise((resolve,reject)=>badServer.once('error',reject).listen(10080,'127.0.0.1',resolve));
 const rawStatus=await new Promise((resolve,reject)=>http.get('http://127.0.0.1:10080/',res=>{res.resume();resolve(res.statusCode)}).once('error',reject));if(rawStatus!==200)throw Error('禁用端口的HTTP对照服务不可用');
 let fetchError='';try{await fetch('http://127.0.0.1:10080/')}catch(e){fetchError=e.cause?.message||e.message}if(fetchError!=='bad port')throw Error('Fetch没有复现bad port');
 const listener=net.createServer();await new Promise((resolve,reject)=>listener.once('error',reject).listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
 const base=`http://127.0.0.1:${port}`;const config=path.join(work,'config.yaml');writeFileSync(config,`listen: "127.0.0.1:${port}"\npublic_base_url: "${base}"\ndata_dir: "${work}/data"\n`);
 server=spawn(binary,['serve','--config',config],{stdio:'ignore'});let ready=false;for(let n=0;n<50;n++){try{if((await fetch(base+'/healthz')).ok){ready=true;break}}catch{};await new Promise(resolve=>setTimeout(resolve,100))};if(!ready)throw Error('最终程序测试实例未就绪');
 const browserConfig=path.join(work,'playwright.json');writeFileSync(browserConfig,JSON.stringify({browser:{browserName:'chromium',launchOptions:{executablePath:executable,headless:true,chromiumSandbox:false},contextOptions:{viewport:{width:1440,height:900}}},outputDir:path.join(work,'browser-output')}));
 run('playwright-cli',[`-s=${session}`,'open','about:blank','--config',browserConfig]);
 const code=`async(page)=>{const badPage=await page.context().newPage();let blocked='';try{await badPage.goto('http://127.0.0.1:10080/',{timeout:5000})}catch(e){blocked=e.message}finally{await badPage.close()}if(!blocked.includes('ERR_UNSAFE_PORT'))throw Error('没有复现Chromium端口阻断：'+blocked);await page.goto(${JSON.stringify(base)},{waitUntil:'networkidle'});await page.getByRole('heading',{name:'欢迎回来'}).waitFor();await page.screenshot({path:${JSON.stringify(path.join(root,'docs/evidence/browser-port-desktop.png'))},fullPage:true});const response=await page.request.get(${JSON.stringify(base+'/healthz')});if(response.status()!==200)throw Error('可访问端口健康检查失败');const mobile=await page.context().browser().newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true});try{const p=await mobile.newPage();await p.goto(${JSON.stringify(base)},{waitUntil:'networkidle'});await p.getByRole('heading',{name:'欢迎回来'}).waitFor();await p.screenshot({path:${JSON.stringify(path.join(root,'docs/evidence/browser-port-mobile.png'))},fullPage:true})}finally{await mobile.close()}return {passed:true,browser:page.context().browser().version(),blocked_error:blocked,allowed_port:${port},views:['1440x900','390x844触摸模拟'],health_status:response.status()}}`;
 const filename=path.join(work,'check.js');writeFileSync(filename,code);const output=run('playwright-cli',[`-s=${session}`,'run-code',`--filename=${filename}`]);const match=output.match(/### Result\s*\n([^\n]+)/);if(!match)throw Error(output);const result=JSON.parse(match[1]);if(!result.passed)throw Error('浏览器验证未通过');
 writeFileSync('docs/evidence/browser-port-report.json',JSON.stringify({finished_at:new Date().toISOString(),browser_executable:executable,blocked_port:10080,raw_http_status:rawStatus,fetch_error:fetchError,result,scope:'10080使用独立HTTP对照服务；可访问端口使用最终Go程序嵌入页面；未执行登录或设备操作，手机为视口触摸模拟'},null,2)+'\n');console.log('浏览器禁用端口复现及桌面/手机可访问端口页面验证通过');
}finally{
 try{run('playwright-cli',[`-s=${session}`,'close'])}catch{}
 if(server&&server.exitCode===null){server.kill('SIGTERM');await new Promise(resolve=>server.once('exit',resolve))}
 if(badServer)await new Promise(resolve=>badServer.close(resolve));rmSync(work,{recursive:true,force:true});
}
