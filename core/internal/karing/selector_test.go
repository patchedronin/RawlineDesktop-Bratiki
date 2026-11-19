package karing

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type fixtureOutbound struct{ outbound.Adapter }
type fixtureManager struct {
	adapter.OutboundManager
	members map[string]adapter.Outbound
}

func (m fixtureManager) Outbound(tag string) (adapter.Outbound, bool) {
	member, ok := m.members[tag]
	return member, ok
}

func (*fixtureOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	panic("not dialed")
}
func (*fixtureOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	panic("not dialed")
}

func TestKaringURLTestSelection(t *testing.T) {
	history := urltest.NewHistoryStorage()
	ctx := service.ContextWithPtr(context.Background(), history)
	first := &fixtureOutbound{outbound.NewAdapter("direct", "first", []string{N.NetworkTCP}, nil)}
	second := &fixtureOutbound{outbound.NewAdapter("direct", "second", []string{N.NetworkTCP, N.NetworkUDP}, nil)}
	store := func(tag string, delay uint16) {
		history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
	}
	g, err := group.NewURLTestGroup(ctx, nil, nil, []adapter.Outbound{first, second}, "", time.Minute, 50, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(network, want string, measured bool) {
		t.Helper()
		got, exists := g.Select(network)
		if got == nil || got.Tag() != want || exists != measured {
			t.Fatalf("%s: got %v/%v, want %s/%v", network, got, exists, want, measured)
		}
	}
	check(N.NetworkTCP, "first", false)
	check(N.NetworkUDP, "second", false)
	store("first", 200)
	store("second", 170)
	check(N.NetworkTCP, "first", true) // The 50 ms tolerance prevents churn.
	store("second", 149)
	check(N.NetworkTCP, "second", true)
	store("first", 80) // Latest successful result, without a sampling average.
	check(N.NetworkTCP, "first", true)
	history.DeleteURLTestHistory("first")
	check(N.NetworkTCP, "second", true)
	history.DeleteURLTestHistory("second")
	check(N.NetworkTCP, "first", false) // All failed: a usable network member remains a fallback.
}

type failingLeaf struct {
	fixtureOutbound
	err error
}

func (f *failingLeaf) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, f.err
}

func (f *failingLeaf) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, f.err
}

func TestFailedDialSwitchesToMeasuredFallback(t *testing.T) {
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		for _, nested := range []bool{false, true} {
			history := urltest.NewHistoryStorage()
			members := make(map[string]adapter.Outbound)
			ctx := service.ContextWithPtr(context.Background(), history)
			ctx = service.ContextWith[adapter.OutboundManager](ctx, fixtureManager{members: members})
			newSelector := func(tag string, outbounds []string, pin string) *Selector {
				t.Helper()
				o, err := New(ctx, nil, log.NewNOPFactory().Logger(), tag,
					option.AutoSelectorOutboundOptions{Outbounds: outbounds, Pinned: pin})
				if err != nil {
					t.Fatal(err)
				}
				s := o.(*Selector)
				if err := s.Start(adapter.StartStateStart, nil); err != nil {
					t.Fatal(err)
				}
				members[tag] = s
				return s
			}
			failure := errors.New("first node failed")
			members["first"] = &failingLeaf{fixtureOutbound{outbound.NewAdapter("direct", "first", []string{network}, nil)}, failure}
			members["second"] = &fixtureOutbound{outbound.NewAdapter("direct", "second", []string{network}, nil)}
			first := "first"
			if nested {
				newSelector("inner", []string{"first"}, "first")
				first = "inner"
			}
			s := newSelector("outer", []string{first, "second"}, "")
			history.StoreURLTestHistory("first", &adapter.URLTestHistory{Time: time.Now(), Delay: 20})
			history.StoreURLTestHistory("second", &adapter.URLTestHistory{Time: time.Now(), Delay: 120})
			s.PerformUpdateCheck()
			if got := s.Selected(network).Tag(); got != first {
				t.Fatalf("%s nested=%v: initial selection %q, want %q", network, nested, got, first)
			}
			var err error
			if network == N.NetworkTCP {
				_, err = s.DialContext(ctx, network, M.Socksaddr{})
			} else {
				_, err = s.ListenPacket(ctx, M.Socksaddr{})
			}
			if !errors.Is(err, failure) || s.Selected(network).Tag() != "second" {
				t.Fatalf("%s nested=%v: got %v and selected %q, want original failure and second", network, nested, err, s.Selected(network).Tag())
			}
			if got := s.Status().Members[1].State; got != "ok" {
				t.Fatalf("successful URL test must report ok, got %q", got)
			}
		}
	}
}

