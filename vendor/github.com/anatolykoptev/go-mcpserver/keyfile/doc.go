// Package keyfile verifies bearer tokens against a hashed, per-key key file
// and restricts each key to an explicit list of tools.
//
// Experimental: the API may change in a minor release.
//
// WARNING: [Store.Verifier] and [Store.ToolFilter] only work as a pair. A
// verifier without the filter authenticates a key but scopes nothing. Use
// [Store.Install] (or [Store.BearerAuth] plus [Store.Middleware]) so they cannot
// be separated.
//
// # File format
//
//	version: 1
//	keys:
//	  - name: reporter                  # ^[a-z][a-z0-9-]{1,31}$, unique
//	    sha256: <64 lowercase hex of the raw token>
//	    expires: 2027-01-12T00:00:00Z   # mandatory, RFC 3339, UTC ("Z") only
//	    tools: [tool_a, tool_b]         # must all be registered; "*" is not accepted
//	    attrs:                          # optional, consumer-defined, never interpreted
//	      role: reader
//	end: true                           # mandatory last line
//
// The last non-blank line must be exactly "end: true". A write cut short at any
// byte therefore invalidates the file instead of silently dropping the entries
// after the cut. Replace the file atomically (write a temporary file with mode
// 0600, then rename it over the key file).
//
// The file is decoded strictly: a YAML error, an unknown field, a version other
// than the integer 1 or a missing end marker makes the whole file invalid, and
// an invalid file means deny-all. An individual entry is dropped on its own,
// and logged at ERROR, when it has a bad or reserved name, a bad hash, a
// missing, non-UTC or past expiry, an empty or wildcard tool list, an
// unregistered tool, or a malformed attrs map (keys ^[a-z][a-z0-9_]{0,31}$,
// string values of at most 256 bytes, at most 32 pairs), or when the consumer's
// Options.ValidateAttrs rejects it. Every copy of a duplicated name or a
// duplicated hash is dropped, since neither identity can be resolved. A
// missing file is an empty key set.
//
// Attrs absent from an entry are empty, not an error. A consumer that depends
// on restrictive attrs should require them in Options.ValidateAttrs.
//
// # File safety
//
// The key file is opened with O_NOFOLLOW and checked on the open descriptor: it
// must be a regular file of at most 1 MiB, not group- or world-writable, and
// owned by the running user or root; its directory must not be group- or
// world-writable unless it has the sticky bit. Anything else is deny-all with
// an ERROR (unix only; elsewhere only the regular-file check applies).
//
// These checks have deployment consequences:
//
//   - Only the final path component is checked for a symlink, and a symlink
//     there is refused rather than followed. A key file mounted as a symlink
//     (Kubernetes Secret and ConfigMap volumes, Nix store links such as those
//     managed by sops-nix) is therefore refused. Point Path at a regular file,
//     for example a copy written by an init step.
//   - The owner must be root or the running user. A process running as root
//     inside a container passes the check for a root-owned file, but a file
//     that is bind-mounted from the host and owned by another host UID is
//     refused, because that UID is neither root nor the container's user.
//     Chown the file on the host, or run the process as the file's owner.
//   - The parent directory must not be group- or world-writable (sticky
//     excepted). A directory created as 0775 under a umask of 002 is refused;
//     chmod it to 0755 or 0700.
//
// A refusal keeps the file in deny-all and is logged at ERROR once per file
// version. The file is not re-read until its inode, mtime, ctime or size
// changes, so a chmod or chown of the file takes effect on the next stat. A
// refused parent directory is the exception: it is re-checked on every stat so
// that fixing the directory is picked up without touching the file.
//
// # Reload
//
// Each verification stats the file (at most once per second; a backward step of
// the wall clock does not delay the next stat) and re-reads it when its inode,
// mtime, ctime or size changed. A failed read or parse keeps
// the previous key set for one 50 ms retry window, then installs deny-all
// until the file next changes. There is deliberately no keep-last-good: a typo
// in an edit that revokes a key must not leave that key alive. Reloads are
// single-flight and never block other requests; a panic during a reload (for
// example in consumer code) results in deny-all, never a wedged store.
//
// # Tokens
//
// Tokens look like "<prefix>1_<43 base62><6 base62 CRC-32>" and are produced by
// [Mint]. The checksum lets a malformed token be rejected without a lookup and
// lets secret scanners match tokens precisely. Only SHA-256 digests are stored.
// No token, token prefix or token hash is ever logged.
//
// # Enforcement
//
// [Store.Verifier] snapshots the key's tool list and attrs into the returned
// TokenInfo and [Store.ToolFilter] reads only that snapshot, so a reload
// between verification and filtering cannot change the decision. A missing
// TokenInfo is denied.
//
// [Store.Middleware] (added by Install) also denies every MCP method other than
// initialize, ping, server/discover, notifications/*, tools/list and
// tools/call (resources, prompts, completion, any other tools/* method, ...),
// because the key file scopes tools only. Options.AllowNonToolMethods turns
// that off.
//
// Tool handlers read the verified attrs with [Attrs](ctx) or, equivalently,
// [AttrsOf](req.Extra.TokenInfo). Both give the current request's identity.
// In stateful mode the identity the SDK stores on the session is the one that
// opened it, which can be stale after a reload; do not use
// auth.TokenInfoFromContext for authorization decisions there. In stateless
// mode every request carries its own identity either way.
//
// The REST bridge (Config.RESTBridge) is not supported with this package: its
// calls run over an in-memory session that carries no TokenInfo, so the filter
// denies every bridged call (fail closed), and Install refuses a Config that
// already enables it.
package keyfile
