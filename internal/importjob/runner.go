package importjob

import (
	"context"
	"path"

	"github.com/kodestar/audiosilo-manager/internal/serverapi"
	"github.com/kodestar/audiosilo-manager/internal/source"
	"github.com/kodestar/audiosilo-manager/internal/transfer"
)

// Progress reports the state of one item during a Run.
type Progress struct {
	Index  int    // 0-based item index
	Total  int    // total items
	Title  string // book title
	File   string // destination rel path
	Copied int64  // bytes copied so far (for Status "transferring")
	Bytes  int64  // total bytes for the current file
	Status string // "downloading" | "transferring" | "placed" | "skipped" | "error"
	Err    string // set when Status == "error"
}

// RunOptions configures Run.
type RunOptions struct {
	Client       *serverapi.Client // for the post-transfer rescan; nil skips it
	LibraryID    int64
	Transfer     transfer.Transfer
	TransferRoot string
	Acquirer     source.Acquirer // download-on-demand sources (nil for file sources)
	OnProgress   func(Progress)
}

// ItemResult is the outcome of one item.
type ItemResult struct {
	RelPath        string
	Placed         bool
	AlreadyPresent bool
	Skipped        bool
	Err            string
}

// Run places each planned item via the transfer backend, then triggers a single
// rescan so the server indexes the new files. Items already on the server are
// skipped; download-on-demand records are acquired (and their temp cleaned up)
// just-in-time. Per-item progress is reported via opts.OnProgress.
func Run(ctx context.Context, items []PlannedItem, opts RunOptions) ([]ItemResult, error) {
	results := make([]ItemResult, 0, len(items))
	placedAny := false

	for i, it := range items {
		emit := func(status, errMsg string, copied, total int64) {
			if opts.OnProgress != nil {
				opts.OnProgress(Progress{
					Index: i, Total: len(items), Title: it.Record.Title, File: it.RelPath,
					Copied: copied, Bytes: total, Status: status, Err: errMsg,
				})
			}
		}

		if it.ExistsOnServer {
			results = append(results, ItemResult{RelPath: it.RelPath, Skipped: true})
			emit("skipped", "", 0, 0)
			continue
		}

		src := it.Record.SrcPath
		var cleanup func()
		if src == "" && opts.Acquirer != nil {
			// Acquire (download + DRM-strip) is the longest stage and reports no
			// sub-progress; emit a status so the UI shows it's working, not stalled.
			emit("downloading", "", 0, 0)
			local, cl, err := opts.Acquirer.Acquire(ctx, it.Record)
			if err != nil {
				results = append(results, ItemResult{RelPath: it.RelPath, Err: err.Error()})
				emit("error", err.Error(), 0, 0)
				continue
			}
			src, cleanup = local, cl
		}

		relDir, file := path.Split(it.RelPath)
		res, err := opts.Transfer.Place(ctx, transfer.PlaceRequest{
			Root: opts.TransferRoot, RelDir: relDir, Filename: file, SrcPath: src,
			Progress: func(c, total int64) { emit("transferring", "", c, total) },
		})
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			results = append(results, ItemResult{RelPath: it.RelPath, Err: err.Error()})
			emit("error", err.Error(), 0, 0)
			continue
		}
		placedAny = placedAny || !res.AlreadyPresent
		results = append(results, ItemResult{RelPath: it.RelPath, Placed: true, AlreadyPresent: res.AlreadyPresent})
		emit("placed", "", 0, 0)
	}

	// One rescan after all placements so the server indexes the new files.
	if placedAny && opts.Client != nil {
		if err := opts.Client.Scan(ctx, opts.LibraryID); err == nil {
			_, _ = opts.Client.WaitForScan(ctx, opts.LibraryID)
		}
	}
	return results, nil
}
