package subs

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"time"
)

// cueTimeRe matches an SRT/VTT timestamp: 01:41:48,980 or 01:41:48.980.
var cueTimeRe = regexp.MustCompile(`(\d{1,3}):(\d{2}):(\d{2})[,.](\d{1,3})`)

// Runtime returns the timestamp of the last cue in a subtitle stream — an
// approximation of the video's length.
//
// This is the signal that makes matching trustworthy. Two scenes can share a
// title, a performer and a studio, but their runtimes differ; in a real library
// the correct match landed within a couple of seconds while a same-titled decoy
// was 23 minutes away. Filename similarity could not tell them apart.
//
// The whole stream is scanned rather than just the tail, because cues are not
// always stored in chronological order and a trailing credit line can carry a
// timestamp earlier than the true end.
//
// Returns ok=false when the stream contains no parsable timestamp.
func Runtime(r io.Reader) (d time.Duration, ok bool) {
	sc := bufio.NewScanner(r)
	// Subtitle lines are short, but a malformed file can hold a very long one.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var max time.Duration
	for sc.Scan() {
		for _, m := range cueTimeRe.FindAllStringSubmatch(sc.Text(), -1) {
			if t, good := parseCue(m); good && t > max {
				max = t
			}
		}
	}
	if max == 0 {
		return 0, false
	}
	return max, true
}

func parseCue(m []string) (time.Duration, bool) {
	if len(m) != 5 {
		return 0, false
	}
	h, err1 := strconv.Atoi(m[1])
	mi, err2 := strconv.Atoi(m[2])
	s, err3 := strconv.Atoi(m[3])
	ms, err4 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, false
	}
	if mi > 59 || s > 59 {
		return 0, false
	}
	// A 2-digit fraction means centiseconds.
	if len(m[4]) == 2 {
		ms *= 10
	} else if len(m[4]) == 1 {
		ms *= 100
	}
	return time.Duration(h)*time.Hour +
		time.Duration(mi)*time.Minute +
		time.Duration(s)*time.Second +
		time.Duration(ms)*time.Millisecond, true
}

// RuntimeFit scores how well a subtitle runtime matches a scene duration.
//
// A subtitle's final cue normally ends slightly BEFORE the video does, so the
// expected delta (sceneDur - subRuntime) is small and positive. A negative
// delta means the subtitle outlives the video, which is evidence against the
// pairing rather than for it.
//
// Returns a score in [0,1] and the signed delta. A zero score means the
// runtimes are incompatible.
func RuntimeFit(subRuntime, sceneDur time.Duration) (score float64, delta time.Duration) {
	if subRuntime <= 0 || sceneDur <= 0 {
		return 0, 0
	}
	delta = sceneDur - subRuntime
	switch {
	case delta < -20*time.Second:
		return 0, delta // subtitle runs well past the end of the video
	case delta <= 20*time.Second:
		return 1, delta
	case delta <= 60*time.Second:
		return 0.6, delta
	case delta <= 180*time.Second:
		return 0.25, delta
	default:
		return 0, delta
	}
}
