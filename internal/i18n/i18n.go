package i18n

import (
	"fmt"
	"strings"
	"time"
)

type Language int

const (
	German Language = iota
	English
)

// current is the language of the interface, the log and the messages. It is set once at the start.
var current = German

// SetLanguage selects the language from a name like "de", "en", "de_DE.UTF-8" or "english". An empty name keeps
// the current one.
func SetLanguage(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	if i := strings.IndexAny(name, "_-.@"); i > 0 {
		name = name[:i]
	}
	switch name {
	case "de", "deutsch", "german":
		current = German
	case "en", "english", "englisch":
		current = English
	default:
		return fmt.Errorf("unbekannte Sprache %q, erlaubt sind de und en / unknown language %q, use de or en", name, name)
	}
	return nil
}

// T returns the German or the English text, depending on the language.
func T(de, en string) string {
	if current == English {
		return en
	}
	return de
}

// Date writes a day the way the language does: 29.10.2026 or 29 Oct 2026.
func Date(d time.Time) string {
	return d.Format(T("02.01.2006", "2 Jan 2006"))
}

// Stamp writes a day and a time: 29.10. 01:04 or 29 Oct 01:04.
func Stamp(d time.Time) string {
	return d.Format(T("02.01. 15:04", "2 Jan 15:04"))
}

// Duration writes a length of time the way the language does: 3 Std. 5 Min. or 3 h 5 min.
func Duration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d >= time.Hour {
		return fmt.Sprintf(T("%d Std. %d Min.", "%d h %d min"), int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf(T("%d Min.", "%d min"), max(1, int(d.Minutes())))
}

// Reset puts the language back to German. Tests use it to leave no trace.
func Reset() { current = German }
