package keyfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"
)

// fileSig identifies a file version. It is read before the content, so a write
// that lands mid-read changes the signature and triggers another reload. The
// zero value never matches a real file and so forces a re-read.
type fileSig struct {
	ino     uint64
	mtimeNS int64
	ctimeNS int64
	size    int64
	missing bool
}

type snapshot struct {
	keys    map[[digestLen]byte]*keyEntry
	sig     fileSig
	fileSHA string
	ok      bool // false = deny-all after a failed load
}

func denyAll(sig fileSig, fileSHA string, ok bool) *snapshot {
	return &snapshot{keys: map[[digestLen]byte]*keyEntry{}, sig: sig, fileSHA: fileSHA, ok: ok}
}

// statDue applies the stat cache: at most one stat per statTTL.
func (s *Store) statDue() bool {
	if s.statTTL <= 0 {
		return true
	}
	now := s.now().UnixNano()
	last := s.lastStatNS.Load()
	return elapsedDue(now, last, int64(s.statTTL)) && s.lastStatNS.CompareAndSwap(last, now)
}

// elapsedDue reports whether at least every nanoseconds passed since last. A
// negative delta (the wall clock stepped backwards) counts as due: otherwise
// the gate stays shut until the clock catches up, and a revoked key would keep
// authenticating meanwhile.
func elapsedDue(now, last, every int64) bool {
	d := now - last
	return d < 0 || d >= every
}

// warnDue reports whether a rate-limited log line may be emitted now and, if
// so, records it.
func (s *Store) warnDue(last *atomic.Int64) bool {
	now := s.now().UnixNano()
	prev := last.Load()
	return elapsedDue(now, prev, int64(rejectLogEvery)) && last.CompareAndSwap(prev, now)
}

// statSig stats the path itself (not a symlink target).
func (s *Store) statSig() (fileSig, error) {
	fi, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSig{missing: true}, nil
	}
	if err != nil {
		return fileSig{}, err
	}
	ino, ctime := sigMeta(fi)
	return fileSig{ino: ino, mtimeNS: fi.ModTime().UnixNano(), ctimeNS: ctime, size: fi.Size()}, nil
}

// maybeReload re-reads the file when its signature changed. It is single-flight
// and never waits for another reload: while one is in progress (including its
// retry window) other requests keep using the current snapshot. The first read
// happens inline (it is a small file); only the retry runs in the background.
func (s *Store) maybeReload() {
	if !s.statDue() {
		return
	}
	sig, err := s.statSig()
	if err != nil {
		s.statFailed()
		return
	}
	if sig == s.snap.Load().sig {
		return
	}
	if !s.reloading.CompareAndSwap(false, true) {
		return
	}
	s.reloads.Add(1)
	handedOff := false
	defer func() {
		if !handedOff {
			s.reloading.Store(false)
		}
	}()
	defer s.recoverDenyAll()
	snap, err := s.safeAttempt()
	if err == nil {
		s.finish(snap, nil)
		return
	}
	s.warnRetry(err)
	handedOff = true
	go func() {
		defer s.reloading.Store(false)
		defer s.recoverDenyAll()
		s.sleep(retryDelay)
		s.finish(s.safeAttempt())
	}()
}

// warnRetry logs the "retrying once" line, at most once a minute so a file that
// keeps changing into an unusable state cannot flood the log.
func (s *Store) warnRetry(err error) {
	if !s.warnDue(&s.lastRetryWarn) {
		return
	}
	s.logger.Warn("keyfile: key file unusable, retrying once",
		slog.String("problem", err.Error()), slog.Duration("retry_in", retryDelay))
}

// statFailed handles a stat error other than "does not exist": deny all, once,
// with no reload cycle and at most one log line per minute while it persists.
func (s *Store) statFailed() {
	if cur := s.snap.Load(); cur.ok || cur.sig != (fileSig{}) {
		s.snap.Store(denyAll(fileSig{}, "", false))
	}
	if s.warnDue(&s.lastIOWarn) {
		s.logger.Error("keyfile: cannot stat key file, denying all keys")
	}
}

