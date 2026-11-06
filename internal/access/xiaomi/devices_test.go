package xiaomi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPublicOutletSpecifications(t *testing.T) {
	for _, tc := range []struct {
		model                                string
		channels, regular, master, indicator int
	}{
		{"oit.plug.cb7", 7, 5, 1, 1}, {"giot.plug.v6shsm", 1, 1, 0, 0},
	} {
		t.Run(tc.model, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + tc.model + ".spec.json")
			if err != nil {
				t.Fatal(err)
			}
			var spec struct {
				Type string `json:"type"`
			}
			if err = json.Unmarshal(b, &spec); err != nil {
				t.Fatal(err)
			}
			switches, err := parseSwitches(b, spec.Type)
			if err != nil {
				t.Fatal(err)
			}
			if len(switches) != tc.channels {
				t.Fatalf("channels=%d, want %d", len(switches), tc.channels)
			}
			roles := map[string]int{}
			for _, s := range switches {
				roles[s.Role]++
				if !s.Read || !s.Write {
					t.Fatal("access lost", s)
				}
			}
			if roles["switch"] != tc.regular || roles["master"] != tc.master || roles["indicator"] != tc.indicator {
				t.Fatal("roles", roles)
			}
			if tc.model == "oit.plug.cb7" {
				for i := 0; i < 5; i++ {
					s := switches[i+2]
					if s.SIID != i+6 || s.PIID != 1 || !strings.HasPrefix(s.Title, "AC") {
						t.Fatal("private channel mapping", s)
					}
				}
			}
			services := switchServices(switches)
			if len(services) != 2*tc.channels {
				t.Fatal("service count", len(services))
			}
			for _, s := range services {
				if s.GroupKey == "" || s.GroupTitle == "" || s.GroupKind == "" {
					t.Fatal("group missing", s.Key)
				}
			}
		})
	}
}

func TestPrivateSwitchRequiresDeclaredMatchingService(t *testing.T) {
	raw := []byte(`{"type":"test","services":[
 {"iid":2,"type":"urn:vendor-spec:service:switch:1","properties":[
 {"iid":1,"type":"urn:vendor-spec:property:on:1","format":"bool","access":["read"]},
 {"iid":2,"type":"urn:other-spec:property:on:1","format":"bool","access":["write"]},
 {"iid":3,"type":"urn:vendor-spec:property:status:1","format":"bool","access":["write"]}]},
 {"iid":3,"type":"urn:vendor-spec:service:setting:1","properties":[{"iid":1,"type":"urn:vendor-spec:property:on:1","format":"bool","access":["write"]}]},
 {"iid":4,"properties":[{"iid":1,"type":"urn:vendor-spec:property:on:1","format":"bool","access":["write"]}]}
 ]}`)
	switches, err := parseSwitches(raw, "test")
	if err != nil || len(switches) != 1 || switches[0].SIID != 2 || !switches[0].Read || switches[0].Write {
		t.Fatal(switches, err)
	}
}
