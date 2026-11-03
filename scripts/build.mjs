import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const args=process.argv.slice(2);
if(args.length && (args.length!==2 || args[0]!=='--target' || args[1]!=='linux/amd64'))throw Error('仅支持Linux/amd64；用法：npm run build');
if(process.platform!=='linux'||process.arch!=='x64')throw Error('构建环境仅支持Linux/amd64');
const version=readFileSync(path.join(root,'VERSION'),'utf8').trim();
if(!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version)||version.split('.').some(n=>BigInt(n)>4294967295n))throw Error('VERSION需为主版本.次版本.修订版本，每段不超过32位无符号整数');
function run(cmd,argv,options={}){const r=spawnSync(cmd,argv,{cwd:root,stdio:'inherit',...options});if(r.error)throw r.error;if(r.status!==0)throw Error(`${cmd}执行失败，退出码${r.status}`);return r}
function git(args){return run('git',args,{encoding:'utf8',stdio:['ignore','pipe','inherit']}).stdout.trim()}
const commit=git(['rev-parse','HEAD']);
const dirty=git(['status','--porcelain','--untracked-files=normal'])!=='';
run('go',['version']);
if(Number(process.versions.node.split('.')[0])<24)throw new Error('需要Node.js24或以上');
rmSync(path.join(root,'web/dist'),{recursive:true,force:true});
run('npm',['ci','--no-audit','--no-fund'],{cwd:path.join(root,'web')});
run('npm',['run','build'],{cwd:path.join(root,'web')});
if(!readFileSync(path.join(root,'web/dist/index.html'),'utf8').includes('/assets/'))throw new Error('缺少正式前端资源');
run('go',['mod','download']);
mkdirSync(path.join(root,'dist'),{recursive:true});
const flags=`-X devconsole/internal/buildinfo.Version=${version} -X devconsole/internal/buildinfo.Commit=${commit} -X devconsole/internal/buildinfo.Dirty=${dirty}`;
run('go',['build','-trimpath','-ldflags',flags,'-o','dist/platform-linux-amd64','./cmd/platform'],{env:{...process.env,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64'}});
console.log(`已生成 dist/platform-linux-amd64，版本${version}，提交${commit}${dirty?'（含未提交修改）':''}`);
