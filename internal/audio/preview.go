package audio

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"time"
)

// End may extend beyond EOF (including the default start+15); output stops at EOF.
func ValidPreviewRange(start, end, duration float64) bool {
	return !math.IsNaN(start) && !math.IsNaN(end) && start >= 0 && start < duration && end > start && end <= 1215
}

func Preview(ctx context.Context, source, destination string, start, end, duration float64) error {
	if !ValidPreviewRange(start, end, duration) {
		return fmt.Errorf("invalid preview range")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	number := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	// Decode only the selected audio track; omit artwork and source metadata.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-threads", "1", "-protocol_whitelist", "file", "-ss", number(start), "-i", source, "-t", number(math.Min(end, duration)-start), "-map", "0:a:0", "-map_metadata", "-1", "-vn", "-ac", "2", "-ar", "44100", "-c:a", "libvorbis", "-q:a", "2", "-threads", "1", "-f", "ogg", "-n", destination)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("preview encoding failed: %w", err)
	}
	if err := CheckPages(destination); err != nil {
		return fmt.Errorf("invalid generated preview: %w", err)
	}
	return nil
}
