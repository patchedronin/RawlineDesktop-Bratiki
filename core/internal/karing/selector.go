// Package karing uses sing-box's URL-test selection, as Karing does, while
// retaining Rawline's status and manual-selection API.
package karing

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func Register(registry *outbound.Registry) {
	outbound.Register[option.AutoSelectorOutboundOptions](registry, C.TypeAutoSelector, New)
}

func New(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AutoSelectorOutboundOptions) (adapter.Outbound, error) {
	// Explicit priority, balancing and passive routing keep their existing semantics.
	if options.Strategy == group.StrategyPriority || options.Balance || options.Passive != nil && options.Passive.Enabled {
		return group.NewAutoSelector(ctx, router, logger, tag, options)
	}
	interval := time.Duration(options.Interval)
	if interval <= 0 {
		interval = 300 * time.Second
	}
	inner, err := group.NewURLTest(ctx, router, logger, tag, option.URLTestOutboundOptions{
		Outbounds: options.Outbounds, URL: options.URL, Interval: badoption.Duration(interval),
		Tolerance: options.Tolerance, IdleTimeout: badoption.Duration(max(interval, 30*time.Minute)),
		InterruptExistConnections: options.InterruptExistConnections,
	})
	if err != nil {
		return nil, err
	}
	if !slices.Contains(options.Outbounds, options.Pinned) {
		options.Pinned = ""
	}
	return &Selector{singURLTest: inner.(*group.URLTest), ctx: ctx,
		manager: service.FromContext[adapter.OutboundManager](ctx), interval: interval,
		pinned: options.Pinned, warm: options.Warm, interrupt: options.InterruptExistConnections, connections: interrupt.NewGroup()}, nil
}

// Use a distinct embedded field name so URLTest is promoted as a method.
type singURLTest = group.URLTest

type Selector struct {
	*singURLTest
	ctx         context.Context
	manager     adapter.OutboundManager
	interval    time.Duration
	mu          sync.Mutex
	pinned      string
	warm        []option.AutoSelectorWarmEntry
	interrupt   bool
	connections *interrupt.Group
}

var _ group.AutoSelectorGroup = (*Selector)(nil)
var _ adapter.URLTestGroup = (*Selector)(nil)
var _ adapter.DialFailureListener = (*Selector)(nil)

func (s *Selector) Type() string { return C.TypeAutoSelector }

func (s *Selector) DialFailed(adapter.Outbound, string, error) {
	s.PerformUpdateCheck()
}

func (s *Selector) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateStart {
		history := service.PtrFromContext[urltest.HistoryStorage](s.ctx)
		for _, entry := range s.warm {
			if history != nil && entry.RTT > 0 && slices.Contains(s.All(), entry.Tag) {
				taken := time.Now().Add(-time.Duration(entry.Age) * time.Second)
				if previous := history.LoadURLTestHistory(entry.Tag); previous == nil || taken.After(previous.Time) {
					history.StoreURLTestHistory(entry.Tag, &adapter.URLTestHistory{Time: taken, Delay: entry.RTT})
				}
			}
		}
	}
	if err := s.singURLTest.Start(stage, scope); err != nil {
		return err
	}
	if stage == adapter.StartStateStarted {
		scope.Add(func() error { s.connections.Interrupt(true); return nil })
	}
	return nil
}

func (s *Selector) Selected(network string) adapter.Outbound {
	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	if pinned != "" && s.manager != nil {
		if member, ok := s.manager.Outbound(pinned); ok && slices.Contains(member.Network(), N.NetworkName(network)) {
			return member
		}
	}
	return s.singURLTest.Selected(network)
}

func (s *Selector) SelectOutbound(tag string) bool {
	if tag != "" && !slices.Contains(s.All(), tag) {
		return false
	}
	s.mu.Lock()
	changed := s.pinned != tag
	s.pinned = tag
	s.mu.Unlock()
	if changed {
		s.connections.Interrupt(s.interrupt)
	}
	return true
}

