package xiaomi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "devconsole/internal/contracts"
)

// 云端设备信息采用白名单字段，绝不把设备 token、MAC 或原始响应返回浏览器。
type RemoteDevice struct {
	DID      remoteID `json:"did"`
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	SpecType string   `json:"spec_type"`
	Online   bool     `json:"isOnline"`
	Home     string   `json:"home"`
	Room     string   `json:"room"`
	Imported string   `json:"imported_device_id,omitempty"`
}

type home struct {
	ID       remoteID `json:"id"`
	Owner    remoteID `json:"uid"`
	Name     string   `json:"name"`
	RoomList []struct {
		Name string     `json:"name"`
		DIDs []remoteID `json:"dids"`
	} `json:"roomlist"`
}

func (cl *Client) devices(ctx context.Context, cred Credentials) ([]RemoteDevice, error) {
	var homes struct {
		Homes []home `json:"homelist"`
	}
	if err := cl.request(ctx, cred, "/v2/homeroom/gethome_merged", map[string]any{"fg": true, "fetch_share": true, "fetch_share_dev": true, "fetch_cariot": true, "limit": 300, "app_ver": 7, "plat_form": 0}, &homes, false); err != nil {
		return nil, err
	}
	devices := []RemoteDevice{}
	seen := map[remoteID]bool{}
	for _, h := range homes.Homes {
		cursor := ""
		pages := map[string]bool{}
		for page := 0; ; page++ {
			if page >= 100 || pages[cursor] {
				return nil, c.Fail("PROTOCOL_ERROR", "米家设备分页未正常结束，请重新刷新")
			}
			pages[cursor] = true
			var v struct {
				Devices []RemoteDevice `json:"device_info"`
				More    bool           `json:"has_more"`
				Cursor  remoteID       `json:"max_did"`
			}
			// 保留十进制 ID 的精度；json.Number 在序列化时仍是 JSON 数字。
			data := map[string]any{"home_owner": json.Number(h.Owner), "home_id": json.Number(h.ID), "limit": 200, "start_did": cursor, "get_split_device": true, "support_smart_home": true, "get_cariot_device": true, "get_third_device": true}
			if err := cl.request(ctx, cred, "/home/home_device_list", data, &v, false); err != nil {
				return nil, err
			}
			for _, d := range v.Devices {
				if d.DID == "" || seen[d.DID] {
					continue
				}
				seen[d.DID] = true
				d.Home = h.Name
				for _, room := range h.RoomList {
					for _, id := range room.DIDs {
						if id == d.DID {
							d.Room = room.Name
						}
					}
				}
				devices = append(devices, d)
			}
			if !v.More {
				break
			}
			if v.Cursor == "" {
				return nil, c.Fail("PROTOCOL_ERROR", "米家设备分页游标缺失，未导入不完整清单")
			}
			cursor = string(v.Cursor)
		}
	}
	// 单独共享的设备可能不属于上述家庭；仍需纳入并按 did 去重。
	var shared struct {
		Devices []struct {
			RemoteDevice
			Owner json.RawMessage `json:"owner"`
		} `json:"list"`
	}
	if err := cl.request(ctx, cred, "/v2/home/device_list_page", map[string]any{"getVirtualModel": true, "getHuamiDevices": 1, "get_split_device": true, "support_smart_home": true, "get_third_device": true}, &shared, false); err != nil {
		return nil, err
	}
	for _, entry := range shared.Devices {
		d := entry.RemoteDevice
		if d.DID != "" && !seen[d.DID] {
			d.Home = "共享设备"
			seen[d.DID] = true
			devices = append(devices, d)
		}
	}
	return devices, nil
}

// 只导入明确的 on/bool：标准属性，或同命名空间 switch/outlet 服务的厂商属性。
type Switch struct {
	Role  string `json:"role,omitempty"`
	SIID  int    `json:"siid"`
	PIID  int    `json:"piid"`
	Title string `json:"title"`
	Read  bool   `json:"read"`
	Write bool   `json:"write"`
}

