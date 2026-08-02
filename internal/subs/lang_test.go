package subs

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"english", `1
00:00:01,000 --> 00:00:03,000
Oh hey, brother. Thank you for letting me stay with you.

2
00:00:04,000 --> 00:00:06,000
I know it's not what you wanted, but I don't have anywhere else to go.`, "en"},

		{"spanish", `1
00:00:01,000 --> 00:00:03,000
Hola, ¿qué tal? Muy bien, gracias.

2
00:00:04,000 --> 00:00:06,000
No sé lo que quiero, pero es mi decisión y no la tuya.`, "es"},

		{"chinese", `1
00:00:01,000 --> 00:00:03,000
你好，我是你的朋友。

2
00:00:04,000 --> 00:00:06,000
今天天气很好，我们一起去吧。`, "zh"},

		{"japanese kana wins over han", `1
00:00:01,000 --> 00:00:03,000
こんにちは、元気ですか。

2
00:00:04,000 --> 00:00:06,000
今日はいい天気ですね。`, "ja"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := DetectLanguage(strings.NewReader(tc.text))
			if !ok {
				t.Fatalf("DetectLanguage returned ok=false")
			}
			if got != tc.want {
				t.Errorf("DetectLanguage = %q, want %q", got, tc.want)
			}
		})
	}
}

// Timestamps and cue numbers must not sway the result.
func TestDetectLanguage_IgnoresMarkup(t *testing.T) {
	only := `1
00:00:01,000 --> 00:00:03,000

2
00:00:04,000 --> 00:00:06,000
`
	if _, _, ok := DetectLanguage(strings.NewReader(only)); ok {
		t.Error("expected ok=false when the file has no dialogue")
	}
}

func TestDetectLanguage_Empty(t *testing.T) {
	if _, _, ok := DetectLanguage(strings.NewReader("")); ok {
		t.Error("expected ok=false for empty input")
	}
}
