package download

// Result is the structured outcome of one Run, surfaced to the cmd layer for
// machine-readable (--json) rendering. It is populated ONLY when Options.Result
// is non-nil; the human path never reads or writes it, so production callers and
// every existing test (which leave Result nil) are byte-for-byte unaffected. cmd
// adds Success/Error at render time (Run communicates failure via its returned
// error, not via Result), so those fields are NOT here.
//
// All json tags are snake_case; the struct marshals to a single compact line.
// Size/Bytes/Resumed/Skipped intentionally have NO omitempty (a 0/-1/false is
// meaningful and the key must always be present for consumers); Sha256/Source
// carry omitempty.
type Result struct {
	URL     string `json:"url"`              // primary/requested URL (opts.URL)
	Output  string `json:"output"`           // resolved final path; "" in stdout mode (unreachable under --json, see cmd reject)
	Bytes   int64  `json:"bytes"`            // bytes written to the destination
	Size    int64  `json:"size"`             // probed total size; -1 when unknown
	Sha256  string `json:"sha256,omitempty"` // verified lowercase hex; set ONLY when --checksum was given and verified
	Resumed bool   `json:"resumed"`          // a matching sidecar was honored
	Skipped bool   `json:"skipped"`          // skip-if-complete short-circuit hit
	Source  string `json:"source,omitempty"` // URL that actually served the bytes (may differ under failover); "" if none served
}
