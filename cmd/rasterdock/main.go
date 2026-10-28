// Command rasterdock starts the desktop application without public CLI options.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jeandeaual/go-locale"

	"rasterdock/internal/adapters/config"
	"rasterdock/internal/buildinfo"
	fyneui "rasterdock/internal/presentation/fyne"
	"rasterdock/locales"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	return runWithGUI(fyneui.Run)
}

func runWithGUI(launch func(string, *locales.Catalog, *slog.Logger)) error {
	root, err := config.Root()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "logs"), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, "logs", fmt.Sprintf("rasterdock-%d-%d.jsonl", time.Now().UnixNano(), os.Getpid())), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Config root from UserHomeDir, generated run-specific filename.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }() // Diagnostic output is not transfer state.
	logger := slog.New(slog.NewJSONHandler(file, nil))
	logger.Info("startup", "version", buildinfo.Version, "commit", buildinfo.Commit, "build_time", buildinfo.BuildTime, "os", runtime.GOOS, "arch", runtime.GOARCH)
	settings, err := config.LoadSettings(filepath.Join(root, "settings.yaml"))
	if err != nil {
		// Preferences errors must not prevent the GUI from showing its persistent
		// inline warning. No defaults are written over the unreadable/invalid file.
		logger.Warn("settings_load_failed", "error", err)
		settings = config.DefaultSettings()
	}
	language := settings.Language
	if language == "system" {
		language, err = locale.GetLocale()
		if err != nil {
			language = "en"
			logger.Warn("system_locale_unavailable", "error", err)
		}
	}
	catalog, err := locales.Load(language)
	if err != nil {
		return err
	}
	launch(root, catalog, logger)
	return nil
}
