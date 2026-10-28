//go:build integration && darwin

package fyneui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/codec"
	"rasterdock/locales"
)

// Test-binary-only protocol. No engine commands, capture substitutions, or packet
// data cross this boundary. All actions use production widgets on the UI thread.
type geometryAction struct{ Control, Value string }
type geometryRequest struct {
	TimerCycles, IdleSeconds *string
	InspectRestored          bool
	InspectSender            bool
	Source, Destination      *string
	Tap, SelectTitle         string
	Actions                  []geometryAction
	Viewport                 image.Point // Desired physical area; resize only this helper's window.
}
type geometryReply struct {
	Restored, Unfocused                                 bool
	RenderedProfile                                     [4]int // colors, module, columns, rows; never payload/descriptor data
	Title, Status, Session, Selected, Dimensions, Error string
	Colors, Module, Grid                                string
	Options                                             []string
	Picture                                             image.Point
	Visible                                             bool
}

func init() { nativeGUIHelper = geometryGUIHelper }

func geometryGUIHelper() error {
	if os.Getenv("RASTERDOCK_RECOVERY_TIMESTAMP_HELPER") == "1" {
		return recoveryTimestampGUIHelper()
	}
	root := os.Getenv("RASTERDOCK_GUI_ROOT")
	if root == "" {
		return fmt.Errorf("missing isolated GUI root")
	}
	catalog, err := locales.Load("en")
	if err != nil {
		return err
	}
	go func() {
		for fyne.CurrentApp() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		var w fyne.Window
		for w == nil {
			fyne.DoAndWait(func() {
				for _, candidate := range fyne.CurrentApp().Driver().AllWindows() {
					if candidate.Content() != nil {
						w = candidate
						break
					}
				}
			})
			if w == nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
		defer fyne.Do(w.Close)
		scanner, encoder := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
		for scanner.Scan() {
			var request geometryRequest
			var reply geometryReply
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				reply.Error = err.Error()
			} else {
				fyne.DoAndWait(func() { reply = geometryDrive(w, request) })
			}
			if encoder.Encode(reply) != nil {
				return
			}
		}
	}()
	Run(root, catalog, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return nil
}

func geometryDrive(w fyne.Window, request geometryRequest) (r geometryReply) {
	split, ok := w.Content().(*container.Split)
	if request.InspectRestored {
		return geometryReply{Restored: ok, Unfocused: w.Canvas().Focused() == nil}
	}
	if !ok {
		r.Error = "production content is not Split"
		return
	}
	scroll, ok := split.Leading.(*container.Scroll)
	if !ok {
		r.Error = "production controls are not Scroll"
		return
	}
	box, ok := scroll.Content.(*fyne.Container)
	if !ok {
		r.Error = "unexpected production form structure"
		return
	}
	fields, err := geometryLookupForm(box)
	if err != nil {
		r.Error = err.Error()
		return
	}
	if request.TimerCycles != nil || request.IdleSeconds != nil {
		if err := geometrySetSenderOptions(box, request.TimerCycles, request.IdleSeconds); err != nil {
			r.Error = err.Error()
			return
		}
	}
	profile, ok := box.Objects[1].(*fyne.Container)
	if !ok || len(profile.Objects) != 6 {
		r.Error = "unexpected profile structure"
		return
	}
	colors, cOK := profile.Objects[0].(*widget.Select)
	moduleLabel, mlOK := profile.Objects[1].(*widget.Label)
	module, mOK := profile.Objects[2].(*widget.Select)
	gridLabel, glOK := profile.Objects[3].(*widget.Label)
	grid, gOK := profile.Objects[4].(*widget.Select)
	dimensions, dOK := profile.Objects[5].(*widget.Label)
	if !cOK || !mlOK || !mOK || !glOK || !gOK || !dOK {
		r.Error = "invalid profile widgets"
		return
	}
	if moduleLabel.Text != "Module size (physical pixels)" || gridLabel.Text != "QR grid (all cells active)" ||
		!slices.Equal(module.Options, []string{"4", "6", "8"}) || !slices.Equal(grid.Options, []string{"1 × 1", "2 × 2", "3 × 2"}) ||
		!slices.Equal(colors.Options, []string{"2 colors — monochrome (default)", "4 colors — experimental", "8 colors — experimental"}) {
		r.Error = "profile labels/options changed"
		return
	}
	source, destination, session := fields.source, fields.destination, fields.session
	windows, status := fields.windows, fields.status
	viewport, vOK := split.Trailing.(*fyne.Container)
	if !vOK || len(viewport.Objects) != 1 {
		r.Error = "production entry/capture/viewport structure changed"
		return
	}
	picture, ok := viewport.Objects[0].(*canvas.Image)
	if !ok || picture.Image == nil {
		r.Error = "missing production picture"
		return
	}
	if request.Viewport.X > 0 && request.Viewport.Y > 0 {
		split.SetOffset(0.4)
		x0, y0 := w.Canvas().PixelCoordinateForPosition(fyne.Position{})
		x1, y1 := w.Canvas().PixelCoordinateForPosition(fyne.NewPos(65536, 65536))
		if x1 <= x0 || y1 <= y0 {
			r.Error = "invalid physical mapping"
			return
		}
		sx, sy := float32(x1-x0)/65536, float32(y1-y0)/65536
		// Compensate for the current split divider and canvas padding.
		current := w.Canvas().Size()
		w.Resize(fyne.NewSize(current.Width+(float32(request.Viewport.X)/sx-viewport.Size().Width)/0.6,
			current.Height+float32(request.Viewport.Y)/sy-viewport.Size().Height))
	}
	if request.Source != nil {
		source.SetText(*request.Source)
	}
	if request.Destination != nil {
		destination.SetText(*request.Destination)
	}
	for _, action := range request.Actions {
		selector := map[string]*widget.Select{"colors": colors, "module": module, "grid": grid}[action.Control]
		if selector == nil || !slices.Contains(selector.Options, action.Value) {
			r.Error = "invalid profile action"
			return
		}
		selector.SetSelected(action.Value)
	}
	if request.SelectTitle != "" {
		matches, selected := 0, ""
		for _, option := range windows.Options {
			if geometryOwnedOption(option, request.SelectTitle) {
				matches++
				selected = option
			}
		}
		if matches != 1 {
			r.Error = fmt.Sprintf("exact owned sender title matches: %d", matches)
			return
		}
		windows.SetSelected(selected)
	}
	if request.Tap != "" {
		var matches []*widget.Button
		var walk func(fyne.CanvasObject)
		walk = func(o fyne.CanvasObject) {
			switch v := o.(type) {
			case *fyne.Container:
				for _, child := range v.Objects {
					walk(child)
				}
			case *widget.Button:
				if v.Text == request.Tap {
					matches = append(matches, v)
				}
			}
		}
		walk(box)
		if len(matches) != 1 || matches[0].Disabled() || matches[0].OnTapped == nil {
			r.Error = "missing/ambiguous/disabled button: " + request.Tap
			return
		}
		matches[0].OnTapped()
	}
	r.Title, r.Status, r.Session, r.Selected, r.Dimensions = w.Title(), status.Text, session.Text, windows.Selected, dimensions.Text
	r.Colors, r.Module, r.Grid = colors.Selected, module.Selected, grid.Selected
	r.Options = append([]string(nil), windows.Options...)
	r.Picture, r.Visible = picture.Image.Bounds().Size(), picture.Visible()
	if request.InspectSender {
		if !strings.Contains(w.Title(), " · Sender · PID ") || !picture.Visible() {
			r.Error = "profile inspection requires a visible sender picture"
			return
		}
		// Sender-only read-only assertion. Discard packets and descriptor locally;
		// neither can enter the receiver process through this protocol.
		beacon, _, err := codec.Decode(picture.Image)
		if err != nil {
			r.Error = "sender profile inspection: " + err.Error()
			return
		}
		r.RenderedProfile = [4]int{int(beacon.Colors), int(beacon.Module), int(beacon.Columns), int(beacon.Rows)}
	}
	return
}

func geometrySetSenderOptions(box *fyne.Container, cycles, idle *string) error {
	values := map[string]*string{"Timer cycles before closing (1–1000000)": cycles,
		"Controlled sender idle timeout, seconds (0 = off, max 86400)": idle}
	for label, value := range values {
		if value == nil {
			continue
		}
		var found *widget.Entry
		for _, object := range box.Objects {
			group, ok := object.(*fyne.Container)
			if !ok {
				continue
			}
			for i, item := range group.Objects {
				text, ok := item.(*widget.Label)
				if !ok || text.Text != label || i+1 >= len(group.Objects) {
					continue
				}
				entry, ok := group.Objects[i+1].(*widget.Entry)
				if !ok || found != nil {
					return fmt.Errorf("ambiguous sender option")
				}
				found = entry
			}
		}
		if found == nil {
			return fmt.Errorf("missing sender option")
		}
		found.SetText(*value)
	}
	return nil
}
