package pathmap

import "testing"

func mustNew(t *testing.T, rules []Rule) *Mapper {
	t.Helper()
	m, err := New(rules)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return m
}

func TestToLocal(t *testing.T) {
	tests := []struct {
		name  string
		rules []Rule
		in    string
		want  string
	}{
		{
			name:  "linux mount",
			rules: []Rule{{"/data", "/mnt/nas/Videos"}},
			in:    "/data/Some Performer/clip.mp4",
			want:  "/mnt/nas/Videos/Some Performer/clip.mp4",
		},
		{
			name:  "nas local path",
			rules: []Rule{{"/data", "/share/main/Videos"}},
			in:    "/data/tmp2/x.srt",
			want:  "/share/main/Videos/tmp2/x.srt",
		},
		{
			name:  "windows mapped drive",
			rules: []Rule{{"/data", `Z:\Videos`}},
			in:    "/data/Some Performer/clip.mp4",
			want:  `Z:\Videos\Some Performer\clip.mp4`,
		},
		{
			name:  "windows UNC",
			rules: []Rule{{"/data", `\\nas\main\Videos`}},
			in:    "/data/jav/URE-005.mp4",
			want:  `\\nas\main\Videos\jav\URE-005.mp4`,
		},
		{
			name:  "windows drive root",
			rules: []Rule{{"/data", `Z:\`}},
			in:    "/data/a/b.mp4",
			want:  `Z:\a\b.mp4`,
		},
		{
			name:  "trailing slashes tolerated",
			rules: []Rule{{"/data/", "/mnt/videos/"}},
			in:    "/data/x.mp4",
			want:  "/mnt/videos/x.mp4",
		},
		{
			name:  "identity when no rules",
			rules: nil,
			in:    "/data/x.mp4",
			want:  "/data/x.mp4",
		},
		{
			name: "longest prefix wins",
			rules: []Rule{
				{"/data", "/mnt/main"},
				{"/data/archive", "/mnt/cold"},
			},
			in:   "/data/archive/old.mp4",
			want: "/mnt/cold/old.mp4",
		},
		{
			name:  "prefix must end on a boundary",
			rules: []Rule{{"/data", "/mnt/main"}, {"/database", "/mnt/db"}},
			in:    "/database/x.mp4",
			want:  "/mnt/db/x.mp4",
		},
		{
			name:  "path equal to prefix",
			rules: []Rule{{"/data", "/mnt/main"}},
			in:    "/data",
			want:  "/mnt/main",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mustNew(t, tc.rules).ToLocal(tc.in)
			if err != nil {
				t.Fatalf("ToLocal(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ToLocal(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestToLocal_UnmappedPathIsAnError(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/videos"}})
	if _, err := m.ToLocal("/elsewhere/x.mp4"); err == nil {
		t.Fatal("expected an error for an unmapped path, got nil")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, rules := range [][]Rule{
		{{"/data", "/mnt/nas/Videos"}},
		{{"/data", `Z:\Videos`}},
		{{"/data", `\\nas\main\Videos`}},
	} {
		m := mustNew(t, rules)
		const orig = "/data/Some Performer/clip.en.srt"
		local, err := m.ToLocal(orig)
		if err != nil {
			t.Fatalf("ToLocal: %v", err)
		}
		back, err := m.ToStash(local)
		if err != nil {
			t.Fatalf("ToStash(%q): %v", local, err)
		}
		if back != orig {
			t.Errorf("round trip via %q: got %q, want %q", rules[0].LocalPrefix, back, orig)
		}
	}
}

func TestToStash_WindowsCaseInsensitive(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", `Z:\Videos`}})
	got, err := m.ToStash(`z:\videos\Sub\x.srt`)
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/data/Sub/x.srt"; got != want {
		t.Errorf("ToStash = %q, want %q", got, want)
	}
}

func TestNew_RejectsBadRules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []Rule
	}{
		{"empty local", []Rule{{"/data", ""}}},
		{"empty stash", []Rule{{"", "/mnt/x"}}},
		{"relative stash prefix", []Rule{{"data", "/mnt/x"}}},
		{"windows stash prefix", []Rule{{`Z:\data`, "/mnt/x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.rules); err == nil {
				t.Errorf("New(%+v) = nil error, want an error", tc.rules)
			}
		})
	}
}

func TestJoinDirBase(t *testing.T) {
	if got := Join("/mnt/v", "a.srt"); got != "/mnt/v/a.srt" {
		t.Errorf("Join posix = %q", got)
	}
	if got := Join(`Z:\v`, "a.srt"); got != `Z:\v\a.srt` {
		t.Errorf("Join windows = %q", got)
	}
	if got := Dir(`Z:\v\a.srt`); got != `Z:\v` {
		t.Errorf("Dir windows = %q", got)
	}
	if got := Base(`Z:\v\a.srt`); got != "a.srt" {
		t.Errorf("Base windows = %q", got)
	}
	if got := Base("/mnt/v/a.srt"); got != "a.srt" {
		t.Errorf("Base posix = %q", got)
	}
}
