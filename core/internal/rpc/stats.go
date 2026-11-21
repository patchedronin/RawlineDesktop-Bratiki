package rpc

import (
	"context"
	"log"

	"RawlineCore/gen"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"google.golang.org/protobuf/proto"
)

func (s *server) QueryStats(ctx context.Context, in *gen.EmptyReq) (out *gen.QueryStatsResp, err error) {
	out = &gen.QueryStatsResp{}
	out.Ups = make(map[string]int64)
	out.Downs = make(map[string]int64)
	if box := currentBox(); box != nil {
		if lifecycleMu.TryLock() {
			for _, session := range extraSessions {
				if session.transport && session.backend == "" && session.tag == "proxy" {
					session.mu.Lock()
					health := session.health
					session.mu.Unlock()
					if health != nil {
						if active, total := health.slots(); total > 0 {
							out.QwdttActive, out.QwdttTotal = proto.Int32(int32(active)), proto.Int32(int32(total))
						}
					}
					break
				}
			}
			lifecycleMu.Unlock()
		}
		trafficManager := service.PtrFromContext[trafficcontrol.Manager](box.Context())
		if trafficManager != nil {
			outbounds := service.FromContext[adapter.OutboundManager](box.Context())
			if outbounds == nil {
				log.Println("Failed to get outbound manager")
				err = E.New("nil outbound manager")
				return
			}
			endpoints := service.FromContext[adapter.EndpointManager](box.Context())
			if endpoints == nil {
				log.Println("Failed to get endpoint manager")
				err = E.New("nil endpoint manager")
				return
			}
			for _, ob := range outbounds.Outbounds() {
				u, d := trafficManager.TotalOutbound(ob.Tag())
				out.Ups[ob.Tag()] = u
				out.Downs[ob.Tag()] = d
			}
			for _, ep := range endpoints.Endpoints() {
				u, d := trafficManager.TotalOutbound(ep.Tag())
				out.Ups[ep.Tag()] = u
				out.Downs[ep.Tag()] = d
			}
		}
	}
	return
}
