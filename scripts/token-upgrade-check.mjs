// 跨格式升级回归需要真实旧版安装包；不会把当前源码改版本号冒充旧实现。
import {spawnSync} from 'node:child_process';
import {existsSync,mkdtempSync,rmSync,writeFileSync,mkdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);const args=process.argv.slice(2);
if(args.length&& (args.length!==2||args[0]!=='--legacy-package'))throw Error('用法：npm run test:token-upgrade -- --legacy-package <格式1安装包>');
const legacy=path.resolve(args[1]||'dist/dconsole-0.1.2-install.run'),current=path.join(root,'dist/platform-linux-amd64');if(!existsSync(legacy)||!existsSync(current))throw Error('需要已归档的0.1.x安装包和当前已构建程序；使用--legacy-package指定旧包，不会自动下载');
const work=mkdtempSync(path.join(tmpdir(),'dconsole-token-upgrade-'));
function run(cmd,args,options={}){const r=spawnSync(cmd,args,{encoding:'utf8',maxBuffer:8*1024*1024,...options});if(r.error)throw r.error;if(r.status!==0)throw Error(r.stderr+r.stdout);return r.stdout}
try{
 const extracted=path.join(work,'legacy');run('bash',[legacy,'--extract',extracted]);const oldBinary=path.join(extracted,'dconsole');const oldInfo=JSON.parse(run(oldBinary,['version'])),newInfo=JSON.parse(run(current,['version']));
 const output=run('go',['test','-race','-count=1','-json','-run','^TestLegacyInstallerTokenUpgradeRollback$','./internal/installer'],{env:{...process.env,DCONSOLE_TEST_LEGACY:oldBinary,DCONSOLE_TEST_CURRENT:current}});
 const events=output.trim().split('\n').map(s=>JSON.parse(s));if(!events.some(e=>e.Action==='pass'&&e.Test==='TestLegacyInstallerTokenUpgradeRollback')||events.some(e=>e.Action==='skip'||e.Action==='fail'))throw Error('真实旧版升级回归没有完整通过');
 mkdirSync('docs/evidence',{recursive:true});writeFileSync('docs/evidence/token-upgrade-report.json',JSON.stringify({finished_at:new Date().toISOString(),passed:true,legacy:oldInfo,current:newInfo,checks:['使用真实格式1旧程序初装并创建旧Token','真实升级到格式2后故障注入，安装器恢复旧二进制及格式1数据','失败迁移密钥随failed-data保留，恢复目录没有错配密钥','成功升级后旧Token继续鉴权，管理员可补存同一原文','新Token加密保存并可恢复','停机备份包含密钥；密钥丢失时拒绝启动且不自动重建','恢复配套密钥后原加密Token可恢复','真实旧程序拒绝读取格式2数据库'],limits:['临时目录及真实程序/SQLite，替代系统账号、chown和系统级systemctl','未在目标机器执行root升级或真实硬件操作']},null,2)+'\n');console.log('真实旧版Token迁移、跨格式安装回退及密钥恢复验证通过');
}finally{rmSync(work,{recursive:true,force:true})}
