package audiblejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/fsutil"
	"github.com/kodestar/audiosilo-bridge/internal/importjob"
	"github.com/kodestar/audiosilo-bridge/internal/jobs"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/mapping"
	"github.com/kodestar/audiosilo-bridge/internal/placement"
	"github.com/kodestar/audiosilo-bridge/internal/source"
	"github.com/kodestar/audiosilo-bridge/internal/store"
	"github.com/kodestar/audiosilo-bridge/internal/transfer"
)

// ImportKind is the job kind that backs one Audible title up into the
// account's books library (BRIDGE-PLAN.md Phase 4b).
const ImportKind = "audible.import"

// The steps an import reports (progress.step), in order.
const (
	StepLicense  = "license"
	StepDownload = "download"
	StepDecrypt  = "decrypt"
	StepPlace    = "place"
	StepRescan   = "rescan"
	StepEnrich   = "enrich"
)

// The codes a failed import reports (its result {code, message}); any other
// failure has none, only its error.
const (
	CodeNotReleased    = "not_released"        // a preorder, or Audible says it isn't out
	CodeNoRights       = "no_rights"           // not owned, no download rights, a show or periodical
	CodeRefused        = "audible_refused"     // Audible refused the sign-in, or the license for another reason
	CodeNotWritable    = "not_writable"        // not the account's library, not a checked writable mapping, or no valid path there
	CodeExists         = "exists"              // a different file is at the destination
	CodeDiskFull       = "disk_full"           // the Bridge's temp space or the library is short of room
	CodeURLExpired     = "offline_url_expired" // the download URL expired again after one new license
	CodeUnknownAccount = "unknown_account"     // not this Bridge's account, or mapped on another server
)

// tmpHeadroom is the free-space factor for the Bridge's temp folder: it holds
// the AAXC and the decrypted copy at once (2 x, plus a margin). The library
// wants transfer's own (transfer.DefaultHeadroom).
const tmpHeadroom = 2.2

// PartialMaxAge is how long the start-up sweep keeps a partial download (a
// job that never resumed: cancelled while the Bridge was down, or lost).
const PartialMaxAge = 7 * 24 * time.Hour

// downloadAttempts bounds one license's download attempts (a dropped
// connection resumes from the partial each time).
const downloadAttempts = 3

// Failure is a failed import's result and error: a code from the list above
// and a sentence for the console. It never holds a key, an iv or a URL.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Failure) Error() string { return f.Message }

func fail(code, format string, args ...any) *Failure {
	return &Failure{Code: code, Message: fmt.Sprintf(format, args...)}
}

// refusedSignIn is the failure when Audible refuses the account's sign-in.
func refusedSignIn() *Failure {
	return fail(CodeRefused, "Audible refused the account's sign-in: sign in again on the Bridge's Accounts tab.")
}

// ImportParams are audible.import's params, built by the server: the account,
// the title, the account's books library, and the console's destination
// override (dest_path) and the account's path template ("" for none: Auto).
// Unknown fields are ignored (the server may add some).
type ImportParams struct {
	Account   string `json:"account"`
	ASIN      string `json:"asin"`
	LibraryID int64  `json:"library_id"`
	DestPath  string `json:"dest_path"`
	Template  string `json:"template"`
}

// maxTemplate bounds a path template.
const maxTemplate = 1024

// ParseImportParams reads and checks the params.
func ParseImportParams(raw json.RawMessage) (ImportParams, error) {
	var p ImportParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ImportParams{}, fmt.Errorf("bad params: %w", err)
	}
	p.ASIN = strings.ToUpper(strings.TrimSpace(p.ASIN))
	switch {
	case !audibleacct.IDPattern.MatchString(p.Account):
		return ImportParams{}, errors.New("bad params: account must be an Audible account id")
	case !asinPattern.MatchString(p.ASIN):
		return ImportParams{}, errors.New("bad params: asin must be an ASIN")
	case p.LibraryID <= 0:
		return ImportParams{}, errors.New("bad params: library_id is required")
	case len(p.Template) > maxTemplate || len(p.DestPath) > maxTemplate:
		return ImportParams{}, errors.New("bad params: the template or dest_path is too long")
	}
	return p, nil
}

