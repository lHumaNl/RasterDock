// Package fyneui adapts transfer operations to a single native Fyne window.
package fyneui

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
	"rasterdock/internal/presentation"
	"rasterdock/locales"
)

type command struct {
	kind, source, destination, session string
	window                             uint32
}
type snapshot struct {
	role     string
	key      string
	params   map[string]interface{}
	picture  image.Image
	sources  []platform.Source
	session  string
	prepared string
}

// Run starts the public GUI; all I/O and capture belong to one background engine.
func Run(root string, catalog *locales.Catalog, logger *slog.Logger) {
	a := newTransientApp()
	w := a.NewWindow(windowTitle(catalog, "idle", os.Getpid()))
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan command, 8)
	events := make(chan snapshot, 1)
	done := make(chan struct{})
	go func() { defer close(done); engine(ctx, root, commands, events, logger) }()
	source, destination, session := widget.NewEntry(), widget.NewEntry(), widget.NewEntry()
	status := widget.NewLabel(catalog.Text("status.idle"))
	status.Wrapping = fyne.TextWrapWord
	saved, savedControl := savedPath(catalog, a.Clipboard())
	picture := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	viewport := newQRViewport(picture, w.Canvas())
	windows := localizedSelect(catalog)
	var sources []platform.Source
	send := func(kind string) {
		id := uint32(0)
		for i, label := range windows.Options {
			if windows.Selected == label && i < len(sources) {
				id = sources[i].ID
				break
			}
		}
		cmd := command{kind: kind, source: source.Text, destination: destination.Text, session: session.Text, window: id}
		select {
		case commands <- cmd:
		default:
			status.SetText(catalog.Text("status.busy"))
		}
	}
	folder := func(entry *widget.Entry) {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if uri != nil {
				entry.SetText(uri.Path())
			}
		}, w)
	}
	file := func() {
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if reader != nil {
				source.SetText(reader.URI().Path())
				if err = reader.Close(); err != nil {
					dialog.ShowError(err, w)
				}
			}
		}, w)
	}
	open := func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if uri != nil {
				select {
				case commands <- command{kind: "open", source: uri.Path()}:
				default:
					status.SetText(catalog.Text("status.busy"))
				}
			}
		}, w)
	}
	button := func(key, kind string) *widget.Button {
		return widget.NewButton(catalog.Text(key), func() { send(kind) })
	}
	note := widget.NewLabel(catalog.Text("profile.note"))
	note.Wrapping = fyne.TextWrapWord
	chooser := &savedChooser{ctx: ctx, state: filepath.Join(root, "state"), catalog: catalog, window: w}
	chooseSend := func(item storage.SavedTransfer) { enqueueOpen(commands, status, catalog, item.Directory) }
	chooseReceive := func(item storage.SavedReceive) { selectReceive(item, session, destination, windows) }
	controls := container.NewVBox(note, widget.NewLabel(catalog.Text("saved.label")), savedControl,
		widget.NewButton(catalog.Text("action.saved"), func() { chooser.show(false, chooseSend, chooseReceive) }),
		widget.NewButton(catalog.Text("action.sessions"), func() { chooser.show(true, chooseSend, chooseReceive) }),
		widget.NewLabel(catalog.Text("source.label")), source,
		container.NewVBox(widget.NewButton(catalog.Text("action.folder"), func() { folder(source) }), widget.NewButton(catalog.Text("action.file"), file), button("action.prepare", "prepare"), widget.NewButton(catalog.Text("action.open"), open)),
		widget.NewLabel(catalog.Text("destination.label")), destination, widget.NewButton(catalog.Text("action.folder"), func() { folder(destination) }),
		widget.NewLabel(catalog.Text("capture.label")), windows, button("action.windows", "windows"),
		widget.NewLabel(catalog.Text("session.label")), session,
		container.NewVBox(button("action.receive", "receive"), button("action.pause", "pause")),
		container.NewVBox(button("action.previous", "previous"), button("action.next", "next"), button("action.auto", "auto")), status)
	split := container.NewHSplit(container.NewScroll(controls), viewport)
	split.SetOffset(0.4)
	w.SetContent(split)
	w.Resize(fyne.NewSize(1150, 800))
	var closing atomic.Bool
	w.SetCloseIntercept(func() {
		if closing.Swap(true) {
			return
		}
		cancel()
		go func() { <-done; chooser.workers.Wait(); fyne.Do(func() { w.SetCloseIntercept(nil); w.Close() }) }()
	})
	// One bounded UI delivery at a time; persistence only writes a latest-value mailbox.
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case event := <-events:
					fyne.DoAndWait(func() {
						w.SetTitle(windowTitle(catalog, event.role, os.Getpid()))
						status.SetText(catalog.Text(event.key, event.params))
						if event.picture != nil {
							picture.Image = event.picture
							picture.Refresh()
							viewport.Refresh()
						}
						if event.sources != nil {
							sources = event.sources
							options := make([]string, len(sources))
							for i, s := range sources {
								options[i] = fmt.Sprintf("%d · %s", s.ID, s.Title)
							}
							windows.Options = options
							windows.Refresh()
						}
						if event.session != "" {
							session.SetText(event.session)
						}
						if event.prepared != "" {
							saved.SetText(event.prepared)
						}
					})
				default:
				}
			}
		}
	}()
	w.ShowAndRun()
	cancel()
}

func engine(ctx context.Context, root string, commands <-chan command, events chan snapshot, logger *slog.Logger) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	runEngine(ctx, root, commands, events, logger, engineIO{
		ticks: tick.C, capture: platform.Capture, decode: codec.Decode, openReceive: openReceive,
	})
}

