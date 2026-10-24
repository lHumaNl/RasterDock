// Package locales embeds flat en/ru catalogs with English fallback.
package locales

import (
	"embed"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
)

//go:embed en.yaml ru.yaml
var catalogs embed.FS

// Catalog localizes messages without changing logical keys.
type Catalog struct{ localizer *i18n.Localizer }

// Load resolves a system or explicit language tag and validates embedded catalogs.
func Load(locale string) (*Catalog, error) {
	bundle := i18n.NewBundle(language.English)
	for _, lang := range []string{"en", "ru"} {
		b, err := catalogs.ReadFile(lang + ".yaml")
		if err != nil {
			return nil, err
		}
		var values map[string]string
		if err = yaml.Unmarshal(b, &values); err != nil {
			return nil, err
		}
		for key, value := range values {
			if value == "" {
				continue
			}
			if err = bundle.AddMessages(language.Make(lang), &i18n.Message{ID: key, Other: value}); err != nil {
				return nil, err
			}
		}
	}
	locale = strings.ReplaceAll(strings.Split(locale, ".")[0], "_", "-")
	return &Catalog{localizer: i18n.NewLocalizer(bundle, locale, "en")}, nil
}

// Text formats named parameters and falls back to the complete English catalog.
func (c *Catalog) Text(key string, params ...map[string]interface{}) string {
	var data interface{}
	if len(params) > 0 {
		data = params[0]
	}
	s, err := c.localizer.Localize(&i18n.LocalizeConfig{MessageID: key, TemplateData: data})
	if err != nil && s == "" {
		return "[" + key + "]"
	}
	return s
}