func (s Switch) key(kind string) string { return fmt.Sprintf("switch.%d.%d.%s", s.SIID, s.PIID, kind) }

func switchServices(switches []Switch) []c.Service {
	services := []c.Service{}
	boolean := c.Schema{Type: "boolean", Title: "开关状态"}
	for _, s := range switches {
		start := len(services)
		if s.Read {
			services = append(services, c.Service{
				Key: s.key("read"), Contract: "xiaomi.switch.read", ContractVersion: 1,
				Title: s.Title + " · 读取状态", Description: "读取米家云保存的开关状态，可能滞后；仅在云端返回属性更新时间时提供 observed_at。",
				Kind: "read", Input: c.Object(map[string]c.Schema{}), Output: c.Object(map[string]c.Schema{"enabled": boolean}, "enabled"),
				TimeoutMS: 10000, ResultSource: "cache",
			})
		}
		if s.Write {
			services = append(services, c.Service{
				Key: s.key("set"), Contract: "xiaomi.switch.set", ContractVersion: 1,
				Title: s.Title + " · 设置开关", Description: "提交明确的开启/关闭状态；成功仅表示米家云或网关接收请求，未核验设备实际动作。不自动重试。",
				Kind: "operation", Input: c.Object(map[string]c.Schema{"enabled": boolean}, "enabled"),
				Output:    c.Object(map[string]c.Schema{"requested_enabled": boolean, "accepted": {Type: "boolean", Enum: []any{true}}}, "requested_enabled", "accepted"),
				TimeoutMS: 10000, ResultSource: "cloud", SupportsVerification: false,
			})
		}
		for i := start; i < len(services); i++ {
			services[i].GroupKey = fmt.Sprintf("switch.%d.%d", s.SIID, s.PIID)
			services[i].GroupTitle = s.Title
			services[i].GroupKind = s.Role
			if s.Role == "" {
				services[i].GroupKind = "switch"
			}
		}
	}
	return services
}

