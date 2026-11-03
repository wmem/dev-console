// 验证单文件无工具链运行、数据库重启、停机备份与恢复；不连接真实设备。
import {spawn,spawnSync} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {mkdtempSync,writeFileSync,cpSync,rmSync,mkdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);
const binary=path.join(root,'dist/platform-linux-amd64');const work=mkdtempSync(path.join(tmpdir(),'devconsole-runtime-'));const password=randomBytes(24).toString('base64url');const base='http://127.0.0.1:18481';const env={...process.env,PATH:'/nonexistent'};let server;
async function stop(){if(server&&server.exitCode===null){server.kill('SIGTERM');await new Promise(r=>server.once('exit',r))};server=undefined}
async function start(data){const config=path.join(work,'config.yaml');writeFileSync(config,`listen: "127.0.0.1:18481"\npublic_base_url: "${base}"\ndata_dir: "${data}"\n`);server=spawn(binary,['serve','--config',config],{env,stdio:'ignore'});for(let n=0;n<50;n++){try{if((await fetch(base+'/healthz')).ok)return}catch{};await new Promise(r=>setTimeout(r,100))};throw Error('无工具链运行启动失败')}
let cookie='',csrf='';
async function call(method,url,body,token){const headers={'Content-Type':'application/json'};if(token)headers.Authorization='Bearer '+token;else{headers.Cookie=cookie;headers['X-CSRF-Token']=csrf};const res=await fetch(base+'/api/v1'+url,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});const text=await res.text();let data;try{data=JSON.parse(text)}catch{data=text};if(!res.ok)throw Error(`${method} ${url}失败：${res.status}`);return {res,data}}
try{
 const data=path.join(work,'data');const init=spawnSync(binary,['admin','bootstrap','--data-dir',data,'--username','runtime-admin','--password-stdin'],{env,input:password+'\n',encoding:'utf8'});if(init.status!==0)throw Error('初始化失败');
 const repeat=spawnSync(binary,['admin','bootstrap','--data-dir',data,'--username','another-admin','--password-stdin'],{env,input:password+'\n',encoding:'utf8'});if(repeat.status===0)throw Error('重复初始化管理员未拒绝');
 await start(data);const login=await call('POST','/auth/login',{username:'runtime-admin',password});cookie=login.res.headers.get('set-cookie').split(';')[0];csrf=login.data.csrf;
 const project=(await call('POST','/admin/projects',{key:'backup-verification',display_name:'备份恢复验收',enabled:true})).data;
 const device=(await call('POST','/admin/devices',{template_key:'itech-it6722',display_name:'离线恢复验证',connection_config:{path:'/tmp/devconsole-runtime-no-device'}})).data;
 await call('POST',`/admin/projects/${project.id}/services`,{key:'power',display_name:'输出操作',enabled:true,device_id:device.id,device_service_key:'output.set'});
 const issued=(await call('POST',`/admin/projects/${project.id}/tokens`,{name:'恢复验证'})).data;const secret=issued.secret;
 await stop();const backup=path.join(work,'backup'),restored=path.join(work,'restored');cpSync(data,backup,{recursive:true});cpSync(backup,restored,{recursive:true});await start(restored);
 const recovered=(await call('POST',`/admin/projects/${project.id}/tokens/${issued.token.id}/reveal`,{})).data;if(recovered.secret!==secret)throw Error('配套备份恢复后不能解密原Token');
 const sessionUser=await call('GET','/auth/me');if(sessionUser.data.user.username!=='runtime-admin')throw Error('会话未恢复');
 const docs=await call('GET','/projects/backup-verification/docs',undefined,secret);if(!docs.data.includes('/services/power/invoke'))throw Error('Token或绑定未恢复');
 const restoredDevices=(await call('GET','/admin/devices')).data;if(restoredDevices.length!==1||restoredDevices[0].id!==device.id||restoredDevices[0].availability!=='offline')throw Error('设备身份、离线或重启恢复错误');
 const unknown=await fetch(base+'/api/v1/not-found');if(unknown.status!==404||!unknown.headers.get('content-type').includes('application/json'))throw Error('未知API回退到页面');const spa=await fetch(base+'/projects/backup-verification');if(!spa.ok||!spa.headers.get('content-type').includes('text/html'))throw Error('嵌入页面深链接失败');
 mkdirSync('docs/evidence',{recursive:true});writeFileSync('docs/evidence/runtime-report.json',JSON.stringify({finished_at:new Date().toISOString(),passed:true,checks:['无Node或Go工具链PATH运行','管理员重复初始化拒绝','真实二进制认证及项目绑定','停机备份整个数据目录并恢复副本','重启保留Session及项目Token和绑定','配套恢复密钥后可再次复制同一Token','设备ID稳定且离线明确','未知API返回JSON404','嵌入式SPA深链接']},null,2));console.log('单文件运行、重启及停机备份恢复验收通过');
}finally{await stop();rmSync(work,{recursive:true,force:true})}
