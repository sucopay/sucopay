package config_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/payment"
)

// jpycReference is the reference the asset tests write. Its shape is not
// checked here: an adapter decides what a reference on its network looks like.
const jpycReference = "0x0000000000000000000000000000000000000001"

// assetDocument is a document declaring one simulated network and one asset
// on it, with the fields given overriding the asset's.
func assetDocument(fields map[string]any) map[string]any {
	asset := map[string]any{
		"network": "local", "reference": jpycReference, "symbol": "JPYC", "decimals": 18,
	}
	for k, v := range fields {
		if v == nil {
			delete(asset, k)
			continue
		}
		asset[k] = v
	}
	return map[string]any{
		"networks": map[string]any{"local": map[string]any{"kind": "simulated"}},
		"assets":   map[string]any{"jpyc": asset},
	}
}

func TestResolve_ReadsAnAssetAsThePaymentPackageDescribesIt(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, assetDocument(nil), noEnv).Config

	asset, ok := got.Assets.Asset("jpyc")
	if !ok {
		t.Fatalf("Asset(jpyc) = not found; assets = %v", got.Assets)
	}
	want, err := payment.NewAsset("local", jpycReference, "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	if !asset.Same(want) {
		t.Errorf("asset = %v, want the same asset as %v", asset, want)
	}
	if asset.Symbol() != "JPYC" || asset.Decimals() != 18 {
		t.Errorf("symbol, decimals = %q, %d, want JPYC, 18", asset.Symbol(), asset.Decimals())
	}
	if _, ok := got.Assets.Asset("usdc"); ok {
		t.Error("Asset(usdc) = found, want not found")
	}
}

func TestResolve_LeavesAssetsEmptyWhenTheDocumentHasNone(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, map[string]any{}, noEnv).Config

	if len(got.Assets) != 0 {
		t.Errorf("assets = %v, want none", got.Assets)
	}
	if _, ok := got.Assets.Asset("jpyc"); ok {
		t.Error("Asset(jpyc) = found on an empty list")
	}
}

func TestResolve_RejectsAnAssetOnANetworkTheDocumentDoesNotDeclare(t *testing.T) {
	t.Parallel()
	doc := assetDocument(map[string]any{"network": "polygon"})

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "assets.jpyc.network" {
		t.Fatalf("problems = %v, want one at assets.jpyc.network", got)
	}
	if !strings.Contains(got[0].Message, "polygon") {
		t.Errorf("message %q does not name the network the asset asked for", got[0].Message)
	}
}

func TestResolve_RejectsAnAssetMissingAField(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"network", "reference", "symbol", "decimals"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			doc := assetDocument(map[string]any{field: nil})

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 || got[0].Path != "assets.jpyc."+field || got[0].Message != "required" {
				t.Fatalf("problems = %v, want one at assets.jpyc.%s saying required", got, field)
			}
		})
	}
}

func TestResolve_RejectsAnAssetWithAnEmptyField(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"network", "reference", "symbol"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			doc := assetDocument(map[string]any{field: ""})

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 || got[0].Path != "assets.jpyc."+field || got[0].Message != "required" {
				t.Fatalf("problems = %v, want one at assets.jpyc.%s saying required", got, field)
			}
		})
	}
}

func TestResolve_RefusesDecimalsWithThePaymentPackagesWords(t *testing.T) {
	t.Parallel()
	doc := assetDocument(map[string]any{"decimals": payment.MaxAssetDecimals + 1})

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "assets.jpyc.decimals")
	if !strings.Contains(p.Message, payment.ErrTooManyDecimals.Error()) {
		t.Errorf("message %q is not the one payment.NewAsset gives", p.Message)
	}
}

func TestResolve_AcceptsDecimalsUpToWhatAnAssetMayHave(t *testing.T) {
	t.Parallel()
	doc := assetDocument(map[string]any{"decimals": payment.MaxAssetDecimals})

	got := mustResolve(t, doc, noEnv).Config

	if asset, _ := got.Assets.Asset("jpyc"); asset.Decimals() != payment.MaxAssetDecimals {
		t.Errorf("decimals = %d, want %d", asset.Decimals(), payment.MaxAssetDecimals)
	}
}

