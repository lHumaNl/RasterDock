package fyneui

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"sync"
	"time"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	v1 "rasterdock/internal/protocol/v1"
)

const frameTraceLimit = 100000
const frameTracePacketLimit = 1024

type tracePacketID struct {
	Kind       byte   `json:"kind"`
	File       uint32 `json:"file"`
	Index      uint64 `json:"index"`
	Count      uint64 `json:"count"`
	Size       uint64 `json:"size"`
	PayloadLen int    `json:"payload_len"`
}

func packetIdentity(p v1.Packet) tracePacketID {
	return tracePacketID{p.Kind, p.FileID, p.Index, p.Count, p.Size, len(p.Payload)}
}

type frameTraceRecord struct {
	Sequence        int             `json:"sequence"`
	NS              int64           `json:"ns"`
	Stage           string          `json:"stage"`
	Picture         pictureIdentity `json:"picture"`
	Old             pictureIdentity `json:"old_picture"`
	Capture         uint64          `json:"capture_sequence"`
	CapturedAt      string          `json:"captured_at,omitempty"`
	CapturedDeltaNS *int64          `json:"captured_delta_ns,omitempty"`
	Packets         []tracePacketID `json:"packets,omitempty"`
	Status          string          `json:"status,omitempty"`
	Bytes           uint64          `json:"durable_bytes,omitempty"`
	Published       uint64          `json:"published,omitempty"`
	Total           uint64          `json:"total,omitempty"`
}

// Only bounded identities are retained, never images, payloads or error text.
// NS uses one process monotonic clock; captured_delta_ns is relative to the
// first nonzero CapturedAt, NOT this clock (native timestamps may lack monotonic data).
type frameTrace struct {
	mu       sync.Mutex
	start    time.Time
	records  []frameTraceRecord
	limit    int
	overflow bool
	// Receiver worker owns these correlation fields, including all wrappers.
	capture      uint64
	firstCapture time.Time
}

func newFrameTrace(limit int) *frameTrace {
	return &frameTrace{start: time.Now(), limit: limit, records: make([]frameTraceRecord, 0, limit)}
}

func (t *frameTrace) add(r frameTraceRecord) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(r.Packets) > frameTracePacketLimit {
		t.overflow = true
		return
	}
	if len(t.records) == t.limit {
		t.overflow = true
		return
	}
	r.Sequence = len(t.records) + 1
	r.NS = time.Since(t.start).Nanoseconds()
	t.records = append(t.records, r)
}

func (t *frameTrace) check() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.overflow {
		return errors.New("frame trace overflow: diagnostic incomplete")
	}
	return nil
}

func traceStatus(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, storage.ErrMetadata):
		return "metadata"
	case errors.Is(err, platform.ErrFramePending):
		return "pending"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "error"
	}
}

// Called only after engines join and measurement ends. Overflow is persisted
// explicitly and fails the diagnostic even when transfer itself succeeded.
func (t *frameTrace) dump(w io.Writer) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := json.NewEncoder(w)
	for _, r := range t.records {
		if err := e.Encode(r); err != nil {
			return err
		}
	}
	if err := e.Encode(map[string]interface{}{"stage": "trace.summary", "overflow": t.overflow, "limit": t.limit, "records": len(t.records), "clock": "helper monotonic relative ns; captured_delta_ns separate CapturedAt clock", "ui_complete": "application only, not compositor presentation"}); err != nil {
		return err
	}
	if t.overflow {
		return errors.New("frame trace overflow: diagnostic incomplete")
	}
	return nil
}

func (t *frameTrace) wrap(base engineIO) engineIO {
	if t == nil {
		return base
	}
	if base.viewport != nil {
		base.observer = func(e engineTraceEvent) {
			t.add(frameTraceRecord{Stage: e.Stage, Picture: e.Picture, Old: e.Old, Status: traceStatus(e.Err)})
		}
	}
	if open := base.openCapture; open != nil {
		base.openCapture = func(id uint32) (captureStream, error) {
			s, err := open(id)
			if err != nil {
				return s, err
			}
			return &traceCapture{captureStream: s, trace: t}, nil
		}
	}
	if decode := base.decode; decode != nil {
		base.decode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
			t.add(frameTraceRecord{Stage: "decode.begin", Capture: t.capture})
			b, packets, err := decode(img)
			ids := make([]tracePacketID, len(packets))
			for i, p := range packets {
				ids[i] = packetIdentity(p)
			}
			t.add(frameTraceRecord{Stage: "decode.end", Capture: t.capture, Picture: pictureIdentity{b.Frame, b.Ordinal, b.Epoch}, Packets: ids, Status: traceStatus(err)})
			return b, packets, err
		}
	}
	if open := base.openReceive; open != nil {
		base.openReceive = func(session, destination string, d v1.Descriptor) (receiveOwner, error) {
			r, err := open(session, destination, d)
			if err != nil {
				return r, err
			}
			return &traceReceiver{receiveOwner: r, trace: t}, nil
		}
	}
	return base
}

type traceCapture struct {
	captureStream
	trace *frameTrace
}

func (s *traceCapture) Next(ctx context.Context) (platform.Frame, error) {
	t := s.trace
	t.capture++
	t.add(frameTraceRecord{Stage: "capture.begin", Capture: t.capture})
	f, err := s.captureStream.Next(ctx)
	r := frameTraceRecord{Stage: "capture.end", Capture: t.capture, Status: traceStatus(err)}
	if !f.CapturedAt.IsZero() {
		if t.firstCapture.IsZero() {
			t.firstCapture = f.CapturedAt
		}
		delta := f.CapturedAt.Sub(t.firstCapture).Nanoseconds()
		r.CapturedAt, r.CapturedDeltaNS = f.CapturedAt.Format(time.RFC3339Nano), &delta
	}
	t.add(r)
	return f, err
}

type traceReceiver struct {
	receiveOwner
	trace *frameTrace
}

func (r *traceReceiver) Accept(p v1.Packet) error {
	e := frameTraceRecord{Stage: "accept.begin", Capture: r.trace.capture, Packets: []tracePacketID{packetIdentity(p)}}
	r.trace.add(e)
	err := r.receiveOwner.Accept(p)
	e.Stage, e.Status = "accept.end", traceStatus(err)
	r.trace.add(e)
	return err
}

func (r *traceReceiver) Publish() error {
	r.trace.add(frameTraceRecord{Stage: "publish.begin", Capture: r.trace.capture})
	err := r.receiveOwner.Publish()
	r.trace.add(frameTraceRecord{Stage: "publish.end", Capture: r.trace.capture, Status: traceStatus(err)})
	return err
}

func (r *traceReceiver) DurableBytes() (uint64, error) {
	n, err := r.receiveOwner.DurableBytes()
	r.trace.add(frameTraceRecord{Stage: "durable.sample", Capture: r.trace.capture, Bytes: n, Status: traceStatus(err)})
	return n, err
}

func (r *traceReceiver) PublishedFiles() (uint64, uint64, error) {
	n, total, err := r.receiveOwner.PublishedFiles()
	r.trace.add(frameTraceRecord{Stage: "published.sample", Capture: r.trace.capture, Published: n, Total: total, Status: traceStatus(err)})
	return n, total, err
}
