package subs

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	dateRe    = regexp.MustCompile(`(20\d{2})[-_.](\d{2})[-_.](\d{2})`)
	normKeyRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// subDate pulls a YYYY-MM-DD date out of a filename stem, if present.
func subDate(stem string) string {
	m := dateRe.FindStringSubmatch(stem)
	if m == nil {
		return ""
	}
	return m[1] + "-" + m[2] + "-" + m[3]
}

// normKey reduces a stem to letters and digits for equality comparison, so
// "Mommy's Taboo Secret" and "Mommys Taboo Secret [1080p]" collapse together.
func normKey(s string) string {
	return normKeyRe.ReplaceAllString(strings.ToLower(StripNoise(s)), "")
}

func pct(f float64) string { return fmt.Sprintf("%d%%", int(f*100+0.5)) }

// signedSeconds renders a duration delta compactly: "+7s", "-14s", "+23m".
func signedSeconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s > -120 && s < 120 {
		return fmt.Sprintf("%+ds", s)
	}
	return fmt.Sprintf("%+dm", s/60)
}

// HMS renders a duration as H:MM:SS for reports.
func HMS(d time.Duration) string {
	if d <= 0 {
		return "?"
	}
	t := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d:%02d", t/3600, (t%3600)/60, t%60)
}
