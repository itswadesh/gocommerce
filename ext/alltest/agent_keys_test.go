package alltest

import (
	"slices"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// AgentKeyRefusedRights names some module rights by their strings, because the
// list is about what an agent may hold rather than about declaring them. A
// module renaming one would leave the old name refused and the new one open,
// so every name on it must be a right this build — every module — declares.
func TestEveryRightRefusedToAgentsExists(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	all := app.Rights()
	for right := range gocommerce.AgentKeyRefusedRights {
		if !slices.Contains(all, right) {
			t.Errorf("AgentKeyRefusedRights names %q, which no module declares", right)
		}
	}
	// The one right reviewed as read-like for an agent, likewise.
	if !slices.Contains(all, "agent.dispatch") || !gocommerce.AgentMayHold("agent.dispatch") {
		t.Error("agent.dispatch is not a right an agent key may hold in this build")
	}
}
