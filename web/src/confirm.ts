import { reactive } from 'vue';
export const confirmation=reactive({message:''});
let resolve:((value:boolean)=>void)|undefined;
export function ask(message:string):Promise<boolean>{
 if(resolve)return Promise.resolve(false);
 confirmation.message=message;
 return new Promise(done=>{resolve=done});
}
export function finishConfirmation(value:boolean){const done=resolve;resolve=undefined;confirmation.message='';done?.(value)}
