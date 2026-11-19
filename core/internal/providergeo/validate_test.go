package providergeo

import (
	"testing"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

func TestValidDatabaseRejectsWrongSchemaAndMalformedNestedRecord(t *testing.T) {
	ip, err := proto.Marshal(&geodata.GeoIPList{Entry: []*geodata.GeoIP{{
		Code: "TEST", Cidr: []*geodata.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	site, err := proto.Marshal(&geodata.GeoSiteList{Entry: []*geodata.GeoSite{{
		Code: "TEST", Domain: []*geodata.Domain{{Value: "example.org"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	badRegex, err := proto.Marshal(&geodata.GeoSiteList{Entry: []*geodata.GeoSite{{
		Code: "TEST", Domain: []*geodata.Domain{{Type: geodata.Domain_Regex, Value: "["}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	broken := []byte{10, 9, 10, 4, 'T', 'E', 'S', 'T', 18, 1, 0x80}
	for _, tc := range []struct {
		kind string
		data []byte
		want bool
	}{
		{"geoip", ip, true},
		{"geosite", site, true},
		{"geoip", site, false},
		{"geosite", ip, false},
		{"geosite", broken, false},
		{"geosite", badRegex, false},
		{"geosite", []byte("<html>temporary failure</html>"), false},
	} {
		if got := ValidDatabase(tc.kind, tc.data); got != tc.want {
			t.Errorf("ValidDatabase(%q, %x) = %v, want %v", tc.kind, tc.data, got, tc.want)
		}
	}
}
