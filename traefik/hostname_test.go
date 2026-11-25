package traefik

import "testing"

func TestRuleHostsRejectsUnicodeCaseFold(t *testing.T) {
	if hosts := ruleHosts("Host(`K.home.arpa`)"); len(hosts) != 0 {
		t.Fatalf("Unicode hostname yielded %v, want no DNS name", hosts)
	}
}
