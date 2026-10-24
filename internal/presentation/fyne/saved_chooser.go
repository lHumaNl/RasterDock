package fyneui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/storage"
	"rasterdock/locales"
)

const discoveryTimeout = 5 * time.Second
const pickerWindowFraction = 0.8

type savedChooser struct {
	ctx     context.Context
	state   string
	catalog *locales.Catalog
	window  fyne.Window
	busy    atomic.Bool
	workers sync.WaitGroup
	current *pickerView
}

type pickerRow struct {
	title, subtitle, detail string
	selectItem              func()
}
type pickerView struct {
	chooser  *savedChooser
	receive  bool
	send     func(storage.SavedTransfer)
	resume   func(storage.SavedReceive)
	rows     []pickerRow
	list     *widget.List
	status   *widget.Label
	choose   *widget.Button
	dialog   dialog.Dialog
	selected int
	closed   bool
	loading  bool
}

func enqueueOpen(commands chan<- command, status *widget.Label, c *locales.Catalog, path string) {
	select {
	case commands <- command{kind: "open", source: path}:
	default:
		status.SetText(c.Text("status.busy"))
	}
}

// Selection only edits the form; it cannot acquire an owner or start capture.
func selectReceive(item storage.SavedReceive, session, destination *widget.Entry, windows *widget.Select) {
	session.SetText(item.Directory)
	destination.SetText(item.Destination)
	windows.ClearSelected()
}

func (c *savedChooser) show(receive bool, send func(storage.SavedTransfer), resume func(storage.SavedReceive)) {
	p := &pickerView{chooser: c, receive: receive, send: send, resume: resume, selected: -1}
	c.current = p
	p.status = widget.NewLabel(c.catalog.Text("saved.loading"))
	p.status.Wrapping = fyne.TextWrapWord
	p.choose = widget.NewButton(c.catalog.Text("action.select"), p.selectRow)
	p.choose.Disable()
	p.list = widget.NewList(func() int { return len(p.rows) }, newPickerRow, p.updateRow)
	p.list.OnSelected = p.selectIndex
	refresh := widget.NewButton(c.catalog.Text("action.refresh"), p.refresh)
	content := container.NewBorder(nil, container.NewVBox(p.status, refresh, p.choose), nil, nil, p.list)
	p.dialog = dialog.NewCustom(c.catalog.Text("saved.title"), c.catalog.Text("action.close"), content, c.window)
	p.dialog.SetOnClosed(func() { p.closed = true })
	size := c.window.Canvas().Size()
	p.dialog.Resize(fyne.NewSize(size.Width*pickerWindowFraction, size.Height*pickerWindowFraction))
	p.dialog.Show()
	p.refresh()
}

func newPickerRow() fyne.CanvasObject {
	title, subtitle, detail := widget.NewLabel(""), widget.NewLabel(""), widget.NewLabel("")
	title.Truncation, subtitle.Truncation, detail.Truncation = fyne.TextTruncateEllipsis, fyne.TextTruncateEllipsis, fyne.TextTruncateEllipsis
	return container.NewVBox(title, subtitle, detail)
}

func (p *pickerView) updateRow(id widget.ListItemID, object fyne.CanvasObject) {
	row := object.(*fyne.Container)
	row.Objects[0].(*widget.Label).SetText(p.rows[id].title)
	row.Objects[1].(*widget.Label).SetText(p.rows[id].subtitle)
	row.Objects[2].(*widget.Label).SetText(p.rows[id].detail)
}

func (p *pickerView) selectRow() {
	if p.closed || p.loading || p.selected < 0 || p.selected >= len(p.rows) {
		return
	}
	p.rows[p.selected].selectItem()
	p.dialog.Hide()
}

func (p *pickerView) selectIndex(id widget.ListItemID) {
	if p.loading || p.closed || id < 0 || id >= len(p.rows) {
		return
	}
	p.selected = id
	p.choose.Enable()
}

func (p *pickerView) refresh() {
	c := p.chooser
	if c.ctx.Err() != nil {
		return
	}
	if !c.busy.CompareAndSwap(false, true) {
		p.invalidateRows()
		p.status.SetText(c.catalog.Text("saved.loading"))
		return
	}
	p.invalidateRows()
	p.loading = true
	p.status.SetText(c.catalog.Text("saved.loading"))
	c.workers.Add(1)
	go func() { defer c.workers.Done(); p.discover() }()
}

func (p *pickerView) discover() {
	c := p.chooser
	ctx, cancel := context.WithTimeout(c.ctx, discoveryTimeout)
	defer cancel()
	result, err := storage.Discover(ctx, c.state)
	if c.ctx.Err() != nil {
		c.busy.Store(false)
		return
	}
	fyne.DoAndWait(func() {
		c.deliverDiscovery(result, err)
	})
}

func (c *savedChooser) deliverDiscovery(result storage.Discovery, err error) {
	c.busy.Store(false)
	current := c.current
	if current != nil && !current.closed {
		current.apply(result, err)
	}
}

func (p *pickerView) apply(result storage.Discovery, err error) {
	p.rows = p.makeRows(result)
	p.loading = false
	p.selected = -1
	p.choose.Disable()
	p.list.UnselectAll()
	p.list.Refresh()
	text := p.chooser.catalog.Text("saved.empty")
	if len(p.rows) > 0 {
		key := "saved.hint"
		if !p.receive {
			key = "saved.sender_hint"
		}
		text = p.chooser.catalog.Text(key)
	}
	if err != nil {
		result.Issues = append(result.Issues, err.Error())
	}
	if len(result.Issues) > 0 {
		text = p.chooser.catalog.Text("saved.issues", map[string]interface{}{"count": len(result.Issues)})
		p.showIssues(result.Issues)
	}
	p.status.SetText(text)
}

func (p *pickerView) invalidateRows() {
	p.loading = true
	p.rows = nil
	p.selected = -1
	p.choose.Disable()
	p.list.UnselectAll()
	p.list.Refresh()
}

func (p *pickerView) showIssues(issues []string) {
	c := p.chooser
	content := container.NewScroll(widget.NewLabel(strings.Join(issues, "\n")))
	d := dialog.NewCustom(c.catalog.Text("saved.issues_title"), c.catalog.Text("action.close"), content, c.window)
	size := c.window.Canvas().Size()
	d.Resize(fyne.NewSize(size.Width*pickerWindowFraction, size.Height*pickerWindowFraction))
	d.Show()
}

func (p *pickerView) makeRows(result storage.Discovery) []pickerRow {
	var rows []pickerRow
	if p.receive {
		for _, item := range result.Receivers {
			rows = append(rows, pickerRow{item.Transfer, item.Session, item.Destination, func() { p.resume(item) }})
		}
	} else {
		for _, item := range result.Senders {
			rows = append(rows, pickerRow{item.Transfer, "", item.Directory, func() { p.send(item) }})
		}
	}
	return rows
}
