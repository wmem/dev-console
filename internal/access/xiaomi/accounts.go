package xiaomi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"devconsole/internal/storage"
)

type Account struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	UserID      string    `json:"user_id"`
	Region      string    `json:"region"`
	Status      string    `json:"status"`
	LastError   string    `json:"last_error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type accountState struct {
	meta       Account
	credential Credentials
	active     int
	refreshing bool
}

type encryptedCredentials struct {
	Version int    `json:"version"`
	Payload string `json:"payload"`
}

type LoginStatus struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	AccountID string    `json:"account_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type loginSession struct {
	public    LoginStatus
	owner     string
	accountID string
	name      string
	qr        *qrLogin
	cancel    context.CancelFunc
}

type discovery struct {
	refreshDeviceID string
	revision        int
	id              string
	accountID       string
	expires         time.Time
	devices         map[string]RemoteDevice
	prepared        map[string][]Switch
}

type Manager struct {
	store     *storage.Store
	registrar c.DeviceRegistrar
	client    *Client
	mu        sync.Mutex
	accounts  map[string]*accountState
	logins    map[string]*loginSession
	discovery map[string]*discovery
	targets   map[string]*target
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
}

func New(s *storage.Store, registrar c.DeviceRegistrar, client *Client) *Manager {
	if client == nil {
		client = NewClient()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: s, registrar: registrar, client: client, accounts: map[string]*accountState{}, logins: map[string]*loginSession{}, discovery: map[string]*discovery{}, targets: map[string]*target{}, ctx: ctx, cancel: cancel}
}

func credentialAAD(a Account) string {
	return "dconsole-xiaomi:v1\x00" + a.ID + "\x00" + a.UserID + "\x00" + a.Region
}

func (m *Manager) credentialChanges(a Account, cred Credentials) ([]storage.Change, error) {
	if m.store.Secrets == nil {
		return nil, c.Fail("INTERNAL_ERROR", "账号加密存储不可用")
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	secret := encryptedCredentials{Version: 1, Payload: m.store.Secrets.Seal(string(b), credentialAAD(a))}
	return []storage.Change{{Table: "xiaomi_accounts", ID: a.ID, Value: a}, {Table: "xiaomi_credentials", ID: a.ID, Value: secret}}, nil
}

func (m *Manager) Accounts() []Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) cleanupLocked() {
	for id, session := range m.logins {
		if time.Now().After(session.public.ExpiresAt.Add(3 * time.Minute)) {
			session.cancel()
			delete(m.logins, id)
		}
	}
	for id, d := range m.discovery {
		if time.Now().After(d.expires) {
			delete(m.discovery, id)
		}
	}
}

func (m *Manager) StartLogin(ctx context.Context, owner, name, region, accountID string) (LoginStatus, error) {
	name = strings.TrimSpace(name)
	if region != "cn" || len(name) == 0 || len([]rune(name)) > 100 {
		return LoginStatus{}, c.Fail("INVALID_ARGUMENT", "请填写不超过100字的账号名称；首版只支持中国大陆")
	}
	m.mu.Lock()
	m.cleanupLocked()
	if m.closed {
		m.mu.Unlock()
		return LoginStatus{}, c.Fail("DEVICE_OFFLINE", "接入服务正在关闭")
	}
	cred := identity()
	if accountID != "" {
		a := m.accounts[accountID]
		if a == nil {
			m.mu.Unlock()
			return LoginStatus{}, c.Fail("NOT_FOUND", "小米账号不存在")
		}
		cred = Credentials{DeviceID: a.credential.DeviceID, PassO: a.credential.PassO, UserAgent: a.credential.UserAgent}
	}
	for id, session := range m.logins {
		if session.owner == owner {
			session.cancel()
			delete(m.logins, id)
		}
	}
	if len(m.logins) >= 32 {
		m.mu.Unlock()
		return LoginStatus{}, c.Fail("RATE_LIMITED", "待处理扫码会话过多，请稍后重试")
	}
	pollCtx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	session := &loginSession{public: LoginStatus{ID: auth.ID(), Status: "pending", ExpiresAt: time.Now().UTC().Add(2 * time.Minute)}, owner: owner, accountID: accountID, name: name, cancel: cancel}
	m.logins[session.public.ID] = session
	m.mu.Unlock()

	prepareCtx, done := context.WithTimeout(ctx, 12*time.Second)
	qr, err := m.client.beginLogin(prepareCtx, cred)
	done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil || m.closed || m.logins[session.public.ID] != session || pollCtx.Err() != nil {
		cancel()
		if m.logins[session.public.ID] == session {
			delete(m.logins, session.public.ID)
		}
		if err == nil {
			err = c.Fail("CONFIG_CONFLICT", "扫码会话已结束，请重新登录")
		}
		return LoginStatus{}, err
	}
	session.qr = qr
	m.wg.Add(1)
	go m.finishSession(pollCtx, session)
	return session.public, nil
}