// ImportResult is a done import: where the book is (the server's book path),
// the files placed and their bytes, and whether the book carries the title's
// ASIN now.
type ImportResult struct {
	LibraryID int64        `json:"library_id"`
	Path      string       `json:"path"`
	Files     []ImportFile `json:"files"`
	Bytes     int64        `json:"bytes"`
	Enriched  bool         `json:"enriched"`
}

// ImportFile is one placed file, library-relative.
type ImportFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ImportAccounts is the part of the account manager an import uses
// (*audibleacct.Manager): the account, one title's metadata, its license and
// its download, with the account's sign-in.
type ImportAccounts interface {
	Account(ctx context.Context, id string) (store.AudibleAccount, error)
	Title(ctx context.Context, id, asin string) (source.Record, error)
	License(ctx context.Context, id, asin string) (audible.License, error)
	Download(ctx context.Context, id, url, dest string, opts audible.DownloadOptions) error
}

// ImportServer is the part of the server link an import calls (*link.Client).
type ImportServer interface {
	LibraryBooks(ctx context.Context, libraryID int64, after string, limit int) (*link.LibraryBooks, error)
	Rescan(ctx context.Context, libraryID int64, path string) (string, error)
	Enrich(ctx context.Context, libraryID int64, path, asin string) error
}

// Libraries is the mapping registry an import checks (*mapping.Registry).
type Libraries interface {
	Library(id int64) (link.Library, bool)
	Status(id int64) (link.LibraryStatus, bool)
}

// ImportOptions wire the import handler.
type ImportOptions struct {
	Accounts  ImportAccounts
	Server    func() ImportServer
	ServerID  func() string
	Libraries Libraries
	Mount     mapping.MountFunc
	// TmpDir is <data>/audible/tmp: the partial downloads and decrypted copies.
	TmpDir string
	// FFmpeg is the ffmpeg to decrypt with ("" is ffmpeg on PATH).
	FFmpeg func() string
	// MaxBytes caps one download (0: audible.DefaultMaxDownloadBytes).
	MaxBytes int64
	Log      *slog.Logger

	// Seams (tests): the decrypt step, the transfer, the free-space reader,
	// the wait between attempts and how often a job claimed before its
	// library's mapping check looks for it (default 1 s).
	Decrypt   func(ctx context.Context, ffmpeg, in, out string, lic audible.License) error
	Transfer  *transfer.Local
	Free      func(dir string) (int64, error)
	Wait      func(ctx context.Context, d time.Duration) error
	CheckPoll time.Duration
	// onProgress sees every progress the job reports (tests).
	onProgress func(jobs.Progress)
}

// Importer runs audible.import, one at a time per account (two accounts
// download side by side when the lane has room).
type Importer struct {
	o ImportOptions

	mu    sync.Mutex
	gates map[string]chan struct{}
}

// NewImport returns the import handler.
func NewImport(o ImportOptions) *Importer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.ServerID == nil {
		o.ServerID = func() string { return "" }
	}
	if o.FFmpeg == nil {
		o.FFmpeg = func() string { return "" }
	}
	if o.Decrypt == nil {
		o.Decrypt = audible.Decrypt
	}
	if o.Transfer == nil {
		o.Transfer = transfer.NewLocal()
	}
	if o.Free == nil {
		o.Free = fsutil.FreeBytes
	}
	if o.Wait == nil {
		o.Wait = sleepCtx
	}
	if o.CheckPoll <= 0 {
		o.CheckPoll = time.Second
	}
	return &Importer{o: o, gates: map[string]chan struct{}{}}
}

// Kind is audible.import.
func (h *Importer) Kind() string { return ImportKind }

// Lane is the download lane.
func (h *Importer) Lane() string { return jobs.LaneDownload }

// tmpBase is the name every temp file of the account's title starts with.
func (h *Importer) tmpBase(account, asin string) string {
	return filepath.Join(h.o.TmpDir, account+"-"+asin)
}

// partPaths are the job's temp files: the partial AAXC (kept across a restart,
// with its state file) and the decrypted copy.
func (h *Importer) partPaths(account, asin string) (part, decrypted string) {
	base := h.tmpBase(account, asin)
	return base + ".aaxc.part", base + ".m4b"
}

