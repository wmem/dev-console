import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const args=process.argv.slice(2);
const hostOS=process.platform==='win32'?'windows':process.platform;
let target=`${hostOS}/${process.arch==='x64'?'amd64':process.arch==='arm64'?'arm64':process.arch}`;
if(args.length){if(args.length!==2||args[0]!=='--target'){throw new Error('用法：npm run build -- --target linux/amd64')};target=args[1]}
if(!['linux/amd64','linux/arm64','windows/amd64'].includes(target))throw new Error('不支持的构建目标');
function run(cmd,argv,options={}){const r=spawnSync(cmd,argv,{cwd:root,stdio:'inherit',...options});if(r.error)throw r.error;if(r.status!==0)process.exit(r.status||1)}
run('go',['version']);
if(Number(process.versions.node.split('.')[0])<24)throw new Error('需要Node.js24或以上');
const npm=process.platform==='win32'?'npm.cmd':'npm';
rmSync(path.join(root,'web/dist'),{recursive:true,force:true});
run(npm,['ci','--no-audit','--no-fund'],{cwd:path.join(root,'web')});
run(npm,['run','build'],{cwd:path.join(root,'web')});
if(!readFileSync(path.join(root,'web/dist/index.html'),'utf8').includes('/assets/'))throw new Error('缺少正式前端资源');
run('go',['mod','download']);
mkdirSync(path.join(root,'dist'),{recursive:true});
const [GOOS,GOARCH]=target.split('/');
run('go',['build','-trimpath','-o',`dist/platform-${GOOS}-${GOARCH}${GOOS==='windows'?'.exe':''}`,'./cmd/platform'],{env:{...process.env,CGO_ENABLED:'0',GOOS,GOARCH}});
console.log(`已生成 dist/platform-${GOOS}-${GOARCH}`);
