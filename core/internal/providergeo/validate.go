package providergeo

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"strings"

	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/geodata/strmatcher"
	"google.golang.org/protobuf/proto"
)

// ValidDatabase checks individual records without keeping a decoded copy of the whole database.
func ValidDatabase(kind string, body []byte) bool {
	if kind != "geoip" && kind != "geosite" {
		return false
	}
	source := io.NewSectionReader(bytes.NewReader(body), 0, int64(len(body)))
	return readEntries(source, func(_ string, entry *io.SectionReader) (bool, error) {
		reader := newGeoReader(entry)
		hasRecords := false
		for {
			tag, record, err := field(reader)
			if err == io.EOF {
				if !hasRecords {
					return false, fmt.Errorf("empty geo category")
				}
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if tag != 18 {
				continue
			}
			if record.Size() > 1<<20 {
				return false, fmt.Errorf("geo record exceeds 1 MiB")
			}
			data, err := io.ReadAll(record)
			if err != nil {
				return false, err
			}
			if !validRecord(kind, data) {
				return false, fmt.Errorf("invalid %s record", kind)
			}
			hasRecords = true
		}
	}) == nil
}

func validRecord(kind string, data []byte) bool {
	if kind == "geoip" {
		var cidr geodata.CIDR
		if proto.Unmarshal(data, &cidr) != nil {
			return false
		}
		ip, ok := netip.AddrFromSlice(cidr.Ip)
		return ok && cidr.Prefix <= uint32(ip.BitLen())
	}

	var domain geodata.Domain
	if proto.Unmarshal(data, &domain) != nil || strings.TrimSpace(domain.Value) == "" {
		return false
	}
	switch domain.Type {
	case geodata.Domain_Substr, geodata.Domain_Domain, geodata.Domain_Full:
		return true
	case geodata.Domain_Regex:
		_, err := strmatcher.Regex.New(domain.Value)
		return err == nil
	default:
		return false
	}
}