// Abandon removes the temp files of a job the runner ended without running it
// (cancelled or lost before it resumed).
func (h *Importer) Abandon(_ context.Context, job jobs.Job) {
	if p, err := ParseImportParams(job.Params); err == nil {
		part, decrypted := h.partPaths(p.Account, p.ASIN)
		audible.RemovePartial(part)
		_ = os.Remove(decrypted)
	}
}

// Run imports the title: license, download, decrypt, place, rescan, enrich.
func (h *Importer) Run(ctx context.Context, job jobs.Job, rep *jobs.Reporter) (any, error) {
	p, err := ParseImportParams(job.Params)
	if err != nil {
		return nil, err
	}
	if job.LibraryID != nil && *job.LibraryID != p.LibraryID {
		return nil, errors.New("bad params: library_id isn't the job's library")
	}
	res, err := h.run(ctx, job, p, rep)
	var f *Failure
	if errors.As(err, &f) {
		return f, f
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (h *Importer) run(ctx context.Context, job jobs.Job, p ImportParams, rep *jobs.Reporter) (res *ImportResult, err error) {
	acct, err := h.o.Accounts.Account(ctx, p.Account)
	if errors.Is(err, audibleacct.ErrNotFound) {
		return nil, fail(CodeUnknownAccount, "This Bridge has no Audible account %s: it was removed here, or signed in on another Bridge.", p.Account)
	}
	if err != nil {
		return nil, err
	}
	if acct.OnOtherServer(h.o.ServerID()) {
		return nil, fail(CodeUnknownAccount, "The Audible account is mapped on another server: map it again on the Bridge's Accounts tab.")
	}
	if acct.LibraryID != p.LibraryID {
		return nil, fail(CodeNotWritable, "Library %d isn't the Audible account's books library: map the account again or queue the import again.", p.LibraryID)
	}
	var srv ImportServer
	if h.o.Server != nil {
		srv = h.o.Server()
	}
	if srv == nil {
		return nil, errors.New("the Bridge isn't paired with a server")
	}

	release, err := h.gate(ctx, acct.ID, rep)
	if err != nil {
		return nil, err
	}
	defer release()
	lib, mount, err := h.library(ctx, p.LibraryID, rep)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(h.o.TmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("the Bridge's temp folder: %w", err)
	}
	part, decrypted := h.partPaths(acct.ID, p.ASIN)
	defer func() {
		_ = os.Remove(decrypted)
		// The partial outlives a stop the next start resumes (decision 6);
		// a cancel, a lost claim, a failure or the end delete it.
		if err != nil && ctx.Err() != nil && jobs.Resumes(ctx) {
			return
		}
		audible.RemovePartial(part)
	}()

	// An earlier run installed the book and then failed (the rescan): its
	// marker says what it placed, and while the library holds exactly that the
	// job goes on from the rescan instead of failing exists.
	marker := h.markerPath(acct.ID, p.ASIN)
	if m, ok := readPlaced(marker, lib.ID, mount); ok {
		return h.finish(ctx, rep, srv, acct.ID, p.ASIN, lib.ID, installed{rel: m.Path, size: m.Size, already: true, enrich: !m.NoEnrich}, marker)
	}

	h.report(rep, jobs.Progress{Step: StepLicense, Message: "Reading the title from Audible"})
	rec, err := h.o.Accounts.Title(ctx, acct.ID, p.ASIN)
	switch {
	case errors.Is(err, audibleacct.ErrRefused):
		return nil, refusedSignIn()
	case errors.Is(err, audible.ErrNotInLibrary):
		return nil, fail(CodeNoRights, "The title isn't in the Audible account's library.")
	case err != nil:
		return nil, h.quiet(ctx, fmt.Errorf("reading the title from Audible: %w", err))
	case rec.Preorder:
		return nil, fail(CodeNotReleased, "The title isn't released yet (%s).", rec.UnavailableReason)
	case strings.TrimSpace(rec.UnavailableReason) != "":
		return nil, fail(CodeNoRights, "Audible won't let the account download the title: %s.", rec.UnavailableReason)
	}
	rec.ASIN, rec.Key = p.ASIN, p.ASIN

	books, err := allBooks(ctx, srv, lib.ID)
	if err != nil {
		return nil, h.quiet(ctx, fmt.Errorf("reading the library's books on the server: %w", err))
	}
	rel, err := Destination(p, rec, books)
	if err != nil {
		return nil, err
	}
	if err := h.vacant(mount, rel); err != nil {
		return nil, err
	}
	enrich := enrichable(rec, books, rel)

	lic, err := h.license(ctx, acct.ID, p.ASIN)
	if err != nil {
		return nil, err
	}
	h.report(rep, jobs.Progress{Step: StepDownload, Message: "Downloading from Audible"})
	if lic, err = h.download(ctx, acct.ID, p.ASIN, lic, part, mount, rel, rep); err != nil {
		return nil, err
	}

	h.report(rep, jobs.Progress{Step: StepDecrypt, Message: "Removing the DRM (a lossless copy)"})
	err = h.o.Decrypt(ctx, h.o.FFmpeg(), part, decrypted, lic)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil && (fsutil.IsNoSpace(err) || strings.Contains(err.Error(), "No space left")):
		return nil, fail(CodeDiskFull, "The Bridge ran out of space removing the DRM: free some space in its data folder.")
	case err != nil:
		return nil, fmt.Errorf("removing the DRM: %w", err)
	}
	audible.RemovePartial(part) // room for the copy into the library

	h.report(rep, jobs.Progress{Step: StepPlace, Message: "Copying the book into the library"})
	if err := h.writable(lib.ID, mount); err != nil {
		return nil, err
	}
	placed, err := h.o.Transfer.Place(ctx, transfer.PlaceRequest{Root: mount, RelPath: rel, SrcPath: decrypted,
		Progress: func(copied, total int64) {
			h.report(rep, jobs.Progress{Step: StepPlace, Message: "Copying the book into the library", Done: int(copied), Total: int(total)})
		}})
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, transfer.ErrExists):
		return nil, fail(CodeExists, "A different file is already at %s: nothing was overwritten. Choose another destination, or move that file.", rel)
	case errors.Is(err, transfer.ErrDiskFull):
		return nil, fail(CodeDiskFull, "The library is short of space for the book (%s).", err)
	case errors.Is(err, transfer.ErrInvalidPath), errors.Is(err, fs.ErrPermission):
		return nil, fail(CodeNotWritable, "The Bridge can't write %s in the library: %v", rel, err)
	case err != nil:
		return nil, fmt.Errorf("copying the book into the library: %w", err)
	}

	if err := writePlaced(marker, lib.ID, mount, placed.RelPath, !enrich); err != nil {
		h.o.Log.Warn("audible: recording an imported book's placement failed", "account", acct.ID, "asin", p.ASIN, "err", err)
	}
	return h.finish(ctx, rep, srv, acct.ID, p.ASIN, lib.ID,
		installed{rel: placed.RelPath, size: placed.Size, already: placed.AlreadyPresent, enrich: enrich}, marker)
}

