// Package i18n loads UI text from locale files; templates never contain UI text directly.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

type Bundle struct {
	langs map[string]map[string]string
	def   string
}

func Load(fsys fs.FS, dir, def string) (*Bundle, error) {
	b := &Bundle{langs: map[string]map[string]string{}, def: def}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		b.langs[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	if _, ok := b.langs[def]; !ok {
		return nil, fmt.Errorf("default language %q missing", def)
	}
	return b, nil
}

func (b *Bundle) Has(lang string) bool { _, ok := b.langs[lang]; return ok }

func (b *Bundle) Langs() []string {
	var out []string
	for k := range b.langs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// T returns the text for key; a missing key shows as [key] so it is noticed.
func (b *Bundle) T(lang, key string, args ...any) string {
	m, ok := b.langs[lang]
	if !ok {
		m = b.langs[b.def]
	}
	s, ok := m[key]
	if !ok {
		s, ok = b.langs[b.def][key]
		if !ok {
			return "[" + key + "]"
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Missing lists keys present in one language but not in another (parity check).
func (b *Bundle) Missing() []string {
	all := map[string]bool{}
	for _, m := range b.langs {
		for k := range m {
			all[k] = true
		}
	}
	var out []string
	for lang, m := range b.langs {
		for k := range all {
			if _, ok := m[k]; !ok {
				out = append(out, lang+":"+k)
			}
		}
	}
	sort.Strings(out)
	return out
}
