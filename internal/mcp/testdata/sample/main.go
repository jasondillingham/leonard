// Package main is a fixture used by Leonard's MCP integration tests.
// Don't edit without updating internal/mcp/mcp_test.go's expected symbols.
package main

import "fmt"

// Greeter prints greetings.
type Greeter struct {
	Prefix string
}

// Greet writes a greeting for name to stdout.
func (g *Greeter) Greet(name string) {
	fmt.Println(g.Prefix, name)
}

// DefaultGreeting is the canned greeting prefix.
const DefaultGreeting = "Hello,"

func main() {
	g := &Greeter{Prefix: DefaultGreeting}
	g.Greet("world")
}
