// Command gh-tidy keeps one GitHub owner's local clones up to date and clear
// of finished work, so they are ready to hand to a coding agent. It lists the
// clones with what stands in the way: how far each is from its upstream and
// its default branch, what is not committed or not pushed, and its worktrees.
package main

import (
	"fmt"
	"os"

	"github.com/babarot/gh-tidy/internal/app"
)

func main() {
	if err := app.Run("gh tidy", os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gh tidy:", err)
		os.Exit(1)
	}
}