// enrichable reports whether the book the title's file lands in at rel may
// take the title's ASIN. A new book is the title itself (a pack placed as a
// pack included). A file that joins a book the library already had (rel is
// inside its folder) gives it the ASIN only under the poll's rule: the
// matcher over the library picks that book for the title, and the title
// doesn't span books the book doesn't (importjob.SpanMismatch), so an omnibus
// never names a single volume and a title never names another book.
func enrichable(rec source.Record, books []link.LibraryBook, rel string) bool {
	joined, ok := bookAt(books, path.Dir(rel))
	if !ok {
		return true
	}
	b, matched := importjob.MatchInBooks(books, rec)
	return matched && b.Path == joined.Path && !importjob.SpanMismatch(rec, joined)
}

// bookAt is the library's book whose folder is dir or holds it (the deepest).
func bookAt(books []link.LibraryBook, dir string) (link.LibraryBook, bool) {
	var best link.LibraryBook
	found := false
	for _, b := range books {
		if (b.Path == dir || strings.HasPrefix(dir, b.Path+"/")) && (!found || len(b.Path) > len(best.Path)) {
			best, found = b, true
		}
	}
	return best, found
}

// installed is the file an install left in the library: where, its size,
// whether it was there already (an earlier run's), and whether its book may
// take the title's ASIN (enrichable).
type installed struct {
	rel             string
	size            int64
	already, enrich bool
}

