package traefik

import (
	"reflect"
	"testing"
)

// Wrong matcher or negation handling would register unrelated DNS names.
func TestRuleHosts(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule string
		want []string
	}{
		{"literal", "Host(`App.Home.Arpa.`)", []string{"app.home.arpa"}},
		{"quoted and combined", `Host("one.home.arpa") || (Host(` + "`two.home.arpa`" + `) && PathPrefix("/"))`, []string{"one.home.arpa", "two.home.arpa"}},
		{"legacy multiple arguments", "Host(`one.home.arpa`, `two.home.arpa`)", []string{"one.home.arpa", "two.home.arpa"}},
		{"duplicate", "Host(`one.home.arpa`) || Host(`ONE.HOME.ARPA`)", []string{"one.home.arpa"}},
		{"negative", "!Host(`not.home.arpa`) && Host(`yes.home.arpa`)", []string{"yes.home.arpa"}},
		{"negative group", "!(Host(`not.home.arpa`) || Path(`/`))", nil},
		{"double negative", "!!Host(`yes.home.arpa`)", []string{"yes.home.arpa"}},
		{"regexp", "HostRegexp(`.*.home.arpa`)", nil},
		{"string containing matcher", "Header(`X-Test`, \"Host(`bad.home.arpa`)\")", nil},
		{"invalid domains", "Host(`*.home.arpa`, `bad_name.home.arpa`, `-bad.home.arpa`, `bad..arpa`, `127.0.0.1`, `a/b.home.arpa`)", nil},
		{"bad rule", "Host(`broken.home.arpa`", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleHosts(tc.rule); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ruleHosts(%q) = %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
}
