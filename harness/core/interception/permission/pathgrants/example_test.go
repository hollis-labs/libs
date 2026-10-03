package pathgrants_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/harness/interception/permission/pathgrants"
)

func ExamplePathGrants_RegisterFromUserMessage() {
	g := pathgrants.NewPathGrants()
	g.RegisterFromUserMessage("session-1", "please read /srv/app/config.yaml for me")

	fmt.Println(g.IsPathAllowed("session-1", "/srv/app/config.yaml"))
	fmt.Println(g.IsPathAllowed("session-1", "/srv/app/other.txt")) // the parent directory is granted too
	fmt.Println(g.IsPathAllowed("session-1", "/etc/passwd"))
	fmt.Println(g.IsPathAllowed("session-2", "/srv/app/config.yaml")) // grants are per session
	// Output:
	// true
	// true
	// false
	// false
}

func ExampleExtractPathMentions() {
	fmt.Println(pathgrants.ExtractPathMentions("see /var/log/app.log, ~/notes.md and https://example.com/x"))
	// Output:
	// [/var/log/app.log ~/notes.md]
}