func (s *Selector) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	var conn net.Conn
	var err error
	member := s.Selected(N.NetworkName(network))
	if pinned != "" && member != nil && member.Tag() == pinned {
		history := service.PtrFromContext[urltest.HistoryStorage](s.ctx)
		tag, sample := history.LoadDirectSample(member)
		conn, err = member.DialContext(ctx, network, destination)
		if err != nil && ctx.Err() == nil {
			history.DeleteURLTestHistoryIfCurrent(tag, sample)
		}
	} else {
		conn, err = s.singURLTest.DialContext(ctx, network, destination)
	}
	if err != nil {
		s.PerformUpdateCheck()
		return nil, err
	}
	return s.connections.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	var conn net.PacketConn
	var err error
	member := s.Selected(N.NetworkUDP)
	if pinned != "" && member != nil && member.Tag() == pinned {
		history := service.PtrFromContext[urltest.HistoryStorage](s.ctx)
		tag, sample := history.LoadDirectSample(member)
		conn, err = member.ListenPacket(ctx, destination)
		if err != nil && ctx.Err() == nil {
			history.DeleteURLTestHistoryIfCurrent(tag, sample)
		}
	} else {
		conn, err = s.singURLTest.ListenPacket(ctx, destination)
	}
	if err != nil {
		s.PerformUpdateCheck()
		return nil, err
	}
	return s.connections.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) AttachConnection(closer io.Closer) func() {
	remove := s.singURLTest.AttachConnection(closer)
	removePinned := s.connections.Add(closer, true)
	return func() { removePinned(); remove() }
}

func (s *Selector) References() []string {
	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	refs := s.singURLTest.References()
	if pinned != "" && !slices.Contains(refs, pinned) {
		refs = append(refs, pinned)
	}
	return refs
}

func (s *Selector) Status() group.AutoSelectorStatus {
	s.mu.Lock()
	pinned := s.pinned
	s.mu.Unlock()
	status := group.AutoSelectorStatus{Tag: s.Tag(), Phase: "running", Strategy: "latency", Pinned: pinned, MembersTotal: len(s.All())}
	if selected := s.Selected(N.NetworkTCP); selected != nil {
		status.Selected = selected.Tag()
	}
	if selected := s.Selected(N.NetworkUDP); selected != nil {
		status.SelectedUDP = selected.Tag()
	}
	history := service.PtrFromContext[urltest.HistoryStorage](s.ctx)
	for rank, tag := range s.All() {
		member := group.AutoSelectorMemberStatus{Tag: tag, Rank: rank, State: "untested", Selected: tag == status.Selected, SelectedUDP: tag == status.SelectedUDP, Active: true}
		if history != nil {
			result := history.LoadURLTestHistory(tag)
			if memberOutbound, ok := s.manager.Outbound(tag); ok {
				if _, nested := memberOutbound.(adapter.OutboundGroup); nested {
					result = nil
					for _, network := range memberOutbound.Network() {
						leaf := group.RealTag(memberOutbound, network)
						if leaf == "" || leaf == tag {
							continue
						}
						if latest := history.LoadURLTestHistory(leaf); latest != nil &&
							(result == nil || latest.Time.After(result.Time)) {
							result = latest
						}
					}
				}
			}
			if result != nil {
				member.State = "ok"
				member.Qualified = true
				member.Samples = 1
				member.AverageMs = int(result.Delay)
				member.MinMs = int(result.Delay)
				member.MaxMs = int(result.Delay)
				member.LastOK = result.Time
				member.LastProbe = result.Time
				status.MembersAlive++
				status.MembersQualified++
				status.MembersProbed++
				if result.Time.After(status.LastRoundAt) {
					status.LastRoundAt = result.Time
				}
			}
		}
		status.Members = append(status.Members, member)
	}
	if !status.LastRoundAt.IsZero() {
		status.NextRoundAt = status.LastRoundAt.Add(s.interval)
		status.RoundsCompleted = 1
	}
	return status
}
