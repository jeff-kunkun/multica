package agent

import (
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/awakeclock"
)

// outputActivity stamps the awakeclock reading of the last line a CLI wrote,
// for Session.OutputActivity. Safe for one writer and any number of readers.
type outputActivity struct {
	at atomic.Int64
}

func (a *outputActivity) mark() { a.at.Store(int64(awakeclock.Now())) }

func (a *outputActivity) last() time.Duration { return time.Duration(a.at.Load()) }
