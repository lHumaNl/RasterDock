//go:build ci

package fyneui

import (
	"bytes"
	"log"
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
)

func TestTransientPreferencesNeverEnablePersistence(t *testing.T) {
	previous := fyne.CurrentApp()
	defer fyne.SetCurrentApp(previous)
	var output bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(oldOutput)
	a := newTransientApp()
	defer a.Quit()
	_ = a.UniqueID()
	a.Preferences().SetBool("file-dialog-show-hidden", true)
	if !a.Preferences().Bool("file-dialog-show-hidden") {
		t.Fatal("preferences do not work")
	}
	// Pin the audited Fyne 2.8.2 implementation contract, not an invented provider.
	prefs := reflect.ValueOf(a.(*transientApp).App).Elem().FieldByName("prefs").Elem().Elem()
	if prefs.FieldByName("needsSaveBeforeExit").Bool() {
		t.Fatal("Fyne persistence enabled")
	}
	if bytes.Contains(output.Bytes(), []byte("Preferences API requires")) {
		t.Fatal(output.String())
	}
}
