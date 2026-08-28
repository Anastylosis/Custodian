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
			in:    "/data/River Stone/clip.mp4",
			want:  "/mnt/nas/Videos/River Stone/clip.mp4",
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
			in:    "/data/River Stone/clip.mp4",
			want:  `Z:\Videos\River Stone\clip.mp4`,
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
		const orig = "/data/River Stone/clip.en.srt"
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

func TestToLocal_NilMapperIsIdentity(t *testing.T) {
	var m *Mapper
	got, err := m.ToLocal("/data/x.mp4")
	if err != nil {
		t.Fatalf("ToLocal error: %v", err)
	}
	if want := "/data/x.mp4"; got != want {
		t.Errorf("ToLocal = %q, want %q", got, want)
	}
}

func TestToStash_NilMapperIsIdentity(t *testing.T) {
	var m *Mapper
	got, err := m.ToStash("/mnt/videos/x.mp4")
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/mnt/videos/x.mp4"; got != want {
		t.Errorf("ToStash = %q, want %q", got, want)
	}
}

func TestToStash_NoRulesIsIdentity(t *testing.T) {
	m := mustNew(t, nil)
	got, err := m.ToStash("/mnt/videos/x.mp4")
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/mnt/videos/x.mp4"; got != want {
		t.Errorf("ToStash = %q, want %q", got, want)
	}
}

func TestToLocal_EmptyPathIsAnError(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/videos"}})
	if _, err := m.ToLocal(""); err == nil {
		t.Fatal("expected an error for an empty path, got nil")
	}
}

func TestToStash_EmptyPathIsAnError(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/videos"}})
	if _, err := m.ToStash(""); err == nil {
		t.Fatal("expected an error for an empty path, got nil")
	}
}

func TestToStash_UnmappedPathIsAnError(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/videos"}})
	if _, err := m.ToStash("/elsewhere/x.mp4"); err == nil {
		t.Fatal("expected an error for an unmapped local path, got nil")
	}
}

func TestToStash_ExactPrefixMatch(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/videos"}})
	got, err := m.ToStash("/mnt/videos")
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/data"; got != want {
		t.Errorf("ToStash(exact prefix) = %q, want %q", got, want)
	}
}

func TestToStash_LongestLocalPrefixWins(t *testing.T) {
	m := mustNew(t, []Rule{
		{"/data", "/mnt/main"},
		{"/data-archive", "/mnt/main/archive"},
	})
	got, err := m.ToStash("/mnt/main/archive/old.mp4")
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/data-archive/old.mp4"; got != want {
		t.Errorf("ToStash = %q, want %q", got, want)
	}
}

func TestRules(t *testing.T) {
	var nilMapper *Mapper
	if got := nilMapper.Rules(); got != nil {
		t.Errorf("nil Mapper.Rules() = %v, want nil", got)
	}

	m := mustNew(t, []Rule{
		{"/data", "/mnt/main"},
		{"/data/archive", "/mnt/cold"},
	})
	got := m.Rules()
	if len(got) != 2 || got[0].StashPrefix != "/data/archive" || got[1].StashPrefix != "/data" {
		t.Fatalf("Rules() = %+v, want longest-prefix-first pair", got)
	}
	got[0].StashPrefix = "mutated"
	if m.Rules()[0].StashPrefix == "mutated" {
		t.Error("Rules() must return a copy, mutation leaked into the mapper")
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

func TestJoin_TrailingSeparatorAndEmptyName(t *testing.T) {
	if got := Join("/mnt/v/", "a.srt"); got != "/mnt/v/a.srt" {
		t.Errorf("Join posix trailing slash = %q", got)
	}
	if got := Join(`Z:\v\`, "a.srt"); got != `Z:\v\a.srt` {
		t.Errorf("Join windows trailing backslash = %q", got)
	}
	if got := Join("/mnt/v", ""); got != "/mnt/v" {
		t.Errorf("Join empty name = %q, want prefix unchanged", got)
	}
}

func TestDir_NoSeparatorOrRoot(t *testing.T) {
	if got := Dir("a.srt"); got != "" {
		t.Errorf("Dir(no separator) = %q, want empty", got)
	}
	if got := Dir("/a.srt"); got != "/" {
		t.Errorf("Dir(root-level) = %q, want \"/\"", got)
	}
}

func TestBase_NoSeparator(t *testing.T) {
	if got := Base("a.srt"); got != "a.srt" {
		t.Errorf("Base(no separator) = %q, want %q", got, "a.srt")
	}
}

func TestToStash_PrefixMustEndOnBoundary(t *testing.T) {
	m := mustNew(t, []Rule{{"/data", "/mnt/main"}, {"/data2", "/mnt/maindb"}})
	got, err := m.ToStash("/mnt/maindb/x.mp4")
	if err != nil {
		t.Fatalf("ToStash error: %v", err)
	}
	if want := "/data2/x.mp4"; got != want {
		t.Errorf("ToStash = %q, want %q", got, want)
	}
}

func TestTrimTrailingSep_PreservesRoots(t *testing.T) {
	tests := []struct{ in, want string }{
		{`Z:\`, `Z:\`},
		{`\\`, `\\`},
		{"/", "/"},
		{"/mnt/v//", "/mnt/v"},
	}
	for _, tc := range tests {
		if got := trimTrailingSep(tc.in); got != tc.want {
			t.Errorf("trimTrailingSep(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
