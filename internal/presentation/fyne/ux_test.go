package fyneui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/storage"
	"rasterdock/locales"
)

func TestWindowTitleIdentifiesRoleAndProcess(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	a, b := windowTitle(c, "sender", 11), windowTitle(c, "sender", 12)
	if a == b || !strings.Contains(a, "Sender") {
		t.Fatalf("indistinct titles: %q / %q", a, b)
	}
}

func TestSavedPathKeepsOneLineAndCopiesFullValue(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	label, control := savedPath(c, a.Clipboard())
	full := "/" + strings.Repeat("long-directory/", 40)
	label.SetText(full)
	next := widget.NewLabel("Source")
	box := container.NewVBox(control, next)
	box.Resize(fyne.NewSize(300, 100))
	if label.Wrapping != fyne.TextWrapOff || label.Truncation != fyne.TextTruncateEllipsis {
		t.Fatal("path is not compact")
	}
	if next.Position().Y < control.Position().Y+control.Size().Height {
		t.Fatal("path overlaps source label")
	}
	for _, object := range control.(*fyne.Container).Objects {
		if button, ok := object.(*widget.Button); ok {
			test.Tap(button)
		}
	}
	if a.Clipboard().Content() != full {
		t.Fatal("clipboard path truncated")
	}
}

func TestReceiveSelectionOnlyFillsFormAndClearsCapture(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	c, err := locales.Load("ru")
	if err != nil {
		t.Fatal(err)
	}
	session, destination := widget.NewEntry(), widget.NewEntry()
	windows := localizedSelect(c)
	windows.Options = []string{"old window"}
	windows.SetSelected("old window")
	selectReceive(storage.SavedReceive{Directory: "/session", Destination: "/destination"}, session, destination, windows)
	if session.Text != "/session" || destination.Text != "/destination" || windows.Selected != "" {
		t.Fatal("incorrect resume form")
	}
	if windows.PlaceHolder != c.Text("select.placeholder") {
		t.Fatal("toolkit placeholder used")
	}
}

func TestSavedOpenUsesNormalCommandAndReportsFullQueue(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan command, 1)
	status := widget.NewLabel("")
	enqueueOpen(commands, status, c, "/prepared")
	if got := <-commands; got.kind != "open" || got.source != "/prepared" {
		t.Fatalf("command: %+v", got)
	}
	commands <- command{}
	enqueueOpen(commands, status, c, "/prepared")
	if status.Text != c.Text("status.busy") {
		t.Fatal("queue rejection hidden")
	}
}

func TestRejectedCommandPreservesRole(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	if got := h.expect("status.frame").role; got != "sender" {
		t.Fatalf("role = %q", got)
	}
	h.start(receiveCommand(t))
	h.send(command{kind: "open", source: h.prepared.Directory})
	if got := h.expect("status.busy").role; got != "receiver" {
		t.Fatalf("busy role = %q", got)
	}
}
