package builtin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/reports"
)

// @SpamBot tells the admins about new reports, like it does appeals. The
// first one after a quiet spell goes out right away; anything filed within
// the notice gap after that waits and goes out as one message, so a wave
// of reports is one notice every few minutes, not one per report.

// noticeTargets is how many targets a notice lists by name.
const noticeTargets = 5

// noticeRecheck catches reports left from before a restart.
const noticeRecheck = time.Minute

func (r *runner) reportNotices(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Reports.Filed():
		case <-timer.C:
		}
		next := noticeRecheck
		list, wait, err := r.Reports.TakeNotice(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				slog.Warn("spambot: reports to tell", "err", err)
			}
		case wait > 0:
			next = wait
		case len(list) > 0:
			r.tellAdmins(ctx, botapi.Out{Text: r.reportNotice(ctx, list)})
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
	}
}

func (r *runner) reportNotice(ctx context.Context, list []reports.Pending) string {
	total := 0
	for _, p := range list {
		total += p.Reports
	}
	var b strings.Builder
	if total == 1 {
		b.WriteString("New report")
	} else {
		fmt.Fprintf(&b, "%d new reports", total)
		if len(list) > 1 {
			fmt.Fprintf(&b, " on %d accounts or chats", len(list))
		}
	}
	b.WriteString(".\n")
	for i, p := range list {
		if i == noticeTargets {
			fmt.Fprintf(&b, "\nand %d more.", len(list)-i)
			break
		}
		fmt.Fprintf(&b, "\n%s: %s", r.Reports.TargetName(ctx, p.TargetKey), reports.Label(p.Reason))
		if p.Reports > 1 {
			fmt.Fprintf(&b, " (%d reports)", p.Reports)
		}
	}
	fmt.Fprintf(&b, "\n\nReview them in the panel: %s/admin#reports", strings.TrimRight(r.BaseURL, "/"))
	return b.String()
}
