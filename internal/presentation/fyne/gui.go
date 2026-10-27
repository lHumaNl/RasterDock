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

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
	"rasterdock/internal/presentation"
	"rasterdock/locales"
)

// Run starts the public GUI; all I/O and capture belong to one background engine.
func Run(root string, catalog *locales.Catalog, logger *slog.Logger) {
	a := newTransientApp()
	w := a.NewWindow(windowTitle(catalog, "idle", os.Getpid()))
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan command, 8)
	events := make(chan snapshot, 1)
	done := make(chan struct{})
	capacity := &viewportCapacity{}
	control := &controlUI{window: w, catalog: catalog, commands: commands}
	control.focusState = func() (bool, uint64) {
		return platform.OwnWindowFocusState(windowTitle(catalog, "sender", os.Getpid()))
	}
	go func() {
		defer close(done)
		engine(ctx, root, commands, events, logger, capacity, control.focused, func() { control.authorized.Store(false) })
	}()
	source, destination, session := widget.NewEntry(), widget.NewEntry(), widget.NewEntry()
	status := widget.NewLabel(catalog.Text("status.idle"))
	status.Wrapping = fyne.TextWrapWord
	var profiles *visualControls
	profiles = newVisualControls(catalog, func(profile application.VisualProfile) {
		if !profiles.enqueue(commands, profile) {
			status.SetText(catalog.Text("status.busy"))
		}
	})
	saved, savedControl := savedPath(catalog, a.Clipboard())
	picture := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	viewport := newQRViewport(picture, w.Canvas(), capacity)
	control.clear = func() { picture.Image = image.NewRGBA(image.Rect(0, 0, 1, 1)); picture.Refresh(); viewport.Refresh() }
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
	note := widget.NewLabel(catalog.Text("profile.transport_note"))
	note.Wrapping = fyne.TextWrapWord
	controlHint := widget.NewLabel(catalog.Text("control.instructions"))
	controlHint.Wrapping = fyne.TextWrapWord
	chooser := &savedChooser{ctx: ctx, state: filepath.Join(root, "state"), catalog: catalog, window: w}
	chooseSend := func(item storage.SavedTransfer) { enqueueOpen(commands, status, catalog, item.Directory) }
	chooseReceive := func(item storage.SavedReceive) { selectReceive(item, session, destination, windows) }
	controls := container.NewVBox(note, profiles.object(), widget.NewLabel(catalog.Text("saved.label")), savedControl,
		controlHint,
		widget.NewButton(catalog.Text("action.control_show"), control.arm), controlledReceiveButton(catalog, w, send),
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
	control.install(viewport)
	w.SetContent(split)
	desiredSize := fyne.NewSize(1150, 800)
	w.Resize(initialWindowSize(desiredSize))
	var closing atomic.Bool
	control.requestClose = func() {
		if closing.Swap(true) {
			return
		}
		cancel()
		control.authorized.Store(false)
		go func() { <-done; chooser.workers.Wait(); fyne.Do(func() { w.SetCloseIntercept(nil); w.Close() }) }()
	}
	w.SetCloseIntercept(control.requestClose)
	// One bounded UI delivery at a time; persistence only writes a latest-value mailbox.
	go func() {
		ticker := time.NewTicker(dimensionsRefreshInterval)
		defer ticker.Stop()
		deliverSnapshots(ctx, events, ticker.C, func(event snapshot) {
			fyne.DoAndWait(func() {
				profiles.refreshCapacity(capacity)
				profiles.reconcile(event)
				w.SetTitle(windowTitle(catalog, event.role, os.Getpid()))
				event = control.guard(event)
				control.apply(event)
				status.SetText(catalog.Text(event.key, event.params))
				if event.picture != nil {
					control.paint(event, func() {
						picture.Image = event.picture
						picture.Refresh()
						viewport.Refresh()
					})
				}
				if event.sources != nil {
					sources = event.sources
					options := make([]string, len(sources))
					for i, s := range sources {
						options[i] = fmt.Sprintf("%d · %s", s.ID, s.Title)
					}
					windows.Options = options
					// Enumeration may retire native selection tokens. Require an
					// explicit choice from the refreshed list, never a stale label.
					windows.ClearSelected()
				}
				if event.session != "" {
					session.SetText(event.session)
				}
				if event.prepared != "" {
					saved.SetText(event.prepared)
				}
				control.acknowledge(event, picture.Image.Bounds(), picture.Visible())
			})
		}, func() { fyne.DoAndWait(func() { profiles.refreshCapacity(capacity); control.poll() }) })
	}()
	w.Show()
	if err := reconcileInitialWindowSize(w, desiredSize); err != nil {
		logger.Warn("initial_window_geometry_failed", "error", err)
	}
	a.Run()
	cancel()
}
func runEngine(ctx context.Context, root string, commands <-chan command, events chan snapshot, logger *slog.Logger, io engineIO) {
	role := "idle"
	profile := application.DefaultVisualProfile(application.Monochrome)
	var revision uint64
	emit := func(s snapshot) {
		s.revision = revision
		s.profile = profile.Colors
		s.visual = profile
		s.role = role // Carry the successful role even when earlier mailbox values were coalesced.
		emitObservedSnapshot(events, s, io.observer)
	}
	report := func(err error) {
		logger.Error("operation_failed", "error", err)
		var fit *fitError
		if errors.As(err, &fit) {
			emit(snapshot{key: "profile.fit", params: map[string]interface{}{"width": fit.required.X, "height": fit.required.Y, "availableWidth": fit.available.X, "availableHeight": fit.available.Y}})
			return
		}
		emit(snapshot{key: "status.error", params: map[string]interface{}{"error": err.Error()}})
	}
	var prepared *application.Prepared
	var receiver receiveOwner
	var stream captureStream
	var mode string
	var ordinal, frame, epoch uint64
	var playback *presentation.Playback
	var receive command
	var calibration calibrationGuard
	lastLost := io.viewport.load().Lost
	fitBlocked := false
	// Every terminal transition relinquishes both the receiver and its command.
	stop := func() error {
		mode = ""
		calibration = calibrationGuard{}
		receive = command{}
		var err error
		if stream != nil {
			err = stream.Close()
			stream = nil
		}
		if receiver != nil {
			err = errors.Join(err, receiver.Close())
			receiver = nil
		}
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
		observedLoss := io.viewport.load().Lost
		s, err := io.render(prepared, profile, ordinal, frame+1, epoch)
		if err != nil {
			return err
		}
		role = "sender"
		frame++
		fitBlocked = false
		lastLost = observedLoss // Never acknowledge a loss that happened during rendering.
		emit(s)
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-commands:
			if cmd.kind == "profile" {
				revision = max(revision, cmd.revision)
			}
			if cmd.kind == "pause" {
				if err := stop(); err != nil {
					report(err)
				} else {
					emit(snapshot{key: "status.paused"})
				}
				continue
			}
			if mode != "" && cmd.kind != "control_show" && cmd.kind != "control_receive" && (cmd.kind != "profile" || mode != "auto") {
				emit(snapshot{key: "status.busy"})
				continue
			}
			var err error
			switch cmd.kind {
			case "control_show":
				if err = stop(); err == nil {
					role = "sender"
					err = runControlledViewer(ctx, prepared, profile, ordinal, commands, emit, io)
				} else {
					emit(snapshot{releaseControl: true, key: "status.paused"})
				}
			case "control_receive":
				if err = stop(); err == nil {
					role = "receiver"
					err = runControlledReceive(ctx, root, cmd, commands, emit, io)
				}
			case "profile":
				requested := cmd.visual
				if requested == (application.VisualProfile{}) {
					requested = profile
					requested.Colors = cmd.profile
				}
				mode = "" // Even rejected profile requests pause playback.
				if requested.Capacity() == 0 {
					err = application.ErrProfile
					break
				}
				previousProfile, previousPlayback, previousFrame := profile, playback, frame
				if err = checkFit(requested, io.viewport); err != nil {
					break
				}
				profile = requested
				if prepared != nil {
					playback, err = presentation.NewVisualPlayback(prepared.ManifestPackets(), prepared.PacketCount(), profile)
					if err == nil {
						err = show()
					}
					if err != nil {
						// show emits only after rendering succeeds. Roll back before
						// acknowledging failure so the selector matches visible pixels.
						profile, playback, frame = previousProfile, previousPlayback, previousFrame
					}
				} else {
					emit(snapshot{key: "status.idle", picture: image.NewRGBA(image.Rect(0, 0, 1, 1))})
				}
			case "prepare", "open":
				if cmd.kind == "prepare" {
					prepared, err = application.Prepare(ctx, cmd.source, filepath.Join(root, "state"), 512, storage.Flush, root)
				} else {
					prepared, err = application.OpenPrepared(cmd.source)
				}
				if err == nil {
					var id [16]byte
					id, err = domain.NewID()
					if err != nil {
						break
					}
					epoch = binary.BigEndian.Uint64(id[:8])
					ordinal = 0
					frame = 0
					playback, err = presentation.NewVisualPlayback(prepared.ManifestPackets(), prepared.PacketCount(), profile)
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
					ordinal += profile.Capacity()
					if ordinal >= prepared.PacketCount() {
						ordinal = 0
					}
				} else if ordinal > 0 {
					ordinal -= min(ordinal, profile.Capacity())
				}
				err = show()
			case "auto":
				observedLoss := io.viewport.load().Lost
				if prepared == nil {
					err = errors.New("no prepared transfer")
				} else if err = checkFit(profile, io.viewport); err == nil {
					lastLost = observedLoss
					mode = "auto"
				}
			case "windows":
				var sources []platform.Source
				sources, err = platform.Sources()
				if err == nil {
					emit(snapshot{key: "status.idle", sources: append([]platform.Source{}, sources...)})
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
					stream, err = io.openCapture(cmd.window)
				}
				if err == nil {
					receive = cmd
					mode = "receive"
					role = "receiver"
					emit(snapshot{key: "status.waiting", picture: image.NewRGBA(image.Rect(0, 0, 1, 1))})
				}
			}
			if err != nil {
				fail(err)
			}
		case <-io.ticks:
			space := io.viewport.load()
			if role == "sender" && !fitBlocked && (checkFit(profile, io.viewport) != nil || space.Lost != lastLost) {
				mode = ""
				fitBlocked = true
				emit(snapshot{key: "status.paused", picture: image.NewRGBA(image.Rect(0, 0, 1, 1))})
				if err := checkFit(profile, io.viewport); err != nil {
					report(err)
				}
			}
			lastLost = space.Lost
			if mode == "auto" {
				ordinal = playback.Next()
				if err := show(); err != nil {
					fail(err)
				}
			}
			if mode != "receive" {
				continue
			}
			capture, err := stream.Next(ctx)
			if ctx.Err() != nil {
				return // Normal GUI shutdown; deferred stop still checks cleanup.
			}
			if errors.Is(err, platform.ErrFramePending) {
				continue
			}
			if err != nil {
				fail(err)
				continue
			}
			beacon, packets, err := io.decode(capture.Image)
			incompatible := calibration.observe(err)
			if err != nil {
				if incompatible {
					fail(err)
				}
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
