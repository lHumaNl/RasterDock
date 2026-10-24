package fyneui

import (
	"context"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/storage"
	"rasterdock/locales"
)

func TestPickerRefreshInvalidatesSelectionAndApplyResetsIt(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	catalog, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	c := &savedChooser{catalog: catalog, ctx: context.Background()}
	c.busy.Store(true)
	chosen := ""
	p := newTestPicker(c, func(item storage.SavedTransfer) { chosen = item.Transfer })
	p.apply(storage.Discovery{Senders: []storage.SavedTransfer{{Transfer: "old"}}}, nil)
	p.list.OnSelected(0)
	p.refresh()
	p.list.OnSelected(0)
	p.selectRow()
	if chosen != "" {
		t.Fatal("stale row was confirmed during refresh")
	}
	p.apply(storage.Discovery{Senders: []storage.SavedTransfer{{Transfer: "inserted"}, {Transfer: "old"}}}, nil)
	if p.selected != -1 || !p.choose.Disabled() {
		t.Fatal("apply retained previous row selection")
	}
}

func TestPickerCompletionUpdatesReopenedCurrentView(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	catalog, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	c := &savedChooser{catalog: catalog}
	old := newTestPicker(c, func(storage.SavedTransfer) {})
	old.closed = true
	current := newTestPicker(c, func(storage.SavedTransfer) {})
	current.loading = true
	c.deliverDiscovery(storage.Discovery{Senders: []storage.SavedTransfer{{Transfer: "ready"}}}, nil)
	if current.loading || len(current.rows) != 1 || old.rows != nil {
		t.Fatal("completion was not delivered to reopened picker")
	}
}

func newTestPicker(c *savedChooser, send func(storage.SavedTransfer)) *pickerView {
	p := &pickerView{chooser: c, send: send, selected: -1}
	p.status = widget.NewLabel("")
	p.choose = widget.NewButton("Select", p.selectRow)
	p.choose.Disable()
	p.list = widget.NewList(func() int { return len(p.rows) }, newPickerRow, p.updateRow)
	p.list.OnSelected = p.selectIndex
	c.current = p
	return p
}
