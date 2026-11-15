// Command tidy-next runs the new screen while it is built, beside the
// current gh-tidy; it goes once the new screen replaces the current one.
package main

import (
	"fmt"
	"os"

	"github.com/babarot/gh-tidy/internal/app"
)

func main() {
	if err := app.Run("tidy-next", os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tidy-next:", err)
		os.Exit(1)
	}
}