func (m *Manager) finishSession(ctx context.Context, session *loginSession) {
	defer m.wg.Done()
	defer session.cancel()
	cred, err := m.client.completeLogin(ctx, session.qr)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.logins[session.public.ID] != session {
		return
	}
	if ctx.Err() != nil {
		session.public.Status, session.public.Error = "expired", "二维码已过期，请重新扫码"
		return
	}
	if err != nil {
		session.public.Status, session.public.Error = "failed", "米家登录未完成，请在手机确认后重新扫码"
		return
	}
	a := Account{ID: session.accountID, DisplayName: session.name, UserID: cred.UserID, Region: "cn", Status: "connected", UpdatedAt: time.Now().UTC()}
	if a.ID != "" {
		old := m.accounts[a.ID]
		if old == nil || old.meta.UserID != a.UserID {
			session.public.Status, session.public.Error = "failed", "扫码账号与原账号不一致，未替换已有设备的账号"
			return
		}
		if old.active > 0 {
			session.public.Status, session.public.Error = "failed", "账号正在通信，请在空闲时重新扫码"
			return
		}
	} else {
		for _, old := range m.accounts {
			if old.meta.UserID == a.UserID && old.meta.Region == a.Region {
				session.public.Status, session.public.Error = "failed", "该小米账号已存在，请使用原账号的重新扫码入口"
				return
			}
		}
		a.ID = auth.ID()
	}
	changes, err := m.credentialChanges(a, cred)
	if err == nil {
		err = m.store.Batch(changes...)
	}
	if err != nil {
		session.public.Status, session.public.Error = "failed", "账号凭据保存失败，未登记登录，请重试"
		return
	}
	m.accounts[a.ID] = &accountState{meta: a, credential: cred}
	// 重新授权后必须重新发现，不能使用旧账号会话产生的导入预览。
	for id, d := range m.discovery {
		if d.accountID == a.ID {
			delete(m.discovery, id)
		}
	}
	session.public.Status, session.public.AccountID = "succeeded", a.ID
}

func (m *Manager) Login(owner, id string) (LoginStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	s := m.logins[id]
	if s == nil || s.owner != owner {
		return LoginStatus{}, c.Fail("NOT_FOUND", "扫码会话不存在或已结束")
	}
	if s.public.Status == "pending" && time.Now().After(s.public.ExpiresAt) {
		s.public.Status, s.public.Error = "expired", "二维码已过期，请重新扫码"
		s.cancel()
	}
	return s.public, nil
}

func (m *Manager) QRImage(owner, id string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.logins[id]
	if s == nil || s.owner != owner || s.qr == nil || s.public.Status != "pending" || time.Now().After(s.public.ExpiresAt) {
		return nil, "", c.Fail("NOT_FOUND", "二维码已失效，请重新登录")
	}
	return s.qr.image, s.qr.imageType, nil
}

func (m *Manager) CancelLogin(owner, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.logins[id]
	if s == nil || s.owner != owner {
		return c.Fail("NOT_FOUND", "扫码会话不存在")
	}
	s.cancel()
	delete(m.logins, id)
	return nil
}

func (m *Manager) DeleteAccount(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accounts[id]
	if a == nil {
		return c.Fail("NOT_FOUND", "小米账号不存在")
	}
	for _, t := range m.targets {
		if t.saved.Config.AccountID == id {
			return c.Fail("RESOURCE_REFERENCED", "账号仍有已导入设备，请先解除项目引用并删除这些设备")
		}
	}
	if a.active > 0 {
		return c.Fail("RESOURCE_BUSY", "账号正在通信")
	}
	if err := m.store.Batch(storage.Change{Table: "xiaomi_accounts", ID: id, Delete: true}, storage.Change{Table: "xiaomi_credentials", ID: id, Delete: true}); err != nil {
		return err
	}
	delete(m.accounts, id)
	for key, s := range m.logins {
		if s.accountID == id {
			s.cancel()
			delete(m.logins, key)
		}
	}
	for key, d := range m.discovery {
		if d.accountID == id {
			delete(m.discovery, key)
		}
	}
	return nil
}

