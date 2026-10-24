package locales

import (
	"strings"
	"testing"
)

func TestLocaleFallback(t *testing.T) {
	en, err := Load("xx")
	if err != nil {
		t.Fatal(err)
	}
	ru, err := Load("ru-RU")
	if err != nil {
		t.Fatal(err)
	}
	if en.Text("action.prepare") != "Prepare" || ru.Text("action.prepare") != "Подготовить" {
		t.Fatal("language resolution")
	}
	if ru.Text("app.title") != "RasterDock" {
		t.Fatal("English fallback")
	}
}

func TestSavedStateMessagesHaveBothTranslations(t *testing.T) {
	en, err := Load("en")
	if err != nil {
		t.Fatal(err)
	}
	ru, err := Load("ru")
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"role.idle", "role.sender", "role.receiver", "select.placeholder", "saved.label", "action.copy_path", "action.saved", "action.sessions", "action.refresh", "action.select", "action.close", "saved.title", "saved.loading", "saved.empty", "saved.hint", "saved.issues", "saved.issues_title"}
	for _, key := range keys {
		a, b := en.Text(key, map[string]interface{}{"count": 2}), ru.Text(key, map[string]interface{}{"count": 2})
		if a == b || strings.HasPrefix(a, "[") || strings.HasPrefix(b, "[") {
			t.Errorf("missing translation: %s", key)
		}
	}
}
