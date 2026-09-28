package claudeconfig

import (
	"context"
	"os"
	"time"
)

// WatchDebounce is how long the config has to be quiet before a change is
// reported. Exported so a test does not have to wait it out.
var WatchDebounce = 300 * time.Millisecond

// Watch reports a debounced change to Claude's config until ctx is done. It polls
// instead of watching the directory: a kqueue directory watch opens every sibling,
// and in $HOME that blocks on ~/Desktop's privacy prompt. A missing file is not an error.
func Watch(ctx context.Context, onChange func()) error {
	path, err := JSONPath()
	if err != nil {
		return err
	}

	last := fingerprint(path)
	go func() {
		poll := time.NewTicker(WatchDebounce / 2)
		defer poll.Stop()
		timer := time.NewTimer(WatchDebounce)
		timer.Stop()
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-poll.C:
				if now := fingerprint(path); now != last {
					last = now
					resetTimer(timer, WatchDebounce)
				}
			case <-timer.C:
				onChange()
			}
		}
	}()

	return nil
}

type fileState struct {
	modTime time.Time
	size    int64
	exists  bool
}

func fingerprint(path string) fileState {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}
	}
	return fileState{modTime: info.ModTime(), size: info.Size(), exists: true}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
