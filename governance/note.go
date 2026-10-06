package governance

import (
	"context"

	"github.com/liliang-cn/dataintelligence/engine"
)

// AuditNote records an event that is not a query — a decision, an action
// taken on someone's behalf, a refusal of either — in the same trail queries
// go to. The consulting loop uses it so "who adopted that plan" and "who tried
// to approve their own" are answered by the table the customer already reads.
func AuditNote(ctx context.Context, eng *engine.Engine, p Principal, note string, refused bool) {
	writeAudit(ctx, eng, p, "", "", "", refused, note)
}
