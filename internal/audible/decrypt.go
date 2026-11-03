package audible

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Decrypt strips the DRM from the AAXC file in into a DRM-free .m4b at out: a
// lossless ffmpeg -c copy with the license's key and iv (ffmpeg >= 4.4). The
// key and iv go into ffmpeg's argv and nowhere else (visible in the process
// table while it runs: accepted, PACKAGES.md); ffmpeg's own words, which can
// quote an option's value ("Error setting option audible_key to value ..."),
// have both removed before they reach the error. An unencrypted MP4 passes
// through unchanged (ffmpeg ignores the key without an encrypted track: the
// Audible fake's download relies on it).
func Decrypt(ctx context.Context, ffmpeg, in, out string, lic License) error {
	if lic.Key == "" || lic.IV == "" {
		return errors.New("the license has no key")
	}
	return runFFmpeg(ctx, ffmpeg, aaxcArgs(in, lic.Key, lic.IV, out), lic.Key, lic.IV)
}

// aaxcArgs builds ffmpeg arguments to decrypt a modern AAXC file to a DRM-free m4b
// (lossless copy, faststart). key/iv are the 32-hex values from the voucher. The
// decryption options precede -i (they are input/demuxer options). Requires ffmpeg
// ≥ 4.4 for the audible_key/audible_iv options.
func aaxcArgs(in, key, iv, out string) []string {
	return append([]string{
		"-audible_key", key, "-audible_iv", iv,
		"-i", in,
	}, remuxArgs(out)...)
}

// remuxArgs are the shared output args for a lossless copy to m4b. It maps the
// audio and (optionally) the cover image, but NOT data/text streams: an AAXC can
// carry a timed-text/data stream that the ipod (m4b) muxer can't write, which
// failed the whole conversion ("Tag text incompatible with output codec id …;
// Could not write header"). Chapters and global metadata are still copied. The
// muxer is named (ipod), so the output's name needn't end in .m4b.
func remuxArgs(out string) []string {
	return []string{
		"-map", "0:a", "-map", "0:v?", "-map_metadata", "0",
		"-c", "copy", "-movflags", "+faststart",
		"-f", "ipod", "-y", out,
	}
}

// runFFmpeg executes ffmpeg with args, surfacing the tail of stderr on failure
// with every secret in it replaced.
func runFFmpeg(ctx context.Context, ffmpeg string, args []string, secrets ...string) error {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpeg, append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"}, args...)...) //nolint:gosec // G204: the configured ffmpeg with typed arguments, never a shell
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, redact(lastLines(stderr.String(), 3), secrets...))
	}
	return nil
}

// redact replaces every secret (case-insensitively: ffmpeg may print hex in
// either case) in s.
func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = regexp.MustCompile("(?i)"+regexp.QuoteMeta(sec)).ReplaceAllLiteralString(s, "[redacted]")
		}
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
