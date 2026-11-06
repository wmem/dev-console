export function serviceGroups(services:any[]) {
  const groups = new Map<string, {key:string,title:string,kind:string,services:any[]}>();
  for (const service of services) {
    const key = service.group_key || 'services';
    if (!groups.has(key)) groups.set(key, {key, title:service.group_title || '', kind:service.group_kind || '', services:[]});
    groups.get(key)!.services.push(service);
  }
  return [...groups.values()];
}
export function serviceSummary(services:any[]) {
  const groups = serviceGroups(services).filter(g => g.kind);
  if (!groups.length) return `${services.length} 项设备服务`;
  const switches = groups.filter(g => g.kind === 'switch').length;
  return [switches ? `${switches} 路开关` : '', groups.some(g => g.kind === 'master') ? '总控' : '', groups.some(g => g.kind === 'indicator') ? '指示灯' : ''].filter(Boolean).join(' · ');
}
