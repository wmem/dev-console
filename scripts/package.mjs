// 自解压包只含一个gzip压缩的静态程序；服务模板与安装器随程序嵌入。
import {spawnSync} from 'node:child_process';
import {readFileSync,writeFileSync,chmodSync,rmSync,renameSync} from 'node:fs';
import {gzipSync} from 'node:zlib';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');process.chdir(root);
const args=process.argv.slice(2);if(args.length && (args.length!==1||args[0]!=='--release'))throw Error('用法：npm run package [-- --release]');
function capture(cmd,args){const r=spawnSync(cmd,args,{encoding:'utf8'});if(r.error)throw r.error;if(r.status!==0)throw Error(`${cmd}失败：${r.stderr}`);return r.stdout.trim()}
const version=readFileSync('VERSION','utf8').trim();
if(args.includes('--release')){
 if(capture('git',['status','--porcelain','--untracked-files=normal']))throw Error('正式发布要求工作区干净，请先提交修改');
 const tagged=capture('git',['rev-parse',`refs/tags/v${version}^{commit}`]);if(tagged!==capture('git',['rev-parse','HEAD']))throw Error(`正式发布要求当前提交标签为v${version}`);
 if(!readFileSync('CHANGELOG.md','utf8').includes(`## ${version}\n`))throw Error('CHANGELOG缺少当前版本记录');
}
const build=spawnSync(process.execPath,['scripts/build.mjs'],{stdio:'inherit'});if(build.status!==0)process.exit(build.status||1);
const info=JSON.parse(capture(path.join(root,'dist/platform-linux-amd64'),['version']));if(info.version!==version||info.target!=='linux/amd64')throw Error('构建版本与包名不一致，未生成安装包');
if(args.includes('--release')&&(info.dirty||capture('git',['status','--porcelain','--untracked-files=normal'])||info.commit!==capture('git',['rev-parse','HEAD'])))throw Error('发布构建期间工作区发生变化，未生成安装包');
const header=readFileSync('deploy/self-extract.sh','utf8');
if(!header.endsWith('__DCONSOLE_PAYLOAD__\n'))throw Error('自解压模板缺少有效边界');
const start=header.split('\n').length;
const prepared=header.replace('@PAYLOAD_LINE@',String(start));
const output=`dist/dconsole-${version}-install.run`,tmp=output+'.tmp';
try{writeFileSync(tmp,Buffer.concat([Buffer.from(prepared),gzipSync(readFileSync('dist/platform-linux-amd64'),{level:9})]),{mode:0o755});chmodSync(tmp,0o755);renameSync(tmp,output)}finally{rmSync(tmp,{force:true})}
console.log(`已生成 ${output}`);
