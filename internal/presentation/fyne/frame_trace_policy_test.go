package fyneui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func TestFrameTraceBoundAndClock(t *testing.T) {
	trace := newFrameTrace(100)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 10 {
				trace.add(frameTraceRecord{Stage: "probe"})
			}
		})
	}
	wg.Wait()
	var out bytes.Buffer
	if err := trace.dump(&out); err != nil {
		t.Fatal(err)
	}
	for i, r := range trace.records {
		if r.Sequence != i+1 || r.NS < 0 || (i > 0 && r.NS < trace.records[i-1].NS) {
			t.Fatalf("unordered record %d: %+v", i, r)
		}
	}
	trace.add(frameTraceRecord{Stage: "overflow"})
	out.Reset()
	if err := trace.dump(&out); err == nil || !strings.Contains(out.String(), `"overflow":true`) || len(trace.records) != 100 {
		t.Fatal("overflow must be bounded, persisted and fail")
	}
	if trace.check() == nil {
		t.Fatal("overflow must prevent successful benchmark record")
	}
	trace = newFrameTrace(1)
	trace.add(frameTraceRecord{Packets: make([]tracePacketID, frameTracePacketLimit+1)})
	if trace.check() == nil || len(trace.records) != 0 {
		t.Fatal("oversized identity list retained")
	}
}

func TestFrameTraceMailboxIdentity(t *testing.T) {
	for _, observed := range []bool{false, true} {
		trace := newFrameTrace(10)
		var observer engineObserver
		if observed {
			observer = func(e engineTraceEvent) { trace.add(frameTraceRecord{Stage: e.Stage, Picture: e.Picture, Old: e.Old}) }
		}
		ch := make(chan snapshot, 1)
		id := pictureIdentity{7, 24, 99}
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		emitObservedSnapshot(ch, snapshot{picture: img, pictureID: id, role: "sender"}, observer)
		emitObservedSnapshot(ch, snapshot{key: "status.busy", role: "sender"}, observer)
		got := <-ch
		if got.picture != img || got.pictureID != id || got.key != "status.busy" {
			t.Fatal("coalesced identity must follow pending picture")
		}
		emitObservedSnapshot(ch, got, observer)
		next := pictureIdentity{8, 36, 99}
		emitObservedSnapshot(ch, snapshot{picture: img, pictureID: next, role: "sender"}, observer)
		if got = <-ch; got.pictureID != next {
			t.Fatal("replacement retained old identity")
		}
		if observed && (trace.records[1].Old != id || trace.records[1].Picture != id || trace.records[4].Old != id || trace.records[4].Picture != next) {
			t.Fatal("replacement trace identities lost")
		}
		emitObservedSnapshot(ch, got, nil)
		emitSnapshot(ch, snapshot{role: "receiver"})
		if got = <-ch; got.picture != nil || got.pictureID != (pictureIdentity{}) {
			t.Fatal("role switch must not carry picture")
		}
	}
}

type traceFakeCapture struct {
	frame  platform.Frame
	err    error
	closed bool
}

func (s *traceFakeCapture) Next(context.Context) (platform.Frame, error) { return s.frame, s.err }
func (s *traceFakeCapture) Close() error                                 { s.closed = true; return s.err }

type traceFakeReceiver struct {
	err      error
	accepted v1.Packet
	closed   bool
}

func (r *traceFakeReceiver) Close() error                            { r.closed = true; return r.err }
func (r *traceFakeReceiver) CheckDescriptor(v1.Descriptor) error     { return r.err }
func (r *traceFakeReceiver) Accept(p v1.Packet) error                { r.accepted = p; return r.err }
func (r *traceFakeReceiver) Publish() error                          { return r.err }
func (r *traceFakeReceiver) DurableBytes() (uint64, error)           { return 512, r.err }
func (r *traceFakeReceiver) PublishedFiles() (uint64, uint64, error) { return 1, 2, r.err }

