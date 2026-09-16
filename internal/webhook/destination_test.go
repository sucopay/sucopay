package webhook_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/webhook"
)

// answering resolves every name to the addresses given, the way a name the
// merchant controls answers with whatever its owner chose.
type answering []string

func (a answering) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	if len(a) == 0 {
		return nil, errors.New("no such host")
	}
	addrs := make([]netip.Addr, 0, len(a))
	for _, s := range a {
		addrs = append(addrs, netip.MustParseAddr(s))
	}
	return addrs, nil
}

// Every range isInside names, each address refused however it is spelled,
// and one public address beside an inside one refusing the destination
// whole.
func TestCheck_RefusesADestinationThatResolvesInside(t *testing.T) {
	t.Parallel()
	for what, addrs := range map[string][]string{
		"loopback":                  {"127.0.0.1"},
		"loopback v6":               {"::1"},
		"link-local":                {"169.254.10.10"},
		"the metadata service":      {"169.254.169.254"},
		"metadata carried in v6":    {"::ffff:169.254.169.254"},
		"link-local v6":             {"fe80::1"},
		"private 10":                {"10.0.0.5"},
		"private 172":               {"172.16.0.5"},
		"private 192":               {"192.168.1.5"},
		"unique local v6":           {"fd00::5"},
		"shared address space":      {"100.64.0.1"},
		"Alibaba metadata":          {"100.100.100.200"},
		"the zero network":          {"0.0.0.0"},
		"broadcast":                 {"255.255.255.255"},
		"multicast":                 {"224.0.0.1"},
		"one public and one inside": {"93.184.216.34", "10.0.0.5"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			_, problems := webhook.Check(t.Context(), answering(addrs), "https://hooks.example/in", nil)

			if len(problems) != 1 || problems[0].Field != "url" {
				t.Fatalf("problems = %v, want one naming url", problems)
			}
			for _, a := range addrs {
				if strings.Contains(problems[0].Message, a) {
					t.Errorf("the refusal repeats the address: %s", problems[0].Message)
				}
			}
		})
	}
}

func TestCheck_RefusesTheShapesADestinationMayNotHave(t *testing.T) {
	t.Parallel()
	for what, raw := range map[string]string{
		"http":                            "http://hooks.example/in",
		"a username":                      "https://user:pw@hooks.example/in",
		"no host":                         "https:///in",
		"not a URL":                       "://",
		"an interface zone":               "https://[fe80::1%25eth0]/in",
		"a name that resolves to nothing": "https://nowhere.example/in",
		"an unseen character":             "https://hooks.example/in\u200b",
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			var resolver answering
			if what != "a name that resolves to nothing" {
				resolver = answering{"93.184.216.34"}
			}
			_, problems := webhook.Check(t.Context(), resolver, raw, nil)

			if len(problems) != 1 || problems[0].Field != "url" {
				t.Errorf("problems = %v, want one naming url", problems)
			}
		})
	}
}

func TestCheck_AnswersWithTheAddressesItSaw(t *testing.T) {
	t.Parallel()
	d, problems := webhook.Check(t.Context(), answering{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"https://hooks.example:8443/in", nil)

	if problems != nil {
		t.Fatalf("problems = %v, want none", problems)
	}
	if d.URL.Hostname() != "hooks.example" || d.URL.Port() != "8443" {
		t.Errorf("URL = %s, want the one given", d.URL)
	}
	if len(d.Addrs) != 2 || d.Addrs[0].String() != "93.184.216.34" {
		t.Errorf("Addrs = %v, want both the name resolved to", d.Addrs)
	}
}

// An operator may allow one destination to reach an address inside, and the
// allowance is the address, so a name moved to another inside address is
// refused as before.
func TestCheck_AllowsAnInsideAddressTheOperatorNamedAndNoOther(t *testing.T) {
	t.Parallel()
	allowed := []netip.Prefix{netip.MustParsePrefix("10.0.5.0/24")}

	if _, problems := webhook.Check(t.Context(), answering{"10.0.5.20"}, "https://erp.internal/in", allowed); problems != nil {
		t.Errorf("the allowed address was refused: %v", problems)
	}
	if _, problems := webhook.Check(t.Context(), answering{"10.0.6.20"}, "https://erp.internal/in", allowed); problems == nil {
		t.Error("an inside address outside the allowance was accepted")
	}
}