type waitingFailure struct {
	fixtureOutbound
	started, finish chan struct{}
}

func (f *waitingFailure) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	close(f.started)
	<-f.finish
	return nil, errors.New("fixture dial failure")
}

func TestPinnedFailureKeepsProbeCompletedDuringDial(t *testing.T) {
	history := urltest.NewHistoryStorage()
	first := &waitingFailure{
		fixtureOutbound{outbound.NewAdapter("direct", "first", []string{N.NetworkTCP}, nil)},
		make(chan struct{}), make(chan struct{}),
	}
	second := &fixtureOutbound{outbound.NewAdapter("direct", "second", []string{N.NetworkTCP}, nil)}
	ctx := service.ContextWithPtr(context.Background(), history)
	ctx = service.ContextWith[adapter.OutboundManager](ctx, fixtureManager{members: map[string]adapter.Outbound{"first": first, "second": second}})
	o, err := New(ctx, nil, log.NewNOPFactory().Logger(), "test",
		option.AutoSelectorOutboundOptions{Outbounds: []string{"first", "second"}, Pinned: "first"})
	if err != nil {
		t.Fatal(err)
	}
	s := o.(*Selector)
	if err := s.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	history.StoreURLTestHistory("first", &adapter.URLTestHistory{Time: time.Now(), Delay: 40})
	history.StoreURLTestHistory("second", &adapter.URLTestHistory{Time: time.Now(), Delay: 120})
	done := make(chan error, 1)
	go func() {
		_, err := s.DialContext(ctx, N.NetworkTCP, M.Socksaddr{})
		done <- err
	}()
	<-first.started
	newest := &adapter.URLTestHistory{Time: time.Now(), Delay: 20}
	history.StoreURLTestHistory("first", newest)
	close(first.finish)
	if err := <-done; err == nil {
		t.Fatal("fixture did not fail")
	}
	s.SelectOutbound("")
	if history.LoadURLTestHistory("first") != newest || s.Selected(N.NetworkTCP) != first {
		t.Fatal("the late failure invalidated a newer successful sample")
	}
}

func TestRoutedNestedFailureAndCancellation(t *testing.T) {
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		for _, canceled := range []bool{false, true} {
			history := urltest.NewHistoryStorage()
			members := make(map[string]adapter.Outbound)
			ctx := service.ContextWithPtr(context.Background(), history)
			ctx = service.ContextWith[adapter.OutboundManager](ctx, fixtureManager{members: members})
			logger := log.NewNOPFactory().Logger()
			newGroup := func(tag string, children ...string) *Selector {
				t.Helper()
				o, err := New(ctx, nil, logger, tag, option.AutoSelectorOutboundOptions{Outbounds: children, Tolerance: 50})
				if err != nil {
					t.Fatal(err)
				}
				s := o.(*Selector)
				if err := s.Start(adapter.StartStateStart, nil); err != nil {
					t.Fatal(err)
				}
				members[tag] = s
				return s
			}
			requestCtx, cancel := context.WithCancel(ctx)
			failed := errors.New("fixture failure")
			if canceled {
				failed = context.Canceled
			}
			first := &failingLeaf{fixtureOutbound{outbound.NewAdapter("direct", "a", []string{network}, nil)}, failed}
			members["a"] = first
			members["b"] = &fixtureOutbound{outbound.NewAdapter("direct", "b", []string{network}, nil)}
			members["d"] = &fixtureOutbound{outbound.NewAdapter("direct", "d", []string{network}, nil)}
			c := newGroup("c", "a", "b")
			outer := newGroup("outer", "c", "d")
			for tag, delay := range map[string]uint16{"a": 20, "b": 100, "d": 200} {
				history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
			}
			c.PerformUpdateCheck()
			outer.PerformUpdateCheck()
			if got := outer.Status().Members[0]; got.State != "ok" || got.AverageMs != 20 {
				t.Fatalf("nested first sample should be reported, got %+v", got)
			}
			if canceled {
				cancel()
			}
			manager := route.NewConnectionManager(ctx, logger)
			metadata := adapter.InboundContext{
				Destination:   M.Socksaddr{Fqdn: "fixture.invalid", Port: 443},
				OutboundChain: []adapter.Outbound{outer, c, first},
			}
			var gotErr error
			closeCallback := func(err error) { gotErr = err }
			if network == N.NetworkTCP {
				conn, peer := net.Pipe()
				manager.NewConnection(requestCtx, first, conn, metadata, closeCallback)
				peer.Close()
			} else {
				conn, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				manager.NewPacketConnection(requestCtx, first, bufio.NewPacketConn(conn), metadata, closeCallback)
				conn.Close()
			}
			cancel()
			want := "b"
			if canceled {
				want = "a"
			}
			if !errors.Is(gotErr, failed) || outer.Selected(network) != c || c.Selected(network).Tag() != want {
				t.Fatalf("%s canceled=%v: error=%v outer=%s inner=%s, want c -> %s", network, canceled,
					gotErr, outer.Selected(network).Tag(), c.Selected(network).Tag(), want)
			}
			if (history.LoadURLTestHistory("a") != nil) != canceled {
				t.Fatalf("unexpected history after %s canceled=%v", network, canceled)
			}
		}
	}
}

