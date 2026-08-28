package cli

import (
	"bytes"
	"strings"
	"testing"
)

// `custodian` with no arguments is the tool's front page: the chain, in the
// order it is meant to be run. A command that exists but is not listed there
// is a command nobody will find, so the two lists have to agree exactly.
func TestChainListsEveryRegisteredCommand(t *testing.T) {
	root := NewRootCmd("test", "none", "never")

	registered := map[string]bool{}
	for _, c := range root.Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		registered[c.Name()] = true
	}

	listed := map[string]bool{}
	for _, c := range chain {
		if listed[c.name] {
			t.Errorf("%s appears twice in the chain", c.name)
		}
		listed[c.name] = true
		if !registered[c.name] {
			t.Errorf("chain lists %q, which is not a registered command", c.name)
		}
		if c.what == "" {
			t.Errorf("chain entry %q has no description", c.name)
		}
	}
	for name := range registered {
		if !listed[name] {
			t.Errorf("command %q is registered but missing from the chain listing", name)
		}
	}
}

func TestNoArgsPrintsTheChainInOrder(t *testing.T) {
	root := NewRootCmd("test", "none", "never")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	text := out.String()
	at := -1
	for _, c := range chain {
		i := strings.Index(text, "  "+c.name+" ")
		if i < 0 {
			t.Fatalf("%q missing from the no-args output:\n%s", c.name, text)
		}
		if i < at {
			t.Errorf("%q is printed out of chain order", c.name)
		}
		at = i
	}
	if !strings.Contains(text, "--commit") {
		t.Error("the no-args output should say writes are dry runs until --commit")
	}
}