// finish is the steps after the install: the rescan, the ASIN (when enrich)
// and the result. The placement marker goes once the result is made.
func (h *Importer) finish(ctx context.Context, rep *jobs.Reporter, srv ImportServer, account, asin string, libraryID int64,
	in installed, marker string) (*ImportResult, error) {
	rel, size := in.rel, in.size
	h.report(rep, jobs.Progress{Step: StepRescan, Message: "Asking the server to index the book"})
	bookPath, err := h.rescan(ctx, srv, libraryID, rel)
	if err != nil {
		return nil, h.quiet(ctx, fmt.Errorf("the book is in the library at %s, but the server couldn't index it: %w", rel, err))
	}

	enriched := false
	if in.enrich {
		h.report(rep, jobs.Progress{Step: StepEnrich, Message: "Adding the title's ASIN to the book"})
		if enriched, err = h.enrichPlaced(ctx, rep, srv, account, libraryID, bookPath, asin); err != nil {
			return nil, err
		}
	} else {
		h.o.Log.Info("audible: the imported file joined a book that isn't the title: its ASIN isn't added",
			"account", account, "asin", asin, "library", libraryID, "path", bookPath)
	}
	h.o.Log.Info("audible: imported", "account", account, "asin", asin, "library", libraryID, "path", bookPath,
		"bytes", size, "already_there", in.already, "enriched", enriched)
	_ = os.Remove(marker)
	return &ImportResult{LibraryID: libraryID, Path: bookPath, Files: []ImportFile{{Path: rel, Size: size}},
		Bytes: size, Enriched: enriched}, nil
}

// enrichPlaced gives the imported book its title's ASIN, waiting out the
// server's 429s as the poll's enrichment does (limitWait); a 429 it gives up
// on, an ASIN set meanwhile or a moved book is no failure, only no ASIN.
func (h *Importer) enrichPlaced(ctx context.Context, rep *jobs.Reporter, srv ImportServer, account string, libraryID int64,
	bookPath, asin string) (bool, error) {
	for limited := 0; ; {
		err := srv.Enrich(ctx, libraryID, bookPath, asin)
		if retry, ok := rateLimited(err); ok {
			limited++
			wait, ok := limitWait(retry, limited)
			if !ok {
				h.o.Log.Info("audible: the server kept refusing an imported book's ASIN (429): the book is placed without it",
					"account", account, "library", libraryID, "path", bookPath)
				return false, nil
			}
			h.report(rep, jobs.Progress{Step: StepEnrich, Message: "The server asked the Bridge to slow down: waiting"})
			if err := h.o.Wait(ctx, wait); err != nil {
				return false, err
			}
			continue
		}
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, link.ErrASINSet), errors.Is(err, link.ErrNoBook):
		case ctx.Err() != nil:
			return false, ctx.Err()
		default:
			h.o.Log.Warn("audible: adding an imported book's ASIN failed", "account", account, "library", libraryID, "err", err)
		}
		return false, nil
	}
}

