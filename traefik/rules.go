package traefik

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"strconv"
	"strings"
)

func ruleHosts(rule string) []string {
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
				host = strings.ToLower(strings.TrimSuffix(host, "."))
				if validHostname(host) && !seen[host] {
					seen[host] = true
					hosts = append(hosts, host)
				}
			}
		}
	}
	visit(expr, false)
	return hosts
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}
