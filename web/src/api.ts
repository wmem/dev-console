export let csrf='';
export function setCSRF(v:string){csrf=v}
export class ApiError extends Error { constructor(public code:string,message:string,public record?:any){super(message)} }
export async function api(path:string,method='GET',body?:any):Promise<any>{
 const res=await fetch('/api/v1'+path,{method,credentials:'same-origin',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:body===undefined?undefined:JSON.stringify(body)});
 const type=res.headers.get('content-type')||'';
 const value=type.includes('application/json')?await res.json():await res.text();
 if(res.status===401&&path!=='/auth/login')window.dispatchEvent(new Event('session-expired'));
 if(!res.ok)throw new ApiError(value.error?.code||'HTTP_ERROR',value.error?.message||'请求失败',value);
 return value;
}
