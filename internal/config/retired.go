package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// retired names the config keys the rebuild removed, and what replaced each.
//
// A removed key is not an error the loader can raise — yaml.v3 ignores a key
// with no struct field, so an old config keeps loading and quietly stops
// meaning anything. `custodian config validate` is where that becomes
// visible, which is why every entry here says what took the key's place
// rather than only that it is gone.
var retired = map[string]string{
	"scoring": "the keeper rules are built in and ordered resolution tier -> bitrate -> " +
		"codec -> size -> filed location -> older scene id; `custodian plan` quotes which " +
		"one decided each group. scoring.path_priority in particular decided nothing: " +
		"it is now the location tiebreak, read off organize.base_dir",
	"review_policy": "there is no metadata-versus-quality conflict to flag any more — " +
		"`apply` unions the losers' metadata onto the keeper first, so nothing is lost " +
		"by keeping the better file",
	"apply": "`apply` has one behaviour: union, tag, quarantine. It is a dry run unless " +
		"you pass --commit. The loser and keeper tags are quarantine.tag and " +
		"quarantine.mistag_tag",
	"merge": "the union policy comes from stash-go (DefaultUnionPolicy) so every project " +
		"here merges scenes the same way",
	"orphans": "identification is opt-in and not part of the chain (REBUILD.md §7 phase 6); " +
		"custodian never writes a stash_id on its own",
	"stash_box_fingerprints": "submitting fingerprints is not part of the chain; " +
		"use Stash's own stash-box submission",
}

// Problem is one thing wrong with a config file.
type Problem struct {
	Key    string
	Detail string
}

func (p Problem) String() string { return p.Key + ": " + p.Detail }

// Check reads the config file at path and reports everything wrong with it:
// keys the rebuild retired, and whatever Validate rejects. A missing file is
// not a problem — the embedded defaults are a valid config.
func Check(path string) ([]Problem, error) {
	var problems []Problem

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		problems = append(problems, retiredIn(data)...)
	case errors.Is(err, os.ErrNotExist):
		// Nothing on disk to check; the defaults still get validated below.
	default:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg, err := Load(path)
	if err != nil {
		problems = append(problems, Problem{Key: "config", Detail: err.Error()})
		return problems, nil
	}
	if err := cfg.Validate(); err != nil {
		problems = append(problems, Problem{Key: "config", Detail: err.Error()})
	}
	return problems, nil
}

// retiredIn finds the retired top-level keys present in a raw config file.
func retiredIn(data []byte) []Problem {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// A file that does not parse is Load's error to report, not ours.
		return nil
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		if _, ok := retired[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	problems := make([]Problem, 0, len(keys))
	for _, k := range keys {
		problems = append(problems, Problem{
			Key:    k,
			Detail: "retired — " + wrapDetail(retired[k]),
		})
	}
	return problems
}

// wrapDetail keeps a long replacement note readable in a terminal by folding
// it at 78 columns and indenting the continuations under the first word.
// indent matches the "  %-9s " prefix `config validate` prints the key with,
// so the note reads as one block rather than a ragged left edge.
const indent = "            "

func wrapDetail(s string) string {
	const width = 78
	var b strings.Builder
	col := len(indent) + len("retired — ")
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			b.WriteString(word)
			col += len(word)
		case col+1+len(word) > width:
			b.WriteString("\n")
			b.WriteString(indent)
			b.WriteString(word)
			col = len(indent) + len(word)
		default:
			b.WriteString(" ")
			b.WriteString(word)
			col += 1 + len(word)
		}
	}
	return b.String()
}
