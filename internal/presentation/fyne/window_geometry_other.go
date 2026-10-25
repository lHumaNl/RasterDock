//go:build !windows

package fyneui

import "fyne.io/fyne/v2"

func initialWindowSize(desired fyne.Size) fyne.Size { return desired }

func reconcileInitialWindowSize(fyne.Window, fyne.Size) error { return nil }
