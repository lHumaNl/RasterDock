package fyneui

import (
	"fmt"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// geometryFormFields is shared by the native driver and headless regressions.
// It only identifies public widgets; it never invokes application operations.
type geometryFormFields struct {
	source, destination, session *widget.Entry
	windows                      *widget.Select
	status                       *widget.Label
}

func geometryLookupForm(box *fyne.Container) (geometryFormFields, error) {
	var f geometryFormFields
	if box == nil || len(box.Objects) < 2 {
		return f, fmt.Errorf("unexpected production form structure")
	}
	// Search only direct siblings: nested labels cannot impersonate a field.
	adjacent := func(text string) fyne.CanvasObject {
		matches, index := 0, -1
		for i, object := range box.Objects {
			if label, ok := object.(*widget.Label); ok && label.Text == text {
				matches++
				index = i + 1
			}
		}
		if matches != 1 || index >= len(box.Objects) {
			return nil
		}
		return box.Objects[index]
	}
	f.source, _ = adjacent("Source file or directory").(*widget.Entry)
	f.destination, _ = adjacent("Destination directory").(*widget.Entry)
	f.session, _ = adjacent("Receive session directory (empty = new session)").(*widget.Entry)
	f.windows, _ = adjacent("Capture window (OS permission may be required)").(*widget.Select)
	// Status text changes at runtime. Its public layout contract is the final
	// label, immediately after the navigation container, not a fixed index.
	f.status, _ = box.Objects[len(box.Objects)-1].(*widget.Label)
	_, navigationOK := box.Objects[len(box.Objects)-2].(*fyne.Container)
	if f.source == nil || f.destination == nil || f.session == nil || f.windows == nil || f.status == nil || !navigationOK {
		return geometryFormFields{}, fmt.Errorf("missing/ambiguous/invalid production entry/capture/status widgets")
	}
	return f, nil
}

func TestGeometryFormLookupRejectsMalformed(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	for _, label := range []string{"Source file or directory", "Destination directory", "Capture window (OS permission may be required)", "Receive session directory (empty = new session)"} {
		for _, mutation := range []string{"missing", "duplicate", "wrong type", "not adjacent", "nested only", "last label"} {
			t.Run(label+"/"+mutation, func(t *testing.T) {
				box, _ := geometryFormFixture(2)
				for i, object := range box.Objects {
					l, ok := object.(*widget.Label)
					if !ok || l.Text != label {
						continue
					}
					switch mutation {
					case "missing":
						l.Text += " changed"
					case "duplicate":
						box.Objects = append(box.Objects[:i], append([]fyne.CanvasObject{widget.NewLabel(label)}, box.Objects[i:]...)...)
					case "wrong type":
						box.Objects[i+1] = widget.NewLabel("not a field")
					case "not adjacent":
						box.Objects = append(box.Objects[:i+1], append([]fyne.CanvasObject{widget.NewButton("inserted", nil)}, box.Objects[i+1:]...)...)
					case "nested only":
						box.Objects[i] = &fyne.Container{Objects: []fyne.CanvasObject{l}}
					case "last label":
						box.Objects = append(box.Objects[:i], box.Objects[i+1:]...)
						box.Objects = append(box.Objects, l)
					}
					break
				}
				if got, err := geometryLookupForm(box); err == nil || got != (geometryFormFields{}) {
					t.Fatalf("malformed form accepted: %+v, %v", got, err)
				}
			})
		}
	}
	for _, box := range []*fyne.Container{nil, {}, {Objects: []fyne.CanvasObject{widget.NewLabel("only")}}} {
		if _, err := geometryLookupForm(box); err == nil {
			t.Fatal("short form accepted")
		}
	}
	box, _ := geometryFormFixture(2)
	box.Objects[len(box.Objects)-1] = widget.NewEntry()
	if _, err := geometryLookupForm(box); err == nil {
		t.Fatal("non-label status accepted")
	}
}

func TestGeometryFormLookupLayouts(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	for _, inserted := range []int{0, 2, 5} {
		t.Run(fmt.Sprintf("inserted_%d", inserted), func(t *testing.T) {
			box, want := geometryFormFixture(inserted)
			got, err := geometryLookupForm(box)
			if err != nil || got != want {
				t.Fatalf("lookup = %+v, %v; want exact widget identities %+v", got, err, want)
			}
		})
	}
}

// Representative old 20-object form and insertion variants. Expected results
// are widget identities, not an independently duplicated index-based lookup.
func geometryFormFixture(inserted int) (*fyne.Container, geometryFormFields) {
	f := geometryFormFields{widget.NewEntry(), widget.NewEntry(), widget.NewEntry(), widget.NewSelect(nil, nil), widget.NewLabel("Idle")}
	objects := []fyne.CanvasObject{widget.NewLabel("note"), &fyne.Container{}, widget.NewLabel("saved"), &fyne.Container{}}
	for i := 0; i < inserted; i++ {
		objects = append(objects, widget.NewButton(fmt.Sprintf("extra %d", i), nil))
	}
	objects = append(objects, widget.NewButton("saved", nil), widget.NewButton("sessions", nil),
		widget.NewLabel("Source file or directory"), f.source, &fyne.Container{},
		widget.NewLabel("Destination directory"), f.destination, widget.NewButton("folder", nil),
		widget.NewLabel("Capture window (OS permission may be required)"), f.windows, widget.NewButton("refresh", nil),
		widget.NewLabel("Receive session directory (empty = new session)"), f.session,
		&fyne.Container{}, &fyne.Container{}, f.status)
	return &fyne.Container{Objects: objects}, f
}