// TestResolve_RejectsDecimalsThatWouldWrapAround keeps a count that does not
// fit the type an asset holds from being cut down to one that does. Both
// counts are ones an asset may have once cut: 256 is 0, and 18 - 256 is 18.
// A count such as -1 would not do, since 255 is refused for its size either
// way.
func TestResolve_RejectsDecimalsThatWouldWrapAround(t *testing.T) {
	t.Parallel()
	for _, decimals := range []int{256, 18 - 256} {
		t.Run(strconv.Itoa(decimals), func(t *testing.T) {
			t.Parallel()
			doc := assetDocument(map[string]any{"decimals": decimals})

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 || got[0].Path != "assets.jpyc.decimals" {
				t.Fatalf("problems = %v, want one at assets.jpyc.decimals", got)
			}
		})
	}
}

func TestResolve_RejectsDecimalsWrittenAsText(t *testing.T) {
	t.Parallel()
	doc := assetDocument(map[string]any{"decimals": "eighteen"})

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "assets.jpyc.decimals" {
		t.Fatalf("problems = %v, want one at assets.jpyc.decimals", got)
	}
	if !strings.Contains(got[0].Message, "want a number") {
		t.Errorf("message %q is not the one a number elsewhere gets", got[0].Message)
	}
}

func TestResolve_RejectsTwoNamesForOneAsset(t *testing.T) {
	t.Parallel()
	doc := assetDocument(nil)
	doc["assets"].(map[string]any)["yen"] = map[string]any{
		"network": "local", "reference": jpycReference, "symbol": "YEN", "decimals": 6,
	}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "assets" {
		t.Fatalf("problems = %v, want one at assets", got)
	}
	for _, name := range []string{"jpyc", "yen"} {
		if !strings.Contains(got[0].Message, name) {
			t.Errorf("message %q does not name %s", got[0].Message, name)
		}
	}
}

func TestResolve_AcceptsTwoAssetsOnOneNetwork(t *testing.T) {
	t.Parallel()
	doc := assetDocument(nil)
	doc["assets"].(map[string]any)["usdc"] = map[string]any{
		"network": "local", "reference": "0x0000000000000000000000000000000000000002",
		"symbol": "USDC", "decimals": 6,
	}

	got := mustResolve(t, doc, noEnv).Config

	if len(got.Assets) != 2 {
		t.Errorf("got %d assets, want 2: %v", len(got.Assets), got.Assets)
	}
}

func TestResolve_RejectsAnAssetNameWithADot(t *testing.T) {
	t.Parallel()
	doc := assetDocument(nil)
	doc["assets"] = map[string]any{"jpyc.v2": doc["assets"].(map[string]any)["jpyc"]}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "assets" {
		t.Fatalf("problems = %v, want one at assets and none for the keys under the name", got)
	}
	if !strings.Contains(got[0].Message, "jpyc.v2") {
		t.Errorf("message %q does not name the asset", got[0].Message)
	}
}

// A name a reader cannot see is refused where the reference and the symbol
// beside it already are. The name is what a probe, a report and the list of
// assets are keyed by, and every one of those is read by somebody.
func TestResolve_RejectsANameNobodyCouldRead(t *testing.T) {
	t.Parallel()
	for what, name := range map[string]string{
		"an override of the reading order": "jp\u202eyc",
		"a zero-width space":               "jp\u200byc",
		"a newline":                        "jp\nyc",
		"a space that is not one":          "jp\u00a0yc",
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			doc := assetDocument(nil)
			doc["assets"] = map[string]any{name: doc["assets"].(map[string]any)["jpyc"]}

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 || got[0].Path != "assets" {
				t.Fatalf("problems = %v, want one at assets and none for the keys under the name", got)
			}
			// The message says which name, and does so without carrying the
			// character into whatever reads the message.
			if strings.Contains(got[0].Message, name) {
				t.Errorf("message %q repeats the name as it was written", got[0].Message)
			}
			if !strings.Contains(got[0].Message, "jp") {
				t.Errorf("message %q does not name the asset", got[0].Message)
			}
		})
	}
}

