package audiblejob

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
	"github.com/kodestar/audiosilo-bridge/internal/audibleacct"
	"github.com/kodestar/audiosilo-bridge/internal/jobs"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/source"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// The secrets a license carries in these tests: none may reach a log, a
// progress, a result, an error or a file the job leaves.
const (
	secretKey = "5ec7e75ec7e75ec7e75ec7e75ec7e7aa"
	secretIV  = "1b1b1b1b1b1b1b1b1b1b1b1b1b1b1bbb"
	secretSig = "SECRETSIG"
)

type stubImportAccounts struct {
	mu       sync.Mutex
	acct     store.AudibleAccount
	acctErr  error
	title    source.Record
	titleErr error
	licErr   []error // popped per License call
	licenses int
	body     []byte
	// failNext: each Download pops one; a non-nil error writes half of what is
	// left first (a dropped connection).
	failNext []error
	// download replaces the stub's Download when set.
	download  func(ctx context.Context, url, dest string, opts audible.DownloadOptions) error
	downloads int
}

func (s *stubImportAccounts) Account(_ context.Context, id string) (store.AudibleAccount, error) {
	if s.acctErr != nil {
		return store.AudibleAccount{}, s.acctErr
	}
	if id != s.acct.ID {
		return store.AudibleAccount{}, audibleacct.ErrNotFound
	}
	return s.acct, nil
}

func (s *stubImportAccounts) Title(context.Context, string, string) (source.Record, error) {
	return s.title, s.titleErr
}

func (s *stubImportAccounts) License(context.Context, string, string) (audible.License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.licenses++
	if len(s.licErr) > 0 {
		err := s.licErr[0]
		s.licErr = s.licErr[1:]
		if err != nil {
			return audible.License{}, err
		}
	}
	return audible.License{URL: fmt.Sprintf("https://cdn.example/f%d?Signature=%s", s.licenses, secretSig), Key: secretKey, IV: secretIV,
		Size: int64(len(s.body))}, nil
}

