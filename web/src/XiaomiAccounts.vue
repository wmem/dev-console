<script setup lang="ts">
import {computed, onMounted, onUnmounted, ref} from 'vue';
import {api} from './api';
import {ask} from './confirm';
import Dialog from './Dialog.vue';
import {serviceSummary} from './device-services';

const emit = defineEmits(['close', 'changed', 'message']);
const accounts = ref<any[]>([]);
const accountID = ref('');
const account = computed(() => accounts.value.find(a => a.id === accountID.value));
const busy = ref(false), error = ref(''), notice = ref('');
const creating = ref(false), loginName = ref(''), reauthID = ref(''), login = ref<any>(null);
const discovery = ref<any>(null), filter = ref(''), selected = ref<string[]>([]);
const previews = ref<Record<string, any>>({}), deviceErrors = ref<Record<string, string>>({});
const visibleDevices = computed(() => (discovery.value?.devices || []).filter((d:any) =>
  [d.name, d.model, d.home, d.room].join(' ').toLowerCase().includes(filter.value.toLowerCase())));
const ready = computed(() => selected.value.length > 0 && selected.value.every(id => previews.value[id] && !deviceErrors.value[id]));
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true, generation = 0;

function close() { if (!busy.value) emit('close'); }

async function loadAccounts() {
  const list = await api('/admin/xiaomi/accounts');
  if (alive) accounts.value = list;
}
onMounted(async () => {
  document.addEventListener('visibilitychange', visibility);
  busy.value = true;
  try { await loadAccounts(); accountID.value = accounts.value[0]?.id || ''; }
  catch (e:any) { error.value = e.message; }
  finally { busy.value = false; }
});
onUnmounted(() => {
  alive = false; generation++; clearTimeout(timer);
  document.removeEventListener('visibilitychange', visibility);
  if (login.value?.status === 'pending') api('/admin/xiaomi/logins/' + login.value.id, 'DELETE').catch(() => {});
});
function visibility() {
  clearTimeout(timer);
  if (!document.hidden && login.value?.status === 'pending') timer = setTimeout(poll, 0);
}
function resetDiscovery() {
  discovery.value = null; selected.value = []; previews.value = {}; deviceErrors.value = {}; filter.value = ''; error.value = ''; notice.value = '';
}
function addAccount(a?:any) {
  creating.value = true; reauthID.value = a?.id || ''; loginName.value = a?.display_name || ''; error.value = ''; notice.value = '';
}
async function cancelLogin() {
  generation++; clearTimeout(timer);
  const current = login.value;
  login.value = null;
  if (current?.status === 'pending') {
    try { await api('/admin/xiaomi/logins/' + current.id, 'DELETE'); } catch {}
  }
}
async function startLogin() {
  busy.value = true; error.value = '';
  await cancelLogin();
  try {
    const value = await api('/admin/xiaomi/logins', 'POST', {display_name: loginName.value, region: 'cn', account_id: reauthID.value});
    if (!alive) { api('/admin/xiaomi/logins/' + value.id, 'DELETE').catch(() => {}); return; }
    login.value = value;
    timer = setTimeout(poll, 1000);
  } catch (e:any) { error.value = e.message; }
  finally { busy.value = false; }
}
async function poll() {
  const current = login.value, version = generation;
  if (!current || current.status !== 'pending' || document.hidden || !alive) return;
  try {
    const result = await api('/admin/xiaomi/logins/' + current.id);
    if (!alive || version !== generation) return;
    login.value = result;
    if (result.status === 'succeeded') {
      await loadAccounts();
      if (!alive || version !== generation) return;
      accountID.value = result.account_id; resetDiscovery(); creating.value = false; login.value = null;
      notice.value = '小米账号已连接，可以刷新设备列表。';
      return;
    }
    if (result.status !== 'pending') return;
    error.value = '';
  } catch (e:any) {
    if (!alive || version !== generation) return;
    error.value = e.message;
    // 网络或会话失败后由用户明确重新扫码，避免后台无限请求。
    login.value = {...current, status: 'failed', error: '无法查询登录结果，请重新扫码'};
    return;
  }
  if (alive && version === generation && !document.hidden) timer = setTimeout(poll, 1500);
}
async function refreshAccount() {
  busy.value = true; error.value = '';
  try {
    await api('/admin/xiaomi/accounts/' + accountID.value + '/refresh', 'POST', {});
    await loadAccounts(); notice.value = '登录态已刷新。';
  } catch (e:any) { error.value = e.message; await loadAccounts().catch(() => {}); }
  finally { busy.value = false; }
}
async function removeAccount() {
  if (!account.value || !await ask('移除小米账号“' + account.value.display_name + '”？有已导入设备时会拒绝移除。')) return;
  busy.value = true; error.value = '';
  try {
    await api('/admin/xiaomi/accounts/' + accountID.value, 'DELETE');
    await loadAccounts(); accountID.value = accounts.value[0]?.id || ''; resetDiscovery();
  } catch (e:any) { error.value = e.message; }
  finally { busy.value = false; }
}
async function discover() {
  if (!accountID.value) return;
  busy.value = true; resetDiscovery();
  try { discovery.value = await api('/admin/xiaomi/accounts/' + accountID.value + '/discovery', 'POST', {}); }
  catch (e:any) { error.value = e.message; await loadAccounts().catch(() => {}); }
  finally { busy.value = false; }
}
async function preview() {
  busy.value = true; error.value = ''; notice.value = '';
  try {
    for (const did of selected.value) {
      deviceErrors.value[did] = '';
      try {
        const result = await api('/admin/xiaomi/accounts/' + accountID.value + '/preview', 'POST', {discovery_id: discovery.value.id, did});
        previews.value[did] = result;
      } catch (e:any) { delete previews.value[did]; deviceErrors.value[did] = e.message; }
    }
  } finally { busy.value = false; }
}
async function removeImported(d:any) {
  if (!await ask('从控制台删除设备“' + d.name + '”？米家中的设备保留，存在项目引用时会拒绝。')) return;
  busy.value = true; error.value = ''; notice.value = '';
  try {
    await api('/admin/devices/' + d.imported_device_id, 'DELETE');
    d.imported_device_id = '';
    notice.value = '设备已从控制台删除，米家中的设备保留。'; emit('changed');
  } catch (e:any) { error.value = e.message; }
  finally { busy.value = false; }
}
async function importDevices() {
  busy.value = true; error.value = ''; notice.value = '';
  try {
    const result = await api('/admin/xiaomi/accounts/' + accountID.value + '/imports', 'POST', {discovery_id: discovery.value.id, dids: selected.value});
    let imported = 0;
    for (const item of result.items) {
      if (item.device) {
        const device = discovery.value.devices.find((d:any) => String(d.did) === item.did);
        if (device) device.imported_device_id = item.device.id;
        selected.value = selected.value.filter(did => did !== item.did); imported++;
      } else deviceErrors.value[item.did] = item.error?.message || '导入失败';
    }
    if (imported) { notice.value = '已导入 ' + imported + ' 台开关设备，可在设备管理中读取状态、调试并绑定项目服务。'; emit('changed'); }
  } catch (e:any) { error.value = e.message; }
  finally { busy.value = false; }
}
</script>

