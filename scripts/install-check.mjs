// 验证真实安装包、真实程序及SQLite；特权事务由Go测试在临时目录执行，另验证用户级systemd。
import {spawnSync} from 'node:child_process';
import {readFileSync,writeFileSync,mkdtempSync,mkdirSync,copyFileSync,rmSync,existsSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {randomBytes} from 'node:crypto';
import net from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);
const version=readFileSync('VERSION','utf8').trim(),archive=path.join(root,`dist/dconsole-${version}-install.run`);
if(!existsSync(archive))throw Error('请先执行npm run package');
const work=mkdtempSync(path.join(tmpdir(),'dconsole-install-check-'));const checks=[];let unit;
function run(cmd,args,options={}){const r=spawnSync(cmd,args,{encoding:'utf8',maxBuffer:16*1024*1024,...options});if(r.error)throw r.error;if(r.status!==0)throw Error(`${cmd}失败：${r.stderr}\n${r.stdout}`);return r.stdout}
function reject(cmd,args,options={}){const r=spawnSync(cmd,args,{encoding:'utf8',...options});if(r.error)throw r.error;if(r.status===0)throw Error(`${cmd}未拒绝错误场景`)}
try{
 const extracted=path.join(work,'payload with spaces');run('bash',[archive,'--extract',extracted]);const binary=path.join(extracted,'dconsole');
 if(!readFileSync(binary).equals(readFileSync('dist/platform-linux-amd64')))throw Error('解压程序与构建产物不一致');
 const info=JSON.parse(run(binary,['version'],{env:{...process.env,PATH:'/nonexistent'}}));if(info.version!==version||info.target!=='linux/amd64')throw Error('内嵌版本或目标错误');
 run('bash',[archive,'--help']);const archiveInfo=JSON.parse(run('bash',[archive,'--version']));if(archiveInfo.version!==version)throw Error('包内版本不匹配');
 const invalidPort=spawnSync('bash',[archive,'--port','65536'],{encoding:'utf8'});if(invalidPort.status===0||!invalidPort.stderr.includes('端口必须'))throw Error('安装包未在执行前拒绝非法端口');checks.push('安装包端口参数校验');
 reject('bash',[archive,'--extract',extracted]);
 const broken=path.join(work,'broken.run');const contents=readFileSync(archive);writeFileSync(broken,contents.subarray(0,contents.length-64));const brokenDir=path.join(work,'broken');reject('bash',[broken,'--extract',brokenDir]);if(existsSync(brokenDir))throw Error('损坏包留下了解压目录');
 if(process.getuid()!==0)reject('bash',[archive,'--reinstall'],{input:''});
 checks.push('真实run包解压、含空格路径、版本与二进制一致','无Go/Node的PATH运行版本查询','损坏包和已有解压目录拒绝','安装帮助无需root、非root安装拒绝');
 reject(process.execPath,['scripts/build.mjs','--target','linux/arm64']);
 checks.push('拒绝非Linux/amd64构建目标');
 const fixtureVersions=['0.0.1','0.0.2','0.0.3'],binaries=[];
 for(const v of fixtureVersions){const target=path.join(work,`dconsole-${v}`);run('go',['build','-trimpath','-ldflags',`-X devconsole/internal/buildinfo.Version=${v} -X devconsole/internal/buildinfo.Commit=install-regression -X devconsole/internal/buildinfo.Dirty=true`,'-o',target,'./cmd/platform'],{env:{...process.env,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64'}});binaries.push(target)}
 const output=run('go',['test','-race','-count=1','-json','./internal/installer'],{env:{...process.env,DCONSOLE_TEST_OLD:binaries[0],DCONSOLE_TEST_NEW:binaries[1],DCONSOLE_TEST_FAILED:binaries[2]}});
 const events=output.trim().split('\n').map(s=>JSON.parse(s));const passed=events.filter(e=>e.Action==='pass'&&e.Test).map(e=>e.Test);if(events.some(e=>e.Action==='skip')||passed.length<9)throw Error('安装事务测试没有完整运行');
 checks.push(...passed);
 // 在独立Git仓库检查发布门禁；该处替代编译步骤，真实构建与打包已由前面的run包验证。
 const policy=path.join(work,'release-policy');mkdirSync(policy);for(const name of ['scripts','deploy'])mkdirSync(path.join(policy,name));
 copyFileSync('scripts/package.mjs',path.join(policy,'scripts/package.mjs'));copyFileSync('deploy/self-extract.sh',path.join(policy,'deploy/self-extract.sh'));
 writeFileSync(path.join(policy,'VERSION'),'0.0.1\n');writeFileSync(path.join(policy,'CHANGELOG.md'),'# 版本\n\n## 0.0.1\n');writeFileSync(path.join(policy,'.gitignore'),'dist/\n');
 writeFileSync(path.join(policy,'scripts/build.mjs'),"import fs from 'node:fs';fs.mkdirSync('dist',{recursive:true});fs.copyFileSync(process.env.DCONSOLE_POLICY_BINARY,'dist/platform-linux-amd64');if(process.env.DCONSOLE_POLICY_MUTATE)fs.writeFileSync('VERSION','0.0.2\\n');\n");
 const git=(...args)=>run('git',['-C',policy,'-c','user.name=安装测试','-c','user.email=install-test@example.invalid',...args]);
 git('init','--quiet');git('add','.');git('commit','--quiet','-m','发布门禁测试');
 const policyScript=path.join(policy,'scripts/package.mjs');reject(process.execPath,[policyScript,'--release']);git('tag','v0.0.1');
 const policyBinary=path.join(work,'policy-binary'),policyCommit=git('rev-parse','HEAD').trim();
 run('go',['build','-trimpath','-ldflags',`-X devconsole/internal/buildinfo.Version=0.0.1 -X devconsole/internal/buildinfo.Commit=${policyCommit} -X devconsole/internal/buildinfo.Dirty=false`,'-o',policyBinary,'./cmd/platform'],{env:{...process.env,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64'}});
 const policyEnv={...process.env,DCONSOLE_POLICY_BINARY:policyBinary};run(process.execPath,[policyScript,'--release'],{env:policyEnv});
 if(!existsSync(path.join(policy,'dist/dconsole-0.0.1-install.run')))throw Error('符合发布门禁的构建未打包');
 reject(process.execPath,[policyScript,'--release'],{env:{...policyEnv,DCONSOLE_POLICY_MUTATE:'1'}});
 reject(process.execPath,[policyScript,'--release'],{env:policyEnv});
 writeFileSync(path.join(policy,'VERSION'),'0.0.1\n');writeFileSync(path.join(policy,'CHANGELOG.md'),'# 版本\n\n## 0.0.1\n\n追加说明\n');git('add','.');git('commit','--quiet','-m','制造标签未指向当前提交');reject(process.execPath,[policyScript,'--release'],{env:policyEnv});
 checks.push('发布门禁：干净工作区和正确标签允许打包','发布门禁：缺失标签、错误标签及未提交修改拒绝','发布门禁：构建期间修改版本文件拒绝');
 // 使用临时实例和用户管理器，不改系统账号、/opt、/etc或既有体验服务。
 const systemdRoot=path.join(work,'systemd-root');mkdirSync(systemdRoot);for(const name of ['data','logs'])mkdirSync(path.join(systemdRoot,name),{mode:0o700});copyFileSync(binary,path.join(systemdRoot,'dconsole'));
 const listener=net.createServer();await new Promise((resolve,reject)=>listener.once('error',reject).listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
 const base=`http://127.0.0.1:${port}`;writeFileSync(path.join(systemdRoot,'config.yaml'),`listen: "127.0.0.1:${port}"\npublic_base_url: "${base}"\ndata_dir: "${systemdRoot}/data"\n`,{mode:0o600});
 const service=readFileSync('deploy/dconsole.service','utf8').replaceAll('/opt/dconsole',systemdRoot);const verifyPath=path.join(work,'dconsole.service');writeFileSync(verifyPath,service);run('systemd-analyze',['verify',verifyPath]);checks.push('系统级服务模板通过systemd-analyze语法及路径检查');
 const password=randomBytes(24).toString('base64url');run(path.join(systemdRoot,'dconsole'),['admin','bootstrap','--data-dir',path.join(systemdRoot,'data'),'--password-stdin'],{input:password+'\n'});
 unit=`dconsole-install-check-${process.pid}`;
 run('systemd-run',['--user',`--unit=${unit}`,'--collect','--property=Type=simple',`--property=WorkingDirectory=${systemdRoot}`,'--property=NoNewPrivileges=true','--property=UMask=0077',`--property=StandardOutput=append:${systemdRoot}/logs/dconsole.log`,`--property=StandardError=append:${systemdRoot}/logs/dconsole.log`,path.join(systemdRoot,'dconsole'),'serve','--config',path.join(systemdRoot,'config.yaml')]);
 let ready=false;for(let n=0;n<50;n++){try{const r=await fetch(base+'/healthz');const data=await r.json();if(data.version===version){ready=true;break}}catch{};await new Promise(r=>setTimeout(r,100))};if(!ready)throw Error('用户级systemd服务启动失败');
 const login=await fetch(base+'/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'admin',password})});if(!login.ok)throw Error('systemd实例真实登录失败');
 const page=await fetch(base+'/');if(!page.ok||!(await page.text()).includes('/assets/'))throw Error('systemd实例未提供嵌入页面');run('systemctl',['--user','is-active','--quiet',unit]);
 run('systemctl',['--user','stop',unit]);unit=undefined;if(!readFileSync(path.join(systemdRoot,'logs/dconsole.log'),'utf8').includes('平台启动'))throw Error('服务日志未写入目录');
 checks.push('真实用户级systemd启动、健康检查、登录、嵌入页面及目录内日志','用户级测试服务正常停止并清理');
 mkdirSync('docs/evidence',{recursive:true});writeFileSync('docs/evidence/install-report.json',JSON.stringify({finished_at:new Date().toISOString(),passed:true,version,build:info,checks,limits:['事务测试使用真实二进制及数据库，替代系统账号、chown和系统级systemctl','正式发布门禁使用独立Git仓库及替代编译步骤，真实编译打包由主路径验证','用户级systemd不覆盖系统级User/Group、串口补充组及文件系统隔离','当前无免密sudo且用户命名空间受限，未执行真实root首次安装或系统级开机启动验证','失败恢复使用当前代码的三个独立版本标记构建及故障注入，不代表未来数据库迁移已验收','未操作真实串口或现有体验服务']},null,2)+'\n');
 console.log(`安装验收通过，共${checks.length}项；真实root/systemd部署限制已记录`);
}finally{
 if(unit)spawnSync('systemctl',['--user','stop',unit],{stdio:'ignore'});
 rmSync(work,{recursive:true,force:true});
}