func (s *stubImportAccounts) Download(ctx context.Context, _ string, url, dest string, opts audible.DownloadOptions) error {
	s.mu.Lock()
	s.downloads++
	s.mu.Unlock()
	if s.download != nil {
		return s.download(ctx, url, dest, opts)
	}
	var held int64
	if st, err := os.Stat(dest); err == nil {
		held = st.Size()
	}
	if opts.BeforeWrite != nil {
		if err := opts.BeforeWrite(int64(len(s.body)), held); err != nil {
			return err
		}
	}
	rest := s.body[held:]
	var fail error
	if len(s.failNext) > 0 {
		fail, s.failNext = s.failNext[0], s.failNext[1:]
	}
	if fail != nil {
		rest = rest[:len(rest)/2]
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, _ = f.Write(rest)
	_ = f.Close()
	if opts.OnProgress != nil {
		opts.OnProgress(held+int64(len(rest)), int64(len(s.body)))
	}
	return fail
}

type stubImportServer struct {
	mu        sync.Mutex
	books     []link.LibraryBook
	rescanErr []error
	rescanned []string
	enriched  []string
	enrichErr error
}

func (s *stubImportServer) LibraryBooks(_ context.Context, _ int64, _ string, _ int) (*link.LibraryBooks, error) {
	return &link.LibraryBooks{Books: s.books}, nil
}

func (s *stubImportServer) Rescan(_ context.Context, _ int64, p string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rescanned = append(s.rescanned, p)
	if len(s.rescanErr) > 0 {
		err := s.rescanErr[0]
		s.rescanErr = s.rescanErr[1:]
		if err != nil {
			return "", err
		}
	}
	return path.Dir(p), nil
}

func (s *stubImportServer) Enrich(_ context.Context, _ int64, p, asin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enriched = append(s.enriched, p+"="+asin)
	return s.enrichErr
}

type stubLibs struct {
	lib    link.Library
	status link.LibraryStatus
	known  bool
}

func (s *stubLibs) Library(id int64) (link.Library, bool) { return s.lib, s.known && id == s.lib.ID }
func (s *stubLibs) Status(id int64) (link.LibraryStatus, bool) {
	return s.status, s.known && id == s.lib.ID && s.status.MountPath != ""
}

type importEnv struct {
	root, tmp string
	accts     *stubImportAccounts
	srv       *stubImportServer
	libs      *stubLibs
	logs      *bytes.Buffer
	opts      ImportOptions
	steps     []string
	progress  []jobs.Progress
	decrypted []audible.License
	waits     []time.Duration
	mu        sync.Mutex
}

func newImportEnv(t *testing.T) *importEnv {
	t.Helper()
	e := &importEnv{root: t.TempDir(), tmp: filepath.Join(t.TempDir(), "audible", "tmp"), logs: &bytes.Buffer{}}
	e.accts = &stubImportAccounts{
		acct:  store.AudibleAccount{ID: "acct0001", LibraryID: 1, ServerID: "srv1"},
		title: rec("B0FAKE0002", "The Copper Grove", "Mara Quill", "Orchard Cycle", 2),
		body:  bytes.Repeat([]byte("audiobook-bytes-"), 4096),
	}
	e.srv = &stubImportServer{books: goldenBooks()}
	e.libs = &stubLibs{known: true, lib: link.Library{ID: 1, Name: "Books", Root: "/srv/books"},
		status: link.LibraryStatus{LibraryID: 1, MountPath: e.root, Mapped: true, Writable: true}}
	e.opts = ImportOptions{
		Accounts: e.accts, ServerID: func() string { return "srv1" }, Libraries: e.libs,
		Server: func() ImportServer { return e.srv },
		Mount:  func(int64, string) (string, bool) { return e.root, true },
		TmpDir: e.tmp, Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Decrypt: func(_ context.Context, _, in, out string, lic audible.License) error {
			e.mu.Lock()
			e.decrypted = append(e.decrypted, lic)
			e.mu.Unlock()
			b, err := os.ReadFile(in)
			if err != nil {
				return err
			}
			return os.WriteFile(out, b, 0o600)
		},
		Free: func(string) (int64, error) { return 1 << 40, nil },
		Wait: func(ctx context.Context, d time.Duration) error {
			e.mu.Lock()
			e.waits = append(e.waits, d)
			e.mu.Unlock()
			return ctx.Err()
		},
		onProgress: func(p jobs.Progress) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.progress = append(e.progress, p)
			if len(e.steps) == 0 || e.steps[len(e.steps)-1] != p.Step {
				e.steps = append(e.steps, p.Step)
			}
		},
	}
	return e
}

func (e *importEnv) params(f func(*ImportParams)) json.RawMessage {
	p := ImportParams{Account: "acct0001", ASIN: "B0FAKE0002", LibraryID: 1}
	if f != nil {
		f(&p)
	}
	b, _ := json.Marshal(p)
	return b
}

func (e *importEnv) run(ctx context.Context, params json.RawMessage) (any, error) {
	return NewImport(e.opts).Run(ctx, jobs.Job{ID: 7, Kind: ImportKind, Params: params, Attempt: 1}, jobs.LocalReporter())
}

// noSecrets fails when a license's key, iv or URL signature shows anywhere
// the job leaves words: its logs, progress, result, error, or a file in the
// temp folder or the library.
func (e *importEnv) noSecrets(t *testing.T, result any, err error) {
	t.Helper()
	var texts []string
	texts = append(texts, e.logs.String())
	if b, merr := json.Marshal(result); merr == nil {
		texts = append(texts, string(b))
	}
	if err != nil {
		texts = append(texts, err.Error())
	}
	pb, _ := json.Marshal(e.progress)
	texts = append(texts, string(pb))
	for _, dir := range []string{e.tmp, e.root} {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
			if werr == nil && d.Type().IsRegular() {
				b, _ := os.ReadFile(p)
				texts = append(texts, string(b))
			}
			return nil
		})
	}
	for _, s := range texts {
		for _, secret := range []string{secretKey, secretIV, secretSig} {
			if strings.Contains(strings.ToLower(s), strings.ToLower(secret)) {
				t.Fatalf("a secret leaked: %.300s", s)
			}
		}
	}
}

