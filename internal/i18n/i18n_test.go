package i18n

import "testing"

func TestSetLanguage(t *testing.T) {
	defer Reset()
	for name, want := range map[string]Language{"de": German, "DE": German, "de_DE.UTF-8": German, "Deutsch": German, "german": German,
		"en": English, "EN": English, "en_US.UTF-8": English, "en-GB": English, " English ": English} {
		if err := SetLanguage(name); err != nil || current != want {
			t.Errorf("%q: err=%v lang=%v, want %v", name, err, current, want)
		}
	}
	current = English
	if err := SetLanguage(""); err != nil || current != English {
		t.Error("an empty name keeps the language")
	}
	if err := SetLanguage("fr"); err == nil || current != English {
		t.Errorf("an unknown language must be an error and change nothing: %v", err)
	}
}

// germanWords must not show up on an English screen.
