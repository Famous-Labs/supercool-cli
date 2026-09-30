// Command supercool is the SuperCool CLI: your SuperCool agent in the terminal.
package main

import (
	"os"

	"github.com/Famous-Labs/supercool-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