var _ adapter.OutboundGroup = (*changingGroup)(nil)

type changingGroup struct {
	fixtureOutbound
	member, replacement adapter.Outbound
}

func (g *changingGroup) Selected(string) adapter.Outbound { return g.member }
func (g *changingGroup) All() []string                    { return []string{g.member.Tag(), g.replacement.Tag()} }
func (*changingGroup) AttachConnection(io.Closer) func()  { return func() {} }
func (g *changingGroup) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	g.member = g.replacement
	return g.member.DialContext(ctx, network, dest)
}

func TestParentDoesNotDeleteHistoryForPreviouslySelectedChild(t *testing.T) {
	history := urltest.NewHistoryStorage()
	ctx := service.ContextWithPtr(context.Background(), history)
	networks := []string{N.NetworkTCP}
	a := &fixtureOutbound{outbound.NewAdapter("direct", "a", networks, nil)}
	failure := errors.New("replacement failed")
	b := &failingLeaf{fixtureOutbound{outbound.NewAdapter("direct", "b", networks, nil)}, failure}
	c := &changingGroup{fixtureOutbound{outbound.NewAdapter("selector", "c", networks, nil)}, a, b}
	members := map[string]adapter.Outbound{"c": c}
	ctx = service.ContextWith[adapter.OutboundManager](ctx, fixtureManager{members: members})
	o, err := New(ctx, nil, log.NewNOPFactory().Logger(), "p",
		option.AutoSelectorOutboundOptions{Outbounds: []string{"c"}, Pinned: "c"})
	if err != nil {
		t.Fatal(err)
	}
	s := o.(*Selector)
	if err := s.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	sample := &adapter.URLTestHistory{Time: time.Now(), Delay: 20}
	history.StoreURLTestHistory("a", sample)
	if _, err := s.DialContext(ctx, N.NetworkTCP, M.Socksaddr{}); !errors.Is(err, failure) {
		t.Fatalf("got %v, want the replacement's error", err)
	}
	if history.LoadURLTestHistory("a") != sample {
		t.Fatal("parent invalidated a leaf it did not dial")
	}
}

func TestIncumbentToleranceAndManualSelection(t *testing.T) {
	history := urltest.NewHistoryStorage()
	first := &fixtureOutbound{outbound.NewAdapter("direct", "first", []string{N.NetworkTCP}, nil)}
	second := &fixtureOutbound{outbound.NewAdapter("direct", "second", []string{N.NetworkTCP, N.NetworkUDP}, nil)}
	ctx := service.ContextWithPtr(context.Background(), history)
	ctx = service.ContextWith[adapter.OutboundManager](ctx, fixtureManager{members: map[string]adapter.Outbound{"first": first, "second": second}})
	created, err := New(ctx, nil, nil, "test", option.AutoSelectorOutboundOptions{Outbounds: []string{"first", "second"}, Tolerance: 50, Pinned: "outside-the-group"})
	if err != nil {
		t.Fatal(err)
	}
	s := created.(*Selector)
	if err = s.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	store := func(tag string, delay uint16) {
		history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
	}
	check := func(want string) {
		t.Helper()
		s.PerformUpdateCheck()
		if got := s.Selected(N.NetworkTCP).Tag(); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
	store("first", 200)
	store("second", 100)
	check("second")
	store("first", 60)
	check("second")
	store("first", 49)
	check("first")
	if s.SelectOutbound("outside-the-group") || !s.SelectOutbound("second") {
		t.Fatal("manual membership check")
	}
	check("second")
	if !s.SelectOutbound("") {
		t.Fatal("return to automatic selection")
	}
	check("first")
	history.DeleteURLTestHistory("first")
	check("second")
	if status := s.Status(); status.MembersAlive != 1 || status.Selected != "second" || status.SelectedUDP != "second" {
		t.Fatalf("status: %+v", status)
	}
}
