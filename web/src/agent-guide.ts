// 只在管理员的临时交接窗口组合真实凭据，不写入普通项目文档或浏览器持久存储。
export function agentGuide(project:any, token:any, secret:string, baseURL:string):string {
 if(!token?.scopes?.includes('docs:read'))return '';
 const base=baseURL.replace(/\/$/,''),url=base+'/api/v1/projects/'+project.key;
 const quote=(value:string)=>"'"+value.replaceAll("'","'\\''")+"'";
 return `# 项目 Agent 接入说明

项目：${JSON.stringify(project.display_name)}（${project.key}）
文档地址：${url}/docs
OpenAPI 地址：${url}/openapi.json
Access Token：${secret}
权限：${token.scopes.join('、')}
有效期：${token.expires_at||'不设到期时间'}

所有项目请求使用请求头 Authorization: Bearer <Access Token>。不需要网页登录，也不需要管理员账号。
请先读取项目文档，再按其中的项目服务名称、参数和限制调用；读取命令如下，可直接执行：

\`\`\`sh
curl --fail-with-body --silent --show-error \\
  --header ${quote('Authorization: Bearer '+secret)} \\
  ${quote(url+'/docs')}
\`\`\`

需要结构化接口定义时，以相同鉴权方式读取上面的 OpenAPI 地址。
调用路径为 POST ${url}/services/<服务标识>/invoke，请求JSON为 {"parameters":{...}}，具体内容以项目文档为准。
本Token${token.scopes.includes('services:invoke')?'具有调用项目服务的权限':'不包含services:invoke，只能执行已授予权限的读取'}。
通信冲突返回409 RESOURCE_BUSY，不排队、不自动重试；设置超时可能表示结果未知，不能盲目重发。
`;
}