func runEngine(ctx context.Context, root string, commands <-chan command, events chan snapshot, logger *slog.Logger, io engineIO) {
	role := "idle"
	emit := func(s snapshot) {
		s.role = role // Carry the successful role even when earlier mailbox values were coalesced.
		select {
		case events <- s:
		default:
			select {
			case <-events:
			default:
			}
			select {
			case events <- s:
			default:
			}
		}
	}
	report := func(err error) {
		logger.Error("operation_failed", "error", err)
		emit(snapshot{key: "status.error", params: map[string]interface{}{"error": err.Error()}})
	}
	var prepared *application.Prepared
	var receiver receiveOwner
	var mode string
	var ordinal, frame, epoch uint64
	var playback *presentation.Playback
	var receive command
	// Every terminal transition relinquishes both the receiver and its command.
	stop := func() error {
		mode = ""
		receive = command{}
		if receiver == nil {
			return nil
		}
		err := receiver.Close()
		receiver = nil
		return err
	}
	fail := func(err error) {
		report(errors.Join(err, stop()))
	}
	defer func() {
		if err := stop(); err != nil {
			logger.Error("close_failed", "error", err)
		}
	}()
	show := func() error {
		if prepared == nil {
			return errors.New("open or prepare a transfer first")
		}
		p, err := prepared.Packet(ordinal)
		if err != nil {
			return err
		}
		frame++
		img, err := codec.Render(prepared.Descriptor, p, ordinal, frame, epoch)
		if err != nil {
			return err
		}
		role = "sender"
		emit(snapshot{key: "status.frame", params: map[string]interface{}{"ordinal": ordinal + 1, "total": prepared.PacketCount()}, picture: img, prepared: prepared.Directory})
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-commands:
			if cmd.kind == "pause" {
				if err := stop(); err != nil {
					report(err)
				} else {
					emit(snapshot{key: "status.paused"})
				}
				continue
			}
			if mode != "" {
				emit(snapshot{key: "status.busy"})
				continue
			}
			var err error
			switch cmd.kind {
			case "prepare", "open":
				if cmd.kind == "prepare" {
					prepared, err = application.Prepare(ctx, cmd.source, filepath.Join(root, "state"), 512, storage.Flush)
				} else {
					prepared, err = application.OpenPrepared(cmd.source)
				}
				if err == nil {
					var id [16]byte
					id, err = domain.NewID()
					epoch = binary.BigEndian.Uint64(id[:8])
					ordinal = 0
					frame = 0
					playback = presentation.NewPlayback(prepared.ManifestPackets(), prepared.PacketCount())
					if err == nil {
						err = show()
					}
				}
			case "next", "previous":
				if prepared == nil {
					err = errors.New("no prepared transfer")
					break
				}
				if cmd.kind == "next" {
					ordinal = (ordinal + 1) % prepared.PacketCount()
				} else if ordinal > 0 {
					ordinal--
				}
				err = show()
			case "auto":
				if prepared == nil {
					err = errors.New("no prepared transfer")
				} else {
					mode = "auto"
				}
			case "windows":
				var sources []platform.Source
				sources, err = platform.Sources()
				if err == nil {
					emit(snapshot{key: "status.idle", sources: sources})
				}
			case "receive":
				// Never assign a new destination/session to an existing owner.
				if err = stop(); err != nil {
					break
				}
				if cmd.window == 0 || cmd.destination == "" {
					err = errors.New("select capture window and destination")
					break
				}
				var info os.FileInfo
				info, err = os.Stat(cmd.destination)
				if err == nil && !info.IsDir() {
					err = errors.New("destination is not a directory")
				}
				if err == nil {
					receive = cmd
					mode = "receive"
					role = "receiver"
					emit(snapshot{key: "status.waiting"})
				}
			}
			if err != nil {
				fail(err)
			}
		case <-io.ticks:
			if mode == "auto" {
				ordinal = playback.Next()
				if err := show(); err != nil {
					fail(err)
				}
			}
			if mode != "receive" {
				continue
			}
			capture, err := io.capture(receive.window)
			if err != nil {
				fail(err)
				continue
			}
			beacon, packets, err := io.decode(capture.Image)
			if err != nil {
				continue
			}
			if receiver == nil {
				if receive.session == "" {
					id, e := domain.NewID()
					if e != nil {
						fail(e)
						continue
					}
					receive.session = filepath.Join(root, "state", "transfers", domain.IDString(beacon.Transfer), "receive", domain.IDString(id))
				}
				receiver, err = io.openReceive(receive.session, receive.destination, beacon.Descriptor)
			} else {
				err = receiver.CheckDescriptor(beacon.Descriptor)
			}
			if err == nil {
				for _, p := range packets {
					err = receiver.Accept(p)
					if errors.Is(err, storage.ErrMetadata) {
						err = nil
						continue
					}
					if err != nil {
						break
					}
				}
			}
			if err == nil {
				err = receiver.Publish()
				if errors.Is(err, storage.ErrMetadata) {
					err = nil
				}
			}
			if err != nil {
				fail(err)
				continue
			}
			n, err := receiver.DurableBytes()
			if err != nil {
				fail(err)
				continue
			}
			published, total, err := receiver.PublishedFiles()
			if err != nil {
				fail(err)
				continue
			}
			emit(snapshot{key: "status.receiving", params: map[string]interface{}{"bytes": n, "path": receive.session, "published": published, "total": total}, session: receive.session})
		}
	}
}