// reloadBlocking is the Open-time variant: first read, one retry, then install.
// The caller holds s.reloading.
func (s *Store) reloadBlocking() {
	defer s.reloading.Store(false)
	defer s.recoverDenyAll()
	snap, err := s.safeAttempt()
	if err != nil {
		s.warnRetry(err)
		s.sleep(retryDelay)
		snap, err = s.safeAttempt()
	}
	s.finish(snap, err)
}

// recoverDenyAll is a backstop for a panic anywhere in a reload: the process
// survives and the store denies everything. The panic value is not logged.
func (s *Store) recoverDenyAll() {
	if r := recover(); r != nil {
		s.snap.Store(denyAll(fileSig{}, "", false))
		s.logger.Error("keyfile: internal error during reload, denying all keys")
	}
}

// safeAttempt runs attempt and converts a panic into a load error.
func (s *Store) safeAttempt() (snap *snapshot, err error) {
	defer func() {
		if r := recover(); r != nil {
			snap, err = denyAll(fileSig{}, "", false), &loadError{"internal error"}
		}
	}()
	return s.attempt()
}

// finish installs the result of a load: the new key set, or deny-all.
func (s *Store) finish(snap *snapshot, err error) {
	if err != nil {
		// A zero signature means the failure may be transient and the file
		// will be re-read; keep that from flooding the log.
		if snap.sig != (fileSig{}) || s.warnDue(&s.lastLoadWarn) {
			s.logger.Error("keyfile: key file invalid, denying all keys",
				slog.String("problem", err.Error()))
		}
		s.snap.Store(denyAll(snap.sig, snap.fileSHA, false))
		return
	}
	s.snap.Store(snap)
	s.logger.Info("keyfile: key file loaded", slog.Int("keys", len(snap.keys)))
}

// attempt stats, reads and parses the file once. On a content failure or a
// refusal (symlink, wrong type, writable, untrusted owner, too large) the
// returned snapshot carries the signature of what was seen, so that version is
// not re-read until the file changes; the ctime in the signature picks up a
// chmod or chown that fixes it. Two cases keep the zero signature so the file
// is checked again on the next pass: an I/O error (it may be transient) and a
// refused parent directory (a fix there leaves the file's own signature
// unchanged).
func (s *Store) attempt() (*snapshot, error) {
	sig, err := s.statSig()
	if err != nil {
		return denyAll(fileSig{}, "", false), &loadError{"stat failed"}
	}
	if sig.missing {
		return denyAll(sig, "", true), nil
	}
	data, err := s.readFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return denyAll(fileSig{missing: true}, "", true), nil
	}
	if err != nil {
		var le *loadError
		if errors.As(err, &le) {
			if le.kind == kindParentWritable {
				return denyAll(fileSig{}, "", false), le
			}
			return denyAll(sig, "", false), le
		}
		return denyAll(fileSig{}, "", false), &loadError{"read failed"}
	}
	sum := sha256.Sum256(data)
	fileSHA := hex.EncodeToString(sum[:])
	entries, err := s.parse(data)
	if err != nil {
		return denyAll(sig, fileSHA, false), err
	}
	keys := make(map[[digestLen]byte]*keyEntry, len(entries))
	for _, e := range entries {
		keys[e.digest] = e
	}
	return &snapshot{keys: keys, sig: sig, fileSHA: fileSHA, ok: true}, nil
}

// readChecked opens the key file without following symlinks, validates the
// open descriptor and its directory, and reads at most maxFileSize+1 bytes.
func (s *Store) readChecked(path string) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if reason := s.checkFile(fi); reason != "" {
		return nil, &loadError{reason}
	}
	reason, err := checkParent(path)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, &loadError{reason}
	}
	if fi.Size() > maxFileSize {
		return nil, &loadError{kindTooLarge}
	}
	return io.ReadAll(io.LimitReader(f, maxFileSize+1))
}