// tmpEmpty fails when the import left anything in its temp folder.
func (e *importEnv) tmpEmpty(t *testing.T) {
	t.Helper()
	entries, _ := os.ReadDir(e.tmp)
	if len(entries) != 0 {
		t.Fatalf("temp files left: %v", entries)
	}
}

// libraryFiles lists the library's files (relative).
func (e *importEnv) libraryFiles() []string {
	var out []string
	_ = filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(e.root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

const copperGrove = "Mara Quill/Orchard Cycle/OC02 - The Copper Grove/The Copper Grove.m4b"

func TestImportBacksATitleUp(t *testing.T) {
	e := newImportEnv(t)
	res, err := e.run(context.Background(), e.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.(*ImportResult)
	if !ok {
		t.Fatalf("result = %#v", res)
	}
	size := int64(len(e.accts.body))
	want := ImportResult{LibraryID: 1, Path: path.Dir(copperGrove), Files: []ImportFile{{Path: copperGrove, Size: size}}, Bytes: size, Enriched: true}
	if b1, b2 := mustJSON(t, got), mustJSON(t, want); b1 != b2 {
		t.Fatalf("result = %s\nwant %s", b1, b2)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(copperGrove))); !bytes.Equal(b, e.accts.body) {
		t.Fatal("the library's file isn't the book")
	}
	if strings.Join(e.steps, ",") != "license,download,decrypt,place,rescan,enrich" {
		t.Fatalf("steps = %v", e.steps)
	}
	if len(e.srv.rescanned) != 1 || e.srv.rescanned[0] != copperGrove || len(e.srv.enriched) != 1 ||
		e.srv.enriched[0] != path.Dir(copperGrove)+"=B0FAKE0002" {
		t.Fatalf("rescanned %v, enriched %v", e.srv.rescanned, e.srv.enriched)
	}
	if len(e.decrypted) != 1 || e.decrypted[0].Key != secretKey || e.decrypted[0].IV != secretIV {
		t.Fatal("decrypt didn't get the license's key and iv")
	}
	e.tmpEmpty(t)
	e.noSecrets(t, res, err)

	// The result's wire shape (the server reads it).
	var wire map[string]any
	_ = json.Unmarshal([]byte(mustJSON(t, got)), &wire)
	for _, k := range []string{"library_id", "path", "files", "bytes", "enriched"} {
		if _, ok := wire[k]; !ok {
			t.Fatalf("result lacks %q: %v", k, wire)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A server that couldn't take the ASIN (another one is set) is no failure.
func TestImportEnrichRefusedIsNotAFailure(t *testing.T) {
	e := newImportEnv(t)
	e.srv.enrichErr = link.ErrASINSet
	res, err := e.run(context.Background(), e.params(nil))
	if err != nil || res.(*ImportResult).Enriched {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

// Each refusal answers its code, leaves nothing in the library or the temp
// folder, and says nothing secret.
func TestImportCodes(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(e *importEnv)
		params func(*ImportParams)
		code   string
	}{
		{name: "unknown account", setup: func(e *importEnv) { e.accts.acctErr = audibleacct.ErrNotFound }, code: CodeUnknownAccount},
		{name: "another server's", setup: func(e *importEnv) { e.accts.acct.ServerID = "srv0" }, code: CodeUnknownAccount},
		{name: "not the account's library", params: func(p *ImportParams) { p.LibraryID = 2 }, code: CodeNotWritable},
		{name: "not writable", setup: func(e *importEnv) { e.libs.status.Writable = false }, code: CodeNotWritable},
		{name: "not mapped", setup: func(e *importEnv) { e.libs.status.Mapped = false }, code: CodeNotWritable},
		{name: "a podcast library", setup: func(e *importEnv) { e.libs.lib.Kind = "podcasts" }, code: CodeNotWritable},
		{name: "unlisted library", setup: func(e *importEnv) { e.libs.known = false }, code: CodeNotWritable},
		{name: "preorder", setup: func(e *importEnv) {
			e.accts.title.Preorder, e.accts.title.UnavailableReason = true, "pre-order"
		}, code: CodeNotReleased},
		{name: "no rights", setup: func(e *importEnv) { e.accts.title.UnavailableReason = "no download rights" }, code: CodeNoRights},
		{name: "not in the library", setup: func(e *importEnv) { e.accts.titleErr = audible.ErrNotInLibrary }, code: CodeNoRights},
		{name: "sign-in refused", setup: func(e *importEnv) { e.accts.titleErr = audibleacct.ErrRefused }, code: CodeRefused},
		{name: "license: not released", setup: func(e *importEnv) {
			e.accts.licErr = []error{fmt.Errorf("%w: B0FAKE0002 (Release NotReleased)", audible.ErrNotReleased)}
		}, code: CodeNotReleased},
		{name: "license: no rights", setup: func(e *importEnv) {
			e.accts.licErr = []error{fmt.Errorf("%w: B0FAKE0002 (Ownership)", audible.ErrNoRights)}
		}, code: CodeNoRights},
		{name: "license: denied", setup: func(e *importEnv) {
			e.accts.licErr = []error{fmt.Errorf("%w: B0FAKE0002 (Throttled)", audible.ErrDenied)}
		}, code: CodeRefused},
		{name: "license: refused", setup: func(e *importEnv) { e.accts.licErr = []error{audibleacct.ErrRefused} }, code: CodeRefused},
		{name: "exists", setup: func(e *importEnv) {
			p := filepath.Join(e.root, filepath.FromSlash(copperGrove))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("an administrator's own copy"), 0o644)
		}, code: CodeExists},
		{name: "dest escapes", params: func(p *ImportParams) { p.DestPath = "../../etc" }, code: CodeNotWritable},
		{name: "temp space short", setup: func(e *importEnv) {
			e.opts.Free = func(dir string) (int64, error) {
				if dir == e.tmp {
					return int64(len(e.accts.body)), nil // under 2.2x
				}
				return 1 << 40, nil
			}
		}, code: CodeDiskFull},
		{name: "library space short", setup: func(e *importEnv) {
			e.opts.Free = func(dir string) (int64, error) {
				if dir == e.tmp {
					return 1 << 40, nil
				}
				return int64(len(e.accts.body)), nil // under 1.1x
			}
		}, code: CodeDiskFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newImportEnv(t)
			if c.setup != nil {
				c.setup(e)
			}
			res, err := e.run(context.Background(), e.params(c.params))
			var f *Failure
			if !errors.As(err, &f) || f.Code != c.code || res != any(f) {
				t.Fatalf("res %#v, err %v; want code %s", res, err, c.code)
			}
			files := e.libraryFiles()
			if c.code == CodeExists {
				if len(files) != 1 {
					t.Fatalf("library = %v", files)
				}
				if b, _ := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(copperGrove))); string(b) != "an administrator's own copy" {
					t.Fatal("the existing file was changed")
				}
			} else if len(files) != 0 {
				t.Fatalf("a refused import wrote %v", files)
			}
			e.tmpEmpty(t)
			e.noSecrets(t, res, err)
		})
	}
}

// Bad params fail before anything is asked of Audible.
func TestImportParams(t *testing.T) {
	for _, raw := range []string{`{}`, `{"account":"BAD","asin":"B0FAKE0002","library_id":1}`,
		`{"account":"acct0001","asin":"nope","library_id":1}`, `{"account":"acct0001","asin":"B0FAKE0002"}`, `[]`} {
		if _, err := ParseImportParams(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
	// Unknown fields are the server's to add; the ASIN is read upper-case.
	p, err := ParseImportParams(json.RawMessage(`{"account":"acct0001","asin":"b0fake0002","library_id":1,"later":true}`))
	if err != nil || p.ASIN != "B0FAKE0002" {
		t.Fatalf("p = %+v, %v", p, err)
	}
	e := newImportEnv(t)
	lib := int64(9)
	if _, err := NewImport(e.opts).Run(context.Background(), jobs.Job{Kind: ImportKind, LibraryID: &lib, Params: e.params(nil)}, jobs.LocalReporter()); err == nil {
		t.Fatal("a job library other than the params' ran")
	}
	if e.accts.licenses != 0 {
		t.Fatal("bad params asked Audible for a license")
	}
}

// A dropped connection resumes from the partial within the job, after a
// wait; the file comes out whole.
func TestImportResumesAfterADroppedConnection(t *testing.T) {
	e := newImportEnv(t)
	e.accts.failNext = []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}
	res, err := e.run(context.Background(), e.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if e.accts.downloads != 3 || len(e.waits) != 2 || e.accts.licenses != 1 {
		t.Fatalf("downloads %d, waits %v, licenses %d", e.accts.downloads, e.waits, e.accts.licenses)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(copperGrove))); !bytes.Equal(b, e.accts.body) {
		t.Fatal("the resumed book isn't whole")
	}
	e.noSecrets(t, res, err)
}

// A connection that keeps dropping fails the job, and its partial goes.
func TestImportGivesUpAfterThreeDrops(t *testing.T) {
	e := newImportEnv(t)
	e.accts.failNext = []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}
	if _, err := e.run(context.Background(), e.params(nil)); err == nil {
		t.Fatal("three drops in a row must fail")
	}
	e.tmpEmpty(t)
	if len(e.libraryFiles()) != 0 {
		t.Fatal("a failed import wrote to the library")
	}
}

// signedDownloads routes the stub's Download through a real audible client
// against srv, so the job sees Audible's real errors (a 403 on an expired URL).
func signedDownloads(t *testing.T, e *importEnv, srv *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	cl, err := audible.NewClient(audible.Credentials{ADPToken: "ADP", DevicePrivateKey: keyPEM, DeviceType: "A2CZJZGLK2JJVM",
		DeviceSerial: "SER", CustomerID: "CUST", Marketplace: "us"}, audible.WithEndpoints(audible.Endpoints{API: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	e.accts.download = func(ctx context.Context, url, dest string, opts audible.DownloadOptions) error {
		url = srv.URL + "/" + path.Base(strings.SplitN(url, "?", 2)[0]) + "?Signature=" + secretSig
		return cl.Download(ctx, url, dest, opts)
	}
}

// An expired download URL gets one new license; the new URL works.
func TestImportRelicensesAnExpiredURLOnce(t *testing.T) {
	e := newImportEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/f1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "f", time.Unix(0, 0), bytes.NewReader(e.accts.body))
	}))
	defer srv.Close()
	signedDownloads(t, e, srv)
	res, err := e.run(context.Background(), e.params(nil))
	if err != nil {
		t.Fatal(err)
	}
	if e.accts.licenses != 2 {
		t.Fatalf("licenses = %d, want 2", e.accts.licenses)
	}
	e.noSecrets(t, res, err)
}

// Expired again after the new license: offline_url_expired.
func TestImportURLExpiredTwice(t *testing.T) {
	e := newImportEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	signedDownloads(t, e, srv)
	res, err := e.run(context.Background(), e.params(nil))
	var f *Failure
	if !errors.As(err, &f) || f.Code != CodeURLExpired || e.accts.licenses != 2 {
		t.Fatalf("err = %v, licenses %d", err, e.accts.licenses)
	}
	e.tmpEmpty(t)
	e.noSecrets(t, res, err)
}

// blockingDownload writes half the book and waits for the job's end.
func blockingDownload(e *importEnv, started chan<- struct{}) func(ctx context.Context, url, dest string, opts audible.DownloadOptions) error {
	return func(ctx context.Context, _, dest string, _ audible.DownloadOptions) error {
		if err := os.WriteFile(dest, e.accts.body[:len(e.accts.body)/2], 0o600); err != nil {
			return err
		}
		_ = os.WriteFile(audible.StatePath(dest), []byte(`{"size":1}`), 0o600)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
}

// An admin's cancel mid-download leaves no temp file and no library file.
func TestImportCancelLeavesNothing(t *testing.T) {
	e := newImportEnv(t)
	started := make(chan struct{})
	e.accts.download = blockingDownload(e, started)
	ctx, cancel := jobs.CancelContext(context.Background())
	go func() { <-started; cancel() }()
	if _, err := e.run(ctx, e.params(nil)); err == nil {
		t.Fatal("a cancelled import succeeded")
	}
	e.tmpEmpty(t)
	if files := e.libraryFiles(); len(files) != 0 {
		t.Fatalf("library = %v", files)
	}
}

// A Bridge stopping mid-download keeps the partial (and its state) for the
// next start to resume (decision 6); the start-up sweep keeps it too.
func TestImportStopKeepsThePartial(t *testing.T) {
	e := newImportEnv(t)
	started := make(chan struct{})
	e.accts.download = blockingDownload(e, started)
	ctx, stop := context.WithCancel(context.Background())
	go func() { <-started; stop() }()
	if _, err := e.run(ctx, e.params(nil)); err == nil {
		t.Fatal("a stopped import succeeded")
	}
	part := filepath.Join(e.tmp, "acct0001-B0FAKE0002.aaxc.part")
	if err := SweepTmp(e.tmp, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{part, audible.StatePath(part)} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
	}
	// The runner ending it without running it (a cancel before it resumed)
	// removes it.
	NewImport(e.opts).Abandon(context.Background(), jobs.Job{Kind: ImportKind, Params: e.params(nil)})
	e.tmpEmpty(t)
}

func TestSweepTmp(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("a-B0FAKE0001.aaxc.part", time.Hour)
	write("a-B0FAKE0001.aaxc.part.state", time.Hour)
	write("a-B0FAKE0002.aaxc.part", 8*24*time.Hour) // too old
	write("a-B0FAKE0002.aaxc.part.state", time.Hour)
	write("a-B0FAKE0003.aaxc.part.state", time.Hour) // no partial
	write("a-B0FAKE0001.m4b", time.Minute)           // a decrypted copy a crash left
	write("stray.txt", time.Minute)
	if err := os.MkdirAll(filepath.Join(dir, "audible-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SweepTmp(dir, now); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "a-B0FAKE0001.aaxc.part,a-B0FAKE0001.aaxc.part.state" {
		t.Fatalf("left %v", left)
	}
	if err := SweepTmp(filepath.Join(dir, "missing"), now); err != nil {
		t.Fatalf("a missing folder: %v", err)
	}
}

// One import per account at a time: a second waits for the first.
func TestImportOnePerAccount(t *testing.T) {
	e := newImportEnv(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	running, most := 0, 0
	e.accts.download = func(_ context.Context, _, dest string, _ audible.DownloadOptions) error {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return os.WriteFile(dest, e.accts.body, 0o600)
	}
	h := NewImport(e.opts)
	errs := make(chan error, 2)
	for _, asin := range []string{"B0FAKE0002", "B0FAKE0003"} {
		params := e.params(func(p *ImportParams) { p.ASIN = asin; p.DestPath = "Imports/" + asin })
		go func() {
			_, err := h.Run(context.Background(), jobs.Job{Kind: ImportKind, Params: params}, jobs.LocalReporter())
			errs <- err
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond) // the second would have started by now
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if most != 1 {
		t.Fatalf("%d imports of one account ran at once", most)
	}
}

// A rescan that fails once is tried again; one that keeps failing fails the
// job, saying the book is placed.
func TestImportRescanRetries(t *testing.T) {
	e := newImportEnv(t)
	e.srv.rescanErr = []error{errors.New("busy")}
	if _, err := e.run(context.Background(), e.params(nil)); err != nil {
		t.Fatal(err)
	}
	e2 := newImportEnv(t)
	e2.srv.rescanErr = []error{errors.New("busy"), errors.New("busy"), errors.New("busy")}
	_, err := e2.run(context.Background(), e2.params(nil))
	if err == nil || !strings.Contains(err.Error(), "is in the library") {
		t.Fatalf("err = %v", err)
	}
}
