// Command sandclaude is the host-side CLI. All logic lives in internal/cli;
// this entrypoint exists only to satisfy Go's cmd/ layout convention.
package main

import "github.com/jackrothrock/sandclaude/internal/cli"

func main() {
	cli.Main()
}
