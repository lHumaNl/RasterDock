package fyneui

import (
	"context"
	"image"
	"testing"
	"testing/synctest"
	"time"
)

func TestDeliveryReadyWithoutRefreshTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan snapshot, 1)
		events <- snapshot{revision: 1}
		var applied uint64
		go deliverSnapshots(ctx, events, make(chan time.Time), func(s snapshot) { applied = s.revision }, func() { t.Error("unexpected refresh") })
		synctest.Wait()
		if applied != 1 {
			t.Fatal("ready snapshot was gated on a refresh tick")
		}
	})
}

type deliveryProbe struct {
	events    chan snapshot
	ticks     chan time.Time
	release   chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	seen      []snapshot
	refreshed int
}

func newDeliveryProbe(t *testing.T) *deliveryProbe {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := &deliveryProbe{events: make(chan snapshot, 1), ticks: make(chan time.Time, 1), release: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(p.done)
		deliverSnapshots(ctx, p.events, p.ticks, func(s snapshot) { p.seen = append(p.seen, s); <-p.release }, func() { p.refreshed++ })
	}()
	t.Cleanup(func() { cancel(); close(p.release); <-p.done })
	return p
}

func deliveryPicture(frame uint64) snapshot {
	return snapshot{role: "sender", picture: image.NewRGBA(image.Rect(0, 0, 1, 1)), pictureID: pictureIdentity{Frame: frame}, revision: frame}
}

func TestDeliveryBlockedApplyCoalescesSerially(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newDeliveryProbe(t)
		emitSnapshot(p.events, deliveryPicture(1))
		synctest.Wait()
		emitSnapshot(p.events, deliveryPicture(2))
		emitSnapshot(p.events, deliveryPicture(3))
		p.ticks <- time.Time{}
		synctest.Wait()
		if len(p.events) != 1 || len(p.seen) != 1 || p.refreshed != 0 {
			t.Fatal("blocked callback must bound and serialize delivery")
		}
		p.release <- struct{}{}
		synctest.Wait()
		if len(p.seen) != 2 || p.seen[1].pictureID.Frame != 3 {
			t.Fatal("latest pending picture not applied")
		}
	})
}

func TestDeliveryPendingPictureIdentityAndRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newDeliveryProbe(t)
		emitSnapshot(p.events, deliveryPicture(1))
		synctest.Wait()
		pending := deliveryPicture(2)
		emitSnapshot(p.events, pending)
		emitSnapshot(p.events, snapshot{role: "sender", key: "status.busy", revision: 3})
		p.release <- struct{}{}
		synctest.Wait()
		got := p.seen[1]
		if got.picture != pending.picture || got.pictureID != pending.pictureID || got.revision != 3 || got.key != "status.busy" {
			t.Fatal("coalesced snapshot lost pending picture or acknowledgment")
		}
	})
}

func TestDeliveryCancelJoinsBlockedApply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newDeliveryProbe(t)
		emitSnapshot(p.events, deliveryPicture(1))
		synctest.Wait()
		emitSnapshot(p.events, deliveryPicture(2))
		p.cancel()
		synctest.Wait()
		select {
		case <-p.done:
			t.Fatal("returned before synchronous callback finished")
		default:
		}
		p.release <- struct{}{}
		<-p.done
		if len(p.seen) != 1 || len(p.events) != 1 {
			t.Fatal("applied pending picture after cancellation")
		}
	})
}

func TestDeliveryRefreshWithoutFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newDeliveryProbe(t)
		p.ticks <- time.Time{}
		synctest.Wait()
		if p.refreshed != 1 || len(p.seen) != 0 {
			t.Fatal("dimensions refresh depends on a frame")
		}
	})
}

func TestDeliveryClosedChannels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newDeliveryProbe(t)
		close(p.ticks)
		synctest.Wait()
		emitSnapshot(p.events, deliveryPicture(1))
		synctest.Wait()
		if len(p.seen) != 1 || p.refreshed != 0 {
			t.Fatal("closed refresh channel changed image delivery")
		}
		close(p.events)
		p.release <- struct{}{}
		<-p.done
	})
}

func TestDeliveryCanceledBeforeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan snapshot, 1)
	events <- deliveryPicture(1)
	deliverSnapshots(ctx, events, nil, func(snapshot) { t.Fatal("applied after cancellation") }, nil)
	if len(events) != 1 {
		t.Fatal("consumed pending event after cancellation")
	}
}