// placedMarker is what an install recorded: the file it placed, with the size
// and modification time it had, so a retry knows the library still holds
// exactly that file (and nothing an administrator put there since), and
// whether the book it landed in may take the title's ASIN (enrichable).
type placedMarker struct {
	LibraryID int64  `json:"library_id"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTime   int64  `json:"mtime_ns"`
	NoEnrich  bool   `json:"no_enrich,omitempty"`
}

// markerPath is the job's placement marker, beside its temp files.
func (h *Importer) markerPath(account, asin string) string {
	return h.tmpBase(account, asin) + placedSuffix
}

// placedSuffix names the placement markers (SweepTmp keeps the young ones).
const placedSuffix = ".placed.json"

// writePlaced records the file an install placed at rel.
func writePlaced(marker string, libraryID int64, mount, rel string, noEnrich bool) error {
	st, err := placedStat(mount, rel)
	if err != nil {
		return err
	}
	b, err := json.Marshal(placedMarker{LibraryID: libraryID, Path: rel, Size: st.Size(), ModTime: st.ModTime().UnixNano(), NoEnrich: noEnrich})
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(marker, b, 0o600)
}

// readPlaced answers the marker when the library at mount holds exactly the
// file it recorded (a regular file of the same size and modification time);
// anything else is no marker, and a marker that doesn't hold any more goes.
func readPlaced(marker string, libraryID int64, mount string) (placedMarker, bool) {
	b, err := os.ReadFile(marker) //nolint:gosec // G304: the job's own marker in the Bridge's data
	if err != nil {
		return placedMarker{}, false
	}
	var m placedMarker
	if json.Unmarshal(b, &m) != nil || m.LibraryID != libraryID {
		_ = os.Remove(marker)
		return placedMarker{}, false
	}
	if _, err := transfer.CleanRel(m.Path); err != nil {
		_ = os.Remove(marker)
		return placedMarker{}, false
	}
	st, err := placedStat(mount, m.Path)
	if err != nil || st.Size() != m.Size || st.ModTime().UnixNano() != m.ModTime {
		_ = os.Remove(marker)
		return placedMarker{}, false
	}
	return m, true
}

// placedStat is the Lstat of a library file through the mount's root (never
// following a link out of it); only a regular file answers.
func placedStat(mount, rel string) (fs.FileInfo, error) {
	root, err := os.OpenRoot(mount)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	st, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	return st, nil
}

// report records the job's progress.
func (h *Importer) report(rep *jobs.Reporter, pr jobs.Progress) {
	rep.Progress(pr)
	if h.o.onProgress != nil {
		h.o.onProgress(pr)
	}
}

// quiet is err, or the context's own error when the job was stopped meanwhile
// (a stop's error is the runner's, never a message about a half-done step).
func (h *Importer) quiet(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// gate waits until no other import of the account runs: one download per
// account (Audible's limits are per account).
func (h *Importer) gate(ctx context.Context, account string, rep *jobs.Reporter) (func(), error) {
	h.mu.Lock()
	g, ok := h.gates[account]
	if !ok {
		g = make(chan struct{}, 1)
		h.gates[account] = g
	}
	h.mu.Unlock()
	select {
	case g <- struct{}{}:
		return func() { <-g }, nil
	default:
	}
	h.report(rep, jobs.Progress{Step: StepLicense, Message: "Waiting for this account's other backup to finish"})
	select {
	case g <- struct{}{}:
		return func() { <-g }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// library is the import's books library and its mount, a checked, writable
// mapping. A job claimed before the library's mapping check has a result for
// that mount (the first claims after a start) waits for it.
func (h *Importer) library(ctx context.Context, id int64, rep *jobs.Reporter) (link.Library, string, error) {
	for {
		lib, ok := h.o.Libraries.Library(id)
		if !ok || lib.LibraryKind() != "books" {
			return link.Library{}, "", fail(CodeNotWritable, "The server lists no books library %d for the Bridge.", id)
		}
		mount, _ := h.o.Mount(lib.ID, lib.Root)
		if st, checked := h.o.Libraries.Status(lib.ID); checked && st.MountPath == mount {
			return lib, mount, h.writable(lib.ID, mount)
		}
		h.report(rep, jobs.Progress{Step: StepLicense, Message: "Waiting for the library's mapping check"})
		if err := h.o.Wait(ctx, h.o.CheckPoll); err != nil {
			return link.Library{}, "", err
		}
	}
}

// writable checks the library is still a checked, writable mapping at mount
// (again before the copy: the mapping may have changed during the download).
func (h *Importer) writable(id int64, mount string) error {
	lib, ok := h.o.Libraries.Library(id)
	if !ok || lib.LibraryKind() != "books" {
		return fail(CodeNotWritable, "The server lists no books library %d for the Bridge any more.", id)
	}
	now, _ := h.o.Mount(lib.ID, lib.Root)
	st, ok := h.o.Libraries.Status(lib.ID)
	if now != mount || !ok || !st.Mapped || !st.Writable || st.MountPath != mount {
		return fail(CodeNotWritable, "Library %s must be mapped and writable on the Bridge: check its mapping on the Bridge's Status tab.", lib.Name)
	}
	return nil
}

// vacant fails early (before an hour's download) when something is already at
// the destination. The copy checks again, and accepts the same bytes there.
func (h *Importer) vacant(mount, rel string) error {
	full, err := mapping.SafeJoin(mount, rel)
	if err != nil {
		return fail(CodeNotWritable, "The destination %s isn't a valid path in the library.", rel)
	}
	if _, err := os.Lstat(full); err == nil {
		return fail(CodeExists, "A file is already at %s: nothing was overwritten. Choose another destination, or move that file.", rel)
	}
	return nil
}

// license asks Audible for the title's license, saying why it was denied.
func (h *Importer) license(ctx context.Context, account, asin string) (audible.License, error) {
	lic, err := h.o.Accounts.License(ctx, account, asin)
	switch {
	case err == nil:
		return lic, nil
	case ctx.Err() != nil:
		return audible.License{}, ctx.Err()
	case errors.Is(err, audibleacct.ErrRefused):
		return audible.License{}, refusedSignIn()
	case errors.Is(err, audible.ErrNotReleased):
		return audible.License{}, fail(CodeNotReleased, "Audible says the title isn't released yet: %v", err)
	case errors.Is(err, audible.ErrNoRights):
		return audible.License{}, fail(CodeNoRights, "Audible won't license a download of the title to the account: %v", err)
	case errors.Is(err, audible.ErrDenied):
		return audible.License{}, fail(CodeRefused, "Audible refused a download license: %v", err)
	}
	return audible.License{}, fmt.Errorf("asking Audible for a license: %w", err)
}

// errNoRoom is a free-space check's refusal inside the download.
var errNoRoom = errors.New("not enough free space")

// download fetches the license's file to part, resuming what a stopped or cut
// attempt left, with a new license once when the URL has expired. It returns
// the license the file came under (its key decrypts it).
func (h *Importer) download(ctx context.Context, account, asin string, lic audible.License, part, mount, rel string,
	rep *jobs.Reporter) (audible.License, error) {
	relicensed := false
	var room string
	opts := audible.DownloadOptions{
		MaxBytes: h.o.MaxBytes,
		BeforeWrite: func(total, held int64) error {
			if total <= 0 {
				return nil
			}
			need := ceil(tmpHeadroom*float64(total)) - held
			if free, err := h.o.Free(h.o.TmpDir); err == nil && free < need {
				room = fmt.Sprintf("the Bridge's data folder has %s free and needs about %s for this book", size(free), size(need))
				return errNoRoom
			}
			need = ceil(transfer.DefaultHeadroom * float64(total))
			if free, err := h.o.Free(transfer.NearestDir(mount, rel)); err == nil && free < need {
				room = fmt.Sprintf("the library has %s free and needs about %s for this book", size(free), size(need))
				return errNoRoom
			}
			return nil
		},
		OnProgress: func(copied, total int64) {
			h.report(rep, jobs.Progress{Step: StepDownload, Message: "Downloading from Audible", Done: int(copied), Total: int(max(total, 0))})
		},
	}
	for attempt := 1; ; attempt++ {
		opts.ExpectedSize = lic.Size
		err := h.o.Accounts.Download(ctx, account, lic.URL, part, opts)
		status := audible.HTTPStatus(err)
		switch {
		case err == nil:
			return lic, nil
		case ctx.Err() != nil:
			return audible.License{}, ctx.Err()
		case errors.Is(err, errNoRoom):
			return audible.License{}, fail(CodeDiskFull, "Not enough free space to back the book up: %s.", room)
		case fsutil.IsNoSpace(err):
			return audible.License{}, fail(CodeDiskFull, "The Bridge's data folder ran out of space during the download.")
		case errors.Is(err, audible.ErrTooLarge):
			return audible.License{}, fmt.Errorf("the book's download is larger than the Bridge's cap (%s)", size(h.capBytes()))
		case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusGone:
			// The presigned URL expired (a long wait, a restart): license again once.
			if relicensed {
				return audible.License{}, fail(CodeURLExpired, "Audible's download link expired again after a new license (HTTP %d): try again later.", status)
			}
			relicensed = true
			h.report(rep, jobs.Progress{Step: StepLicense, Message: "The download link expired: asking Audible for a new license"})
			if lic, err = h.license(ctx, account, asin); err != nil {
				return audible.License{}, err
			}
			h.report(rep, jobs.Progress{Step: StepDownload, Message: "Downloading from Audible"})
			attempt = 0
			continue
		case (status == 0 || status >= 500) && attempt < downloadAttempts:
			// A dropped connection or a server error: the partial resumes.
			h.o.Log.Info("audible: a download attempt failed; resuming", "account", account, "asin", asin, "attempt", attempt, "err", err)
			if werr := h.o.Wait(ctx, time.Duration(attempt*attempt)*2*time.Second); werr != nil {
				return audible.License{}, werr
			}
			continue
		case status != 0:
			return audible.License{}, fmt.Errorf("the download from Audible answered HTTP %d", status)
		}
		return audible.License{}, fmt.Errorf("downloading from Audible: %w", err)
	}
}

// capBytes is the download cap in force.
func (h *Importer) capBytes() int64 {
	if h.o.MaxBytes > 0 {
		return h.o.MaxBytes
	}
	return audible.DefaultMaxDownloadBytes
}

// rescanAttempts bounds the rescan's tries (the file is placed either way).
const rescanAttempts = 3

// rescan asks the server to index the placed file and returns the book's
// path, trying again a couple of times (a server busy or restarting).
func (h *Importer) rescan(ctx context.Context, srv ImportServer, libraryID int64, rel string) (string, error) {
	var err error
	for attempt := 1; attempt <= rescanAttempts; attempt++ {
		var book string
		if book, err = srv.Rescan(ctx, libraryID, rel); err == nil {
			if book == "" {
				book = path.Dir(rel)
			}
			return book, nil
		}
		if ctx.Err() != nil || attempt == rescanAttempts {
			break
		}
		if werr := h.o.Wait(ctx, time.Duration(attempt)*5*time.Second); werr != nil {
			return "", werr
		}
	}
	return "", err
}

// Destination is where the title goes in the library, library-relative and
// clean: the console's dest_path when set, else the account's template when
// set, else Auto (placement: series siblings' folder and shortcode, else
// Author/Title), with siblings and author folders from the library's books.
// A dest_path or template whose last segment ends in an audio extension names
// the file; otherwise it names the folder and the file is the title's
// ("<Title>.m4b"). An unusable result (a "..", an absolute path, a hidden
// name) is not_writable.
func Destination(p ImportParams, rec source.Record, books []link.LibraryBook) (string, error) {
	mode, tmpl := placement.ModeAuto, strings.TrimSpace(p.Template)
	if tmpl != "" {
		mode = placement.ModeTemplate
	}
	items, err := importjob.Plan([]source.Record{rec}, importjob.PlanOptions{Mode: mode, Template: tmpl, Books: books})
	if err != nil {
		return "", fail(CodeNotWritable, "The account's path template can't place the title: %v", err)
	}
	file := rec.FileName
	if file == "" {
		file = "Audiobook.m4b"
	}
	rel := items[0].RelPath
	if tmpl != "" && !isAudioName(rel) {
		rel = path.Join(rel, file)
	}
	if d := strings.TrimSpace(p.DestPath); d != "" {
		rel = d
		if !isAudioName(d) {
			rel = path.Join(d, file)
		}
	}
	clean, err := transfer.CleanRel(rel)
	if err != nil {
		return "", fail(CodeNotWritable, "The destination %q isn't a valid path in the library.", rel)
	}
	return clean, nil
}

// isAudioName reports whether a path's last segment names an audio file.
func isAudioName(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".m4b", ".m4a", ".mp4", ".mp3", ".aac":
		return true
	}
	return false
}

func ceil(v float64) int64 {
	if v >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(math.Ceil(v))
}

// size is a byte count for a sentence (MB/GB, one decimal).
func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// SweepTmp is the start-up sweep of the import's temp folder (decision 6): a
// partial download (*.aaxc.part and its .state) younger than PartialMaxAge
// stays for its job to resume, as does a placement marker (*.placed.json) as
// young, for a retry to finish an installed book; everything else goes
// (decrypted copies, older partials and markers, a state without its partial,
// anything a crash left).
func SweepTmp(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		marker, part := strings.HasSuffix(e.Name(), placedSuffix), strings.HasSuffix(e.Name(), ".aaxc.part")
		if !marker && !part {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) < PartialMaxAge {
			keep[e.Name()] = true
			if part {
				keep[filepath.Base(audible.StatePath(e.Name()))] = true
			}
		}
	}
	var errs []error
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
