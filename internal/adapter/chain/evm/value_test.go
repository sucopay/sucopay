package evm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuantity_ReadsOnlyTheWayAChainWritesANumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		json  string
		want  quantity
		reads bool
	}{
		{"a number", `"0x89"`, 0x89, true},
		{"zero", `"0x0"`, 0, true},
		{"the widest a chain counts to", `"0xffffffffffffffff"`, 1<<64 - 1, true},
		{"no prefix", `"89"`, 0, false},
		{"a leading zero", `"0x089"`, 0, false},
		{"no digits", `"0x"`, 0, false},
		{"decimal", `137`, 0, false},
		{"not hexadecimal", `"0xzz"`, 0, false},
		{"wider than a chain counts to", `"0x1ffffffffffffffff"`, 0, false},
		{"nothing", `null`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got quantity

			err := json.Unmarshal([]byte(c.json), &got)

			if reads := err == nil; reads != c.reads {
				t.Fatalf("read = %v, want %v: %v", reads, c.reads, err)
			}
			if c.reads && got != c.want {
				t.Errorf("read %d, want %d", got, c.want)
			}
		})
	}
}

func TestValues_ReadOnlyTheirOwnWidth(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("ab", 40)
	cases := []struct {
		name  string
		into  json.Unmarshaler
		json  string
		reads bool
	}{
		{"a hash", new(hash), `"0x` + full[:64] + `"`, true},
		{"a hash of 31 bytes", new(hash), `"0x` + full[:62] + `"`, false},
		{"a hash of 33 bytes", new(hash), `"0x` + full[:66] + `"`, false},
		{"a hash without the prefix", new(hash), `"` + full[:64] + `"`, false},
		{"an address", new(address), `"0x` + full[:40] + `"`, true},
		{"an address of 19 bytes", new(address), `"0x` + full[:38] + `"`, false},
		{"an address of 21 bytes", new(address), `"0x` + full[:42] + `"`, false},
		{"an address in mixed case", new(address), `"0xAB` + full[2:40] + `"`, true},
		{"a word", new(word), `"0x` + full[:64] + `"`, true},
		{"a word of 31 bytes", new(word), `"0x` + full[:62] + `"`, false},
		{"digits that are not hexadecimal", new(hash), `"0x` + strings.Repeat("zz", 32) + `"`, false},
		{"a number where a hash goes", new(hash), `1`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := json.Unmarshal([]byte(c.json), c.into)

			if reads := err == nil; reads != c.reads {
				t.Errorf("read = %v, want %v: %v", reads, c.reads, err)
			}
		})
	}
}

func TestTopics_ReadNoMoreThanALogCarries(t *testing.T) {
	t.Parallel()
	one := `"0x` + strings.Repeat("ab", 32) + `"`
	cases := []struct {
		name  string
		count int
		reads bool
	}{
		{"none", 0, true},
		{"one", 1, true},
		{"the most a log has", maxTopics, true},
		{"one more than a log has", maxTopics + 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list := make([]string, c.count)
			for i := range list {
				list[i] = one
			}
			var got topics

			err := json.Unmarshal([]byte("["+strings.Join(list, ",")+"]"), &got)

			if reads := err == nil; reads != c.reads {
				t.Fatalf("read = %v, want %v: %v", reads, c.reads, err)
			}
			if c.reads && len(got) != c.count {
				t.Errorf("read %d topics, want %d", len(got), c.count)
			}
		})
	}
}

func TestValues_WriteWhatAChainReads(t *testing.T) {
	t.Parallel()
	var (
		height quantity = 0x89
		block  hash
		who    address
	)
	block[0], who[0] = 0xab, 0xcd

	for name, pair := range map[string][2]string{
		"a quantity": {marshal(t, height), `"0x89"`},
		"a hash":     {marshal(t, block), `"0xab` + strings.Repeat("00", 31) + `"`},
		"an address": {marshal(t, who), `"0xcd` + strings.Repeat("00", 19) + `"`},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s writes as %s, want %s", name, pair[0], pair[1])
		}
	}
}

// marshal is what a value looks like on its way into a request.
func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