<template>
  <Dialog title="小米设备接入" @close="close">
    <p class="muted">连接中国大陆米家账号，选择要加入控制台的开关设备。</p>
    <div v-if="!creating" class="xiaomi-accounts">
      <div class="row wrap">
        <label v-if="accounts.length" class="xiaomi-account-select">小米账号
          <select v-model="accountID" :disabled="busy" @change="resetDiscovery">
            <option v-for="a in accounts" :key="a.id" :value="a.id">{{a.display_name}} · 中国大陆</option>
          </select>
        </label>
        <button @click="addAccount()" :disabled="busy">＋ 添加小米账号</button>
      </div>
      <template v-if="account">
        <p class="meta">账号 {{account.user_id}} · {{account.status === 'connected' ? '登录态可用' : '需要检查登录态'}}</p>
        <p v-if="account.last_error" class="notice">{{account.last_error}}</p>
        <div class="row wrap section-tools">
          <button class="primary" @click="discover" :disabled="busy">{{busy ? '处理中…' : '刷新设备列表'}}</button>
          <button @click="refreshAccount" :disabled="busy">刷新登录态</button>
          <button @click="addAccount(account)" :disabled="busy">重新扫码</button>
          <button class="text-button danger" @click="removeAccount" :disabled="busy">移除账号</button>
        </div>
      </template>
    </div>

    <form v-if="creating" @submit.prevent="startLogin">
      <label>账号名称<input v-model="loginName" required maxlength="100" :disabled="busy || login?.status === 'pending'" placeholder="例如：实验室米家"></label>
      <p v-if="reauthID" class="notice">请使用原小米账号扫码，已有设备和项目绑定会保留。</p>
      <div v-if="login?.status === 'pending'" class="xiaomi-qr" role="status">
        <img :src="'/api/v1/admin/xiaomi/logins/' + login.id + '/qr'" alt="米家账号登录二维码" width="240" height="240">
        <strong>请用米家 App 扫码，并在手机确认登录</strong>
        <p class="meta">二维码约两分钟有效，过期后可重新生成。</p>
      </div>
      <p v-else-if="login?.error" class="notice" role="status">{{login.error}}</p>
      <div class="form-actions">
        <button type="button" @click="cancelLogin(); creating = false" :disabled="busy">返回账号列表</button>
        <button v-if="login?.status !== 'pending'" class="primary" :disabled="busy">{{busy ? '正在生成…' : login ? '重新生成二维码' : '生成登录二维码'}}</button>
      </div>
    </form>

    <section v-if="discovery && !creating" class="xiaomi-discovery">
      <h3>选择开关设备</h3>
      <p class="meta">已发现 {{discovery.devices.length}} 台设备。支持标准开关及规格明确声明的厂商开关通道，每次最多导入20台；设备在线标记来自米家云。</p>
      <label>筛选设备<input v-model="filter" :disabled="busy" placeholder="搜索设备、型号、家庭或房间"></label>
      <p v-if="!visibleDevices.length" class="muted">没有符合条件的设备。</p>
      <div class="xiaomi-device-list">
        <article v-for="d in visibleDevices" :key="d.did" class="xiaomi-device">
          <label class="check-row">
            <input v-model="selected" type="checkbox" :value="String(d.did)" :disabled="busy || !!d.imported_device_id || (selected.length >= 20 && !selected.includes(String(d.did)))" :aria-label="'选择 ' + d.name">
            <span><strong>{{d.name || '未命名设备'}}</strong><small class="muted">{{[d.home, d.room].filter(Boolean).join(' / ') || '未分配房间'}}</small></span>
            <span class="badge" :class="d.imported_device_id ? 'online' : ''">{{d.imported_device_id ? '已导入' : d.isOnline ? '云端在线' : '云端离线'}}</span>
          </label>
          <p class="meta">{{d.model}}</p><button v-if="d.imported_device_id" class="danger" @click="removeImported(d)" :disabled="busy">删除已导入设备</button>
          <template v-if="selected.includes(String(d.did))">
            <p v-if="previews[String(d.did)]" class="meta">{{serviceSummary(previews[String(d.did)].services)}}</p><ul v-if="previews[String(d.did)]" class="xiaomi-capabilities">
              <li v-for="service in previews[String(d.did)].services" :key="service.key">{{service.title}}</li>
            </ul>
            <p v-else class="meta">请预览能力，确认是否支持开关通道。</p>
            <p v-if="deviceErrors[String(d.did)]" class="error-text" role="alert">{{deviceErrors[String(d.did)]}}</p>
          </template>
        </article>
      </div>
      <p v-if="selected.length" class="notice">设置开关会改变实际设备状态。云端或网关接收请求不代表设备动作已核验；读取的云端状态可能滞后。</p>
      <footer class="form-actions">
        <span class="meta">已选择 {{selected.length}} 台</span>
        <button @click="preview" :disabled="busy || !selected.length">{{busy ? '处理中…' : '预览所选能力'}}</button>
        <button class="primary" @click="importDevices" :disabled="busy || !ready">导入所选设备</button>
      </footer>
    </section>
    <p v-if="notice" class="notice" role="status">{{notice}}</p>
    <p v-if="error" class="error-text" role="alert">{{error}}</p>
  </Dialog>
</template>

<style scoped>
.xiaomi-account-select{flex:1;margin:0;min-width:150px}.xiaomi-accounts>.row{align-items:end}.xiaomi-qr{display:flex;align-items:center;flex-direction:column;gap:12px;text-align:center;padding:20px 0}.xiaomi-qr img{max-width:100%;object-fit:contain;background:white}.xiaomi-device-list{display:flex;flex-direction:column;gap:10px}.xiaomi-device{border:1px solid var(--line);border-radius:8px;padding:14px}.xiaomi-device .check-row{margin-bottom:6px;align-items:center}.xiaomi-device .check-row>span:nth-child(2){flex:1;min-width:0;overflow-wrap:anywhere}.xiaomi-device small{display:block;font-weight:400}.xiaomi-device .meta{margin:0}.xiaomi-device .error-text{margin:8px 0 0;font-size:12px}.xiaomi-capabilities{margin:10px 0 0;padding-left:20px;font-size:12px;color:var(--muted)}.xiaomi-discovery>.form-actions{flex-wrap:wrap}.xiaomi-discovery>.form-actions>span{margin-right:auto}@media(max-width:400px){.xiaomi-device .check-row{flex-wrap:wrap}.xiaomi-device .badge{margin-left:28px}}
</style>