// 登录续期发生在业务请求发送之前；失败后不会重放本次业务请求。
func (m *Manager) credentials(ctx context.Context, id string, force bool) (Credentials, func(), error) {
	m.mu.Lock()
	a := m.accounts[id]
	if a == nil || m.closed {
		m.mu.Unlock()
		return Credentials{}, nil, c.Fail("DEVICE_OFFLINE", "小米账号不可用")
	}
	need := force || a.meta.Status != "connected" || time.Since(a.credential.RefreshedAt) >= 15*time.Minute
	if a.refreshing || need && a.active > 0 {
		m.mu.Unlock()
		return Credentials{}, nil, c.Fail("RESOURCE_BUSY", "小米账号正在刷新登录态")
	}
	a.active++
	cred := a.credential
	if need {
		a.refreshing = true
	}
	m.mu.Unlock()
	release := func() { m.mu.Lock(); a.active--; m.mu.Unlock() }
	if !need {
		return cred, release, nil
	}
	refreshed, err := m.client.refresh(ctx, cred)
	m.mu.Lock()
	a.refreshing = false
	if err == nil {
		meta := a.meta
		meta.Status, meta.LastError, meta.UpdatedAt = "connected", "", time.Now().UTC()
		var changes []storage.Change
		changes, err = m.credentialChanges(meta, refreshed)
		if err == nil {
			err = m.store.Batch(changes...)
		}
		if err == nil {
			a.meta, a.credential = meta, refreshed
			cred = refreshed
		}
	}
	if err != nil {
		a.meta.Status, a.meta.LastError = "needs_login", "登录态刷新失败，请检查网络或重新扫码"
		_ = m.store.Put("xiaomi_accounts", id, a.meta)
	}
	m.mu.Unlock()
	if err != nil {
		release()
		return Credentials{}, nil, c.Fail("DEVICE_OFFLINE", "小米登录态无法刷新，请检查网络或重新扫码；未发送设备操作")
	}
	return cred, release, nil
}

func (m *Manager) Refresh(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, release, err := m.credentials(ctx, id, true)
	if err == nil {
		release()
	}
	return err
}

func (m *Manager) accountError(id string, err error) error {
	if authFailure(err) {
		m.mu.Lock()
		if a := m.accounts[id]; a != nil {
			a.meta.Status, a.meta.LastError = "needs_login", "小米登录态已失效，请刷新登录态或重新扫码"
			a.credential.RefreshedAt = time.Time{}
			_ = m.store.Put("xiaomi_accounts", id, a.meta)
		}
		m.mu.Unlock()
		return c.Fail("DEVICE_OFFLINE", "小米登录态已失效，未自动重试设备请求；请刷新登录态或重新扫码")
	}
	var remote *cloudError
	if errors.As(err, &remote) {
		return c.Fail("DEVICE_ERROR", remote.Error())
	}
	return err
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	for _, s := range m.logins {
		s.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) Restore(ctx context.Context) error {
	accounts, err := storage.List[Account](m.store, "xiaomi_accounts")
	if err != nil {
		return err
	}
	for _, a := range accounts {
		var encrypted encryptedCredentials
		if err := m.store.Get("xiaomi_credentials", a.ID, &encrypted); err != nil || encrypted.Version != 1 {
			return fmt.Errorf("小米账号 %s 缺少可用的加密凭据", a.ID)
		}
		raw, err := m.store.Secrets.Open(encrypted.Payload, credentialAAD(a))
		if err != nil {
			return fmt.Errorf("小米账号凭据无法解密，请恢复配套数据与密钥")
		}
		var cred Credentials
		if json.Unmarshal([]byte(raw), &cred) != nil || cred.UserID != a.UserID || a.Region != "cn" {
			return fmt.Errorf("小米账号凭据与登记身份不一致")
		}
		m.accounts[a.ID] = &accountState{meta: a, credential: cred}
	}
	saved, err := storage.List[savedDevice](m.store, "xiaomi_devices")
	if err != nil {
		return err
	}
	for _, s := range saved {
		var d c.Device
		if err := m.store.Get("devices", s.ID, &d); err != nil || m.accounts[s.Config.AccountID] == nil || len(s.Switches) == 0 {
			return fmt.Errorf("小米设备接入登记不完整：%s", s.ID)
		}
		m.targets[s.ID] = &target{saved: s}
		d.Services, d.AccessRoute, d.Availability, d.LastError = switchServices(s.Switches), Route, "unknown", "等待显式读取米家云状态"
		if err := m.registrar.UpsertDevice(ctx, d); err != nil {
			return err
		}
	}
	return nil
}
