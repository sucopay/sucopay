package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/observe"
)

// aReadableNetwork is the sections declaring a network that runs inside the
// process, read a second apart, and one asset on it.
func aReadableNetwork() string {
	return "networks:\n  local:\n    kind: simulated\n    poll: 1s\n" + anAsset("local")
}

// wordAfter is what the deployment says about the network within three
// seconds, which is long enough for a round at the poll these documents set.
func wordAfter(t *testing.T, port int, want string) string {
	t.Helper()
	var word string
	for began := time.Now(); time.Since(began) < 3*time.Second; time.Sleep(50 * time.Millisecond) {
		_, body, _ := readyz(t, port)
		if word = wordFor(body, "networks", "local"); word == want {
			return word
		}
	}
	return word
}

// The command exists for a deployment that has stopped: the chain no longer
// holds the block the cursor sits on, and nothing moves until somebody puts
// the cursor where the chain does hold one. This is that, end to end.
func TestRun_NetworkCursorStartsAStoppedDeploymentAgain(t *testing.T) {
	port := freePort(t)
	d := deployed(t)
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s%s", port, namingADatabase(), aReadableNetwork()))

	// The cursor on a block no chain of this document has. A reader that
	// carried on from it would be reading a chain the records did not come off.
	if _, _, err := observe.NewCursors(d.pool.Conns()).Set(t.Context(), "local",
		observe.Position{Height: 0, Hash: "0x" + strings.Repeat("de", 32)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, stop := serving(t)
	stuck := wordAfter(t, port, "finalized-changed")
	stop()

	if stuck != "finalized-changed" {
		t.Fatalf("the deployment says %q, want it stopped where the chain holds no block", stuck)
	}

	if _, _, err := runArgs(t, "network", "cursor", "local", "0"); err != nil {
		t.Fatalf("err = %v, want none", err)
	}

	_, stopAgain := serving(t)
	defer stopAgain()
	if word := wordAfter(t, port, "observing"); word != "observing" {
		t.Errorf("the deployment says %q after the cursor was put back, want observing", word)
	}
}

// The hash written is the one the chain gave for that height, and not one the
// command made up: a height alone does not say which chain it was on.
func TestRun_NetworkCursorWritesTheHashTheChainGave(t *testing.T) {
	d := deployed(t)
	document(t, namingADatabase()+aReadableNetwork())

	stdout, _, err := runArgs(t, "network", "cursor", "local", "0")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	at, read, err := observe.NewCursors(d.pool.Conns()).Get(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if !read {
		t.Fatal("the network has no cursor")
	}
	if at.Height != 0 || at.Hash == "" {
		t.Errorf("the cursor is at %d on %q, want the block the chain gave for height 0", at.Height, at.Hash)
	}
	// Both, so that whoever reads the log after an operator has been at the
	// database can see what was replaced.
	if !strings.Contains(stdout, at.Hash) {
		t.Errorf("the log does not say where the cursor was put:\n%s", stdout)
	}
}

// It says what it replaced, so that a cursor put somewhere by mistake can be
// put back from the log.
func TestRun_NetworkCursorSaysWhatItReplaced(t *testing.T) {
	d := deployed(t)
	document(t, namingADatabase()+aReadableNetwork())
	was := "0x" + strings.Repeat("ab", 32)
	if _, _, err := observe.NewCursors(d.pool.Conns()).Set(t.Context(), "local",
		observe.Position{Height: 0, Hash: was}, time.Now()); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "network", "cursor", "local", "0")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, was) {
		t.Errorf("the log does not say where the cursor was:\n%s", stdout)
	}
}

func TestRun_NetworkCursorRefusesWhatItCannotPut(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"a network the document does not declare", []string{"elsewhere", "0"}},
		{"a height the chain does not have", []string{"local", "99"}},
		{"a height that is not a number", []string{"local", "eight"}},
		{"no height at all", []string{"local"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deployed(t)
			document(t, namingADatabase()+aReadableNetwork())

			_, _, err := runArgs(t, append([]string{"network", "cursor"}, c.args...)...)

			if err == nil {
				t.Errorf("network cursor %v was accepted", c.args)
			}
		})
	}
}

// Moving a cursor is not a round, so it waits for nothing and holds nothing
// up: a round that is under way finds the cursor moved and starts again from
// where it was put.
func TestRun_NetworkCursorTakesNoLease(t *testing.T) {
	d := deployed(t)
	document(t, namingADatabase()+aReadableNetwork())

	if _, _, err := runArgs(t, "network", "cursor", "local", "0"); err != nil {
		t.Fatal(err)
	}

	var held int
	if err := d.pool.Conns().QueryRow(t.Context(),
		`select count(*) from leases`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("%d leases are held, want none: moving a cursor is not reading a chain", held)
	}
}
