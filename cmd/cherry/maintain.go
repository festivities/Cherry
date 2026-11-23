package main

import (
	"context"
	"log"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
)

// maintain runs the housekeeping of the stores until ctx ends (PLAN 8.6b part 4): the batched writes of the tokens' last
// use every few minutes, and once an hour the pruning of idle alias tokens (retention.idle-token-days) and the deletion of old
// chat messages (retention.chat-days). Deleted avatars are kept with no expiry (deleted_avatars, 2026-11-21).
// A failure is logged and the next round tries again.
func maintain(ctx context.Context, logger *log.Logger, c config.Config) {
	flush := time.NewTicker(5 * time.Minute)
	defer flush.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	sweep(logger, c)
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			if err := store.FlushTokenUse(); err != nil {
				logger.Printf("cherry: token last-use write failed: %v", err)
			}
		case <-hourly.C:
			sweep(logger, c)
		}
	}
}

// sweep is one round of the retention work of the hourly timer (and of the start).
func sweep(logger *log.Logger, c config.Config) {
	now := time.Now()
	days := store.ChatRetention(c.ChatDays) // the staff panel's value when one is set, else retention.chat-days
	if n, err := store.PruneChatLog(now, days); err != nil {
		logger.Printf("cherry: prune chat log failed: %v", err)
	} else if n > 0 {
		logger.Printf("cherry: chat log deleted %d messages older than %d days", n, days)
	}
	if n, err := store.PruneIdleTokens(now, c.IdleTokenDays); err != nil {
		logger.Printf("cherry: prune idle tokens failed: %v", err)
	} else if n > 0 {
		logger.Printf("cherry: pruned %d idle alias tokens (unused for %d days)", n, c.IdleTokenDays)
	}
	if rows, freed, orphans, err := store.PruneStaleMedia(now, time.Hour); err != nil {
		logger.Printf("cherry: prune stale media failed: %v", err)
	} else if rows > 0 || orphans > 0 {
		logger.Printf("cherry: pruned %d stale media rows (%d bytes) and %d orphan files", rows, freed, orphans)
	}
}
