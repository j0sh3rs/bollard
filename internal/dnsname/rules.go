package dnsname

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// RuleHosts returns the canonical, valid DNS names a Traefik router rule matches with a positive
// Host() matcher, in order of appearance without duplicates. Negated matchers, HostRegexp, HostSNI,
// wildcards and IP literals yield nothing, and a rule that does not parse yields nothing.
func RuleHosts(rule string) []string {
	expr, err := parser.ParseExpr(rule)
	if err != nil {
		return nil
	}
	var hosts []string
	seen := make(map[string]bool)
	var visit func(ast.Expr, bool)
	visit = func(expr ast.Expr, negative bool) {
		switch node := expr.(type) {
		case *ast.ParenExpr:
			visit(node.X, negative)
		case *ast.UnaryExpr:
			if node.Op == token.NOT {
				visit(node.X, !negative)
			}
		case *ast.BinaryExpr:
			if node.Op == token.LAND || node.Op == token.LOR {
				visit(node.X, negative)
				visit(node.Y, negative)
			}
		case *ast.CallExpr:
			name, ok := node.Fun.(*ast.Ident)
			if !ok || name.Name != "Host" || negative {
				return
			}
			for _, arg := range node.Args {
				literal, ok := arg.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				host, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}
				host = Canonical(host)
				if Valid(host) && !seen[host] {
					seen[host] = true
					hosts = append(hosts, host)
				}
			}
		}
	}
	visit(expr, false)
	return hosts
}