func (cl *Client) switches(ctx context.Context, urn string) ([]Switch, error) {
	if !strings.HasPrefix(urn, "urn:miot-spec-v2:device:") || len(urn) > 512 {
		return nil, c.Fail("INVALID_ARGUMENT", "该设备没有可用的标准 MIoT 规格，首版暂不支持")
	}
	u, err := url.Parse(cl.SpecURL)
	if err != nil || u.Scheme != "https" || u.Host != "miot-spec.org" {
		return nil, c.Fail("INTERNAL_ERROR", "MIoT 规格地址无效")
	}
	u.RawQuery = url.Values{"type": {urn}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	h := *cl.HTTP
	h.Jar = nil
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := h.Do(req)
	if err != nil {
		return nil, c.Fail("DEVICE_OFFLINE", "无法读取 MIoT 设备规格，请稍后重试")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, c.Fail("DEVICE_OFFLINE", "MIoT 设备规格暂时不可用")
	}
	b, err := boundedRead(res.Body, maxResponse)
	if err != nil {
		return nil, err
	}
	return parseSwitches(b, urn)
}

func urnName(raw, kind string) (string, string) {
	p := strings.Split(raw, ":")
	if len(p) < 5 || p[0] != "urn" || p[2] != kind || p[1] == "" || p[4] == "" {
		return "", ""
	}
	return p[1], p[3]
}

func parseSwitches(b []byte, urn string) ([]Switch, error) {
	var spec struct {
		Type     string `json:"type"`
		Services []struct {
			IID         int    `json:"iid"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Comment     string `json:"comment"`
			Properties  []struct {
				IID    int      `json:"iid"`
				Type   string   `json:"type"`
				Format string   `json:"format"`
				Access []string `json:"access"`
			} `json:"properties"`
		} `json:"services"`
	}
	if json.Unmarshal(b, &spec) != nil || spec.Type != urn {
		return nil, c.Fail("PROTOCOL_ERROR", "MIoT 规格与所选设备不一致")
	}
	out := []Switch{}
	seen := map[string]bool{}
	for _, s := range spec.Services {
		serviceNS, serviceName := urnName(s.Type, "service")
		for _, p := range s.Properties {
			propertyNS, propertyName := urnName(p.Type, "property")
			allowed := propertyName == "on" && (propertyNS == "miot-spec-v2" || (propertyNS != "" && propertyNS == serviceNS && (serviceName == "switch" || serviceName == "outlet")))
			if s.IID <= 0 || p.IID <= 0 || p.Format != "bool" || !allowed {
				continue
			}
			v := Switch{SIID: s.IID, PIID: p.IID, Title: strings.TrimSpace(s.Comment), Role: "switch"}
			if v.Title == "" {
				v.Title = s.Description
			}
			if serviceName == "indicator-light" {
				v.Role = "indicator"
			} else if strings.Contains(s.Comment, "总控") || strings.Contains(s.Comment, "总开关") {
				v.Role = "master"
			}
			if v.Title == "" {
				v.Title = "开关"
			}
			v.Title += fmt.Sprintf("（通道 %d.%d）", s.IID, p.IID)
			for _, access := range p.Access {
				v.Read = v.Read || access == "read"
				v.Write = v.Write || access == "write"
			}
			if (v.Read || v.Write) && !seen[v.key("read")] {
				seen[v.key("read")] = true
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		return nil, c.Fail("INVALID_ARGUMENT", "该设备没有标准布尔开关通道，首版暂不支持")
	}
	return out, nil
}

func (cl *Client) property(ctx context.Context, cred Credentials, did string, sw Switch, enabled *bool) (c.Result, error) {
	param := map[string]any{"did": did, "siid": sw.SIID, "piid": sw.PIID}
	path := "/miotspec/prop/get"
	data := map[string]any{"params": []any{param}, "datasource": 1}
	write := enabled != nil
	if write {
		path = "/miotspec/prop/set"
		param["value"] = *enabled
		delete(data, "datasource")
	}
	var values []struct {
		DID        remoteID        `json:"did"`
		SIID       int             `json:"siid"`
		PIID       int             `json:"piid"`
		Code       *int            `json:"code"`
		Value      json.RawMessage `json:"value"`
		UpdateTime json.RawMessage `json:"updateTime"`
	}
	if err := cl.request(ctx, cred, path, data, &values, write); err != nil {
		return c.Result{}, err
	}
	badReply := func() (c.Result, error) {
		if write {
			return c.Result{}, &c.Error{Code: "OUTCOME_UNKNOWN", Message: "米家未返回匹配的控制结果，未自动重试，请显式读取状态", Outcome: "unknown"}
		}
		return c.Result{}, c.Fail("PROTOCOL_ERROR", "米家属性响应与所选开关不匹配")
	}
	if len(values) != 1 {
		return badReply()
	}
	v := values[0]
	if string(v.DID) != did || v.SIID != sw.SIID || v.PIID != sw.PIID || v.Code == nil {
		return badReply()
	}
	if *v.Code != 0 && !(write && *v.Code == 1) {
		outcome := "not_started"
		if write {
			outcome = "unknown"
		}
		return c.Result{}, &c.Error{Code: "DEVICE_ERROR", Message: fmt.Sprintf("米家开关拒绝请求（代码 %d）", *v.Code), Outcome: outcome}
	}
	if write {
		return c.Result{Output: map[string]any{"requested_enabled": *enabled, "accepted": true}, Confirmation: "acknowledged", Source: "cloud"}, nil
	}
	var state bool
	if len(v.Value) == 0 || string(v.Value) == "null" || json.Unmarshal(v.Value, &state) != nil {
		return badReply()
	}
	var observed time.Time
	var seconds int64
	if json.Unmarshal(v.UpdateTime, &seconds) == nil && seconds > 0 && seconds <= time.Now().Add(time.Minute).Unix() {
		observed = time.Unix(seconds, 0).UTC()
	}
	return c.Result{Output: map[string]any{"enabled": state}, Confirmation: "read", Source: "cache", ObservedAt: observed}, nil
}