// The same rule on a network, refused whether or not an asset names it.
func TestResolve_RejectsANetworkNameNobodyCouldRead(t *testing.T) {
	t.Parallel()
	// Beside the one the asset settles on, so that the only thing wrong with
	// the document is the name.
	doc := assetDocument(nil)
	doc["networks"].(map[string]any)["po\u202elygon"] = map[string]any{"kind": "simulated"}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "networks" {
		t.Fatalf("problems = %v, want one at networks and none for the keys under the name", got)
	}
	if !strings.Contains(got[0].Message, "does not show up") {
		t.Errorf("message %q is not the one a name nobody could read gets", got[0].Message)
	}
}

func TestResolve_RejectsAssetsThatAreNotAMapping(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"assets": []any{"jpyc"}}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "assets" {
		t.Fatalf("problems = %v, want one at assets", got)
	}
}

func TestResolve_ReportsAnAssetsProblemsTogether(t *testing.T) {
	t.Parallel()
	doc := assetDocument(map[string]any{"network": "polygon", "symbol": "", "decimals": 40})

	_, err := config.Resolve(doc, noEnv)

	var got []string
	for _, p := range problems(t, err) {
		got = append(got, p.Path)
	}
	want := "assets.jpyc.decimals assets.jpyc.network assets.jpyc.symbol"
	if strings.Join(got, " ") != want {
		t.Errorf("problem paths = %v, want exactly %s", got, want)
	}
}

func TestReport_ShowsTheFourValuesOfAnAsset(t *testing.T) {
	t.Parallel()
	lines := mustResolve(t, assetDocument(nil), noEnv).Report()

	want := map[string]string{
		"assets.jpyc.network":   "local",
		"assets.jpyc.reference": jpycReference,
		"assets.jpyc.symbol":    "JPYC",
		"assets.jpyc.decimals":  "18",
	}
	for path, value := range want {
		line := lineAt(t, lines, path)
		if line.Value != value || line.Secret || line.Source.Origin != config.FromFile {
			t.Errorf("%s = %+v, want %q from the file and not secret", path, line, value)
		}
	}
}

// The EIP-712 domain an asset's contract signs under is the document's to
// give, beside the asset, and is read back by the asset rather than by its
// name.
func TestResolve_ReadsAnAssetsDomainAndFindsItByTheAsset(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, assetDocument(map[string]any{
		"eip712": map[string]any{"name": "JPY Coin", "version": "1"},
	}), noEnv)

	asset, _ := got.Config.Assets.Asset("jpyc")
	domain, ok := got.Config.Domain(asset)

	if !ok || domain.Name != "JPY Coin" || domain.Version != "1" {
		t.Errorf("Domain = %+v, %t; want JPY Coin 1", domain, ok)
	}
	same, err := payment.NewAsset("local", jpycReference, "Other Name", 18)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := got.Config.Domain(same); !ok || d.Name != "JPY Coin" {
		t.Errorf("Domain of the same token under another symbol = %+v, %t; want found", d, ok)
	}
	var values = map[string]string{}
	for _, line := range got.Report() {
		values[line.Path] = line.Value
	}
	if values["assets.jpyc.eip712.name"] != "JPY Coin" || values["assets.jpyc.eip712.version"] != "1" {
		t.Errorf("report = %v, want the domain under assets.jpyc.eip712", values)
	}
}

func TestResolve_LeavesAnAssetWithoutADomainWhenNoneIsGivenAndRefusesHalfOfOne(t *testing.T) {
	t.Parallel()
	none := mustResolve(t, assetDocument(nil), noEnv).Config
	asset, _ := none.Assets.Asset("jpyc")
	if d, ok := none.Domain(asset); ok {
		t.Errorf("Domain without one given = %+v, want none", d)
	}
	for what, half := range map[string]map[string]any{
		"name alone":    {"name": "JPY Coin"},
		"version alone": {"version": "1"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			_, err := config.Resolve(assetDocument(map[string]any{"eip712": half}), noEnv)

			got := problems(t, err)
			if len(got) != 1 || !strings.HasPrefix(got[0].Path, "assets.jpyc.eip712.") {
				t.Errorf("problems = %v, want one under assets.jpyc.eip712", got)
			}
		})
	}
}
