package dream

import "time"

// Dreaming behavior is intentionally not user-configurable: the only switch is
// [dreaming].enabled. These values bound cost and keep consolidation offline.
const (
	runInterval  = 24 * time.Hour
	minIdle      = 15 * time.Minute
	runTimeout   = 10 * time.Minute
	retryDelay   = 30 * time.Minute
	maxFailures  = 3
	tickInterval = time.Minute

	lookbackDays       = 14
	maxSessions        = 20
	maxTurnsPerSession = 80
	maxMessageChars    = 1200
	maxCharsPerSession = 8000
	maxDigestChars     = 48000
	maxMemoryChars     = 12000
	maxOutputTokens    = 4096
	maxTranscriptBytes = 16 << 20
	maxErrorChars      = 500

	journalDirName    = "dreams"
	journalRetention  = 30
	backupRetention   = 10
	latestJournalFile = "latest.md"
	memoryBackupBase  = "MEMORY"
)
