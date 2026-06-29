package audible

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// aaxcArgs builds ffmpeg arguments to decrypt a modern AAXC file to a DRM-free m4b
// (lossless copy, faststart). key/iv are the 32-hex values from the voucher. The
// decryption options precede -i (they are input/demuxer options). Requires ffmpeg
// ≥ 4.4 for the audible_key/audible_iv options.
func aaxcArgs(in, key, iv, out string) []string {
	return []string{
		"-audible_key", key, "-audible_iv", iv,
		"-i", in,
		"-map", "0", "-map_metadata", "0",
		"-c", "copy", "-movflags", "+faststart",
		"-y", out,
	}
}

// aaxArgs builds ffmpeg arguments to decrypt a legacy AAX file with the
// account-wide activation bytes (8 hex).
func aaxArgs(in, activationBytes, out string) []string {
	return []string{
		"-activation_bytes", activationBytes,
		"-i", in,
		"-map", "0", "-map_metadata", "0",
		"-c", "copy", "-movflags", "+faststart",
		"-y", out,
	}
}

// runFFmpeg executes ffmpeg with args, surfacing the tail of stderr on failure.
func runFFmpeg(ctx context.Context, ffmpeg string, args []string) error {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLines(stderr.String(), 3))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