//nolint:errorlint // Wrappers must return the exact original error, not merely an equivalent chain.
func TestFrameTraceWrappersPreserveLinksAndSemantics(t *testing.T) {
	fatal := errors.New("fatal sentinel")
	for _, wantErr := range []error{nil, storage.ErrMetadata, fatal} {
		trace := newFrameTrace(30)
		s := &traceFakeCapture{frame: platform.Frame{CapturedAt: time.Unix(123, 456)}}
		r := &traceFakeReceiver{err: wantErr}
		p := v1.Packet{Kind: 2, FileID: 3, Index: 4, Count: 5, Size: 6, Payload: []byte("secret")}
		b := v1.Beacon{Frame: 7, Ordinal: 8, Epoch: 9}
		base := engineIO{
			openCapture: func(uint32) (captureStream, error) { return s, nil },
			decode:      func(image.Image) (v1.Beacon, []v1.Packet, error) { return b, []v1.Packet{p}, wantErr },
			openReceive: func(string, string, v1.Descriptor) (receiveOwner, error) { return r, nil },
		}
		wrapped := trace.wrap(base)
		stream, _ := wrapped.openCapture(1)
		f, err := stream.Next(context.Background())
		if err != nil || f.CapturedAt != s.frame.CapturedAt {
			t.Fatal("capture changed")
		}
		gotB, gotP, err := wrapped.decode(nil)
		if err != wantErr || gotB != b || len(gotP) != 1 || &gotP[0].Payload[0] != &p.Payload[0] {
			t.Fatal("decode changed")
		}
		owner, _ := wrapped.openReceive("", "", v1.Descriptor{})
		if owner.Accept(p) != wantErr || owner.Publish() != wantErr || owner.CheckDescriptor(v1.Descriptor{}) != wantErr {
			t.Fatal("receiver error changed")
		}
		if n, e := owner.DurableBytes(); n != 512 || e != wantErr {
			t.Fatal("durable result changed")
		}
		if n, total, e := owner.PublishedFiles(); n != 1 || total != 2 || e != wantErr {
			t.Fatal("published result changed")
		}
		if owner.Close() != wantErr || !r.closed || stream.Close() != nil || !s.closed {
			t.Fatal("close delegation changed")
		}
		for _, e := range trace.records {
			if e.Capture != 1 {
				t.Fatal("capture linkage lost")
			}
		}
		end := trace.records[3]
		if end.Picture != (pictureIdentity{7, 8, 9}) || end.Packets[0] != packetIdentity(p) || end.Status != traceStatus(wantErr) {
			t.Fatal("decode identity/status lost")
		}
		if *trace.records[1].CapturedDeltaNS != 0 {
			t.Fatal("first native timestamp delta must be zero")
		}
		var out bytes.Buffer
		if err := trace.dump(&out); err != nil || strings.Contains(out.String(), "secret") {
			t.Fatal("payload leaked or dump failed")
		}
		var disabled *frameTrace
		plain := disabled.wrap(base)
		plainStream, _ := plain.openCapture(1)
		plainOwner, _ := plain.openReceive("", "", v1.Descriptor{})
		if plainStream != s || plainOwner != r || plain.observer != nil {
			t.Fatal("nil trace must not wrap boundaries")
		}
		disabled.add(frameTraceRecord{})
	}
}

func TestFrameTraceArtifactPolicy(t *testing.T) {
	t.Setenv("RASTERDOCK_FRAME_TRACE", "")
	trace, finish, err := benchmarkFrameTrace()
	if trace != nil || err != nil || finish() != nil {
		t.Fatal("disabled trace must do nothing")
	}
	t.Setenv("RASTERDOCK_FRAME_TRACE", "1")
	dir := t.TempDir()
	t.Setenv("RASTERDOCK_FRAME_TRACE_DIR", dir)
	t.Setenv("RASTERDOCK_COLOR_BENCHMARK_TRIAL", "../escape")
	if _, _, err = benchmarkFrameTrace(); err == nil {
		t.Fatal("unsafe trial accepted")
	}
	name := "repeat1_position2_cells4"
	t.Setenv("RASTERDOCK_COLOR_BENCHMARK_TRIAL", name)
	trace, finish, err = benchmarkFrameTrace()
	if err != nil || trace.limit != frameTraceLimit {
		t.Fatal("trace setup failed")
	}
	trace.add(frameTraceRecord{Stage: "probe"})
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, name+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if finish() == nil {
		t.Fatal("must not overwrite existing artifact")
	}
}

func TestFrameTraceRenderMetadata(t *testing.T) {
	p := prepareFixture(t)
	capacity := &viewportCapacity{}
	capacity.store(image.Pt(3000, 2000), image.Point{})
	trace := newFrameTrace(10)
	io := trace.wrap(engineIO{viewport: capacity})
	s, err := io.render(p, application.DefaultVisualProfile(application.Colors8), 0, 17, 31)
	if err != nil || s.picture == nil || s.pictureID != (pictureIdentity{17, 0, 31}) {
		t.Fatalf("render identity missing: %+v %v", s.pictureID, err)
	}
	if len(trace.records) != 2 || trace.records[0].Stage != "render.begin" || trace.records[1].Stage != "render.end" || trace.records[1].Picture != s.pictureID {
		t.Fatal("render boundaries missing")
	}
	capacity.store(image.Point{}, image.Point{})
	if _, err = io.render(p, application.DefaultVisualProfile(application.Colors8), 0, 18, 31); err == nil || trace.records[3].Status != "error" {
		t.Fatal("failed render not traced")
	}
}
