export const meta = {
  name: 'refactor-implement',
  description: 'Implement the durable-resume v2: fill in each frozen-contract stub file + its test, in dependency waves, then build/test gate',
  phases: [
    { title: 'Wave1', detail: 'leaf files: probe, chunk, state, retry, progress, verify' },
    { title: 'Wave2', detail: 'integrators: pool, download.Run, cmd/root' },
    { title: 'Gate', detail: 'go build/vet/test/race + gofmt + fix loop' },
  ],
}

const REPO = '/Users/azhovan/GolandProjects/durable-resume'

// The frozen contract — every implementer must honor these signatures EXACTLY.
const CONTRACT = `
PROJECT: durable-resume, a Go CLI downloader (binary \`dr\`). Module github.com/azhovan/durable-resume. Go 1.22.
ONLY dependencies allowed: github.com/spf13/cobra, github.com/stretchr/testify, and Go stdlib. NO new deps. Stdlib-only TTY detection. http/https only (no ftp).

The repo ALREADY contains compiling stub files with the FROZEN, DOC-COMMENTED signatures (bodies are \`panic("not implemented")\`). You MUST read the stub file before editing and keep every exported AND unexported signature EXACTLY as written — other files depend on them. Do not rename, do not change parameter or return types, do not add/remove exported identifiers. You MAY add small private helpers within your file if needed.

ARCHITECTURE / DATA FLOW (download package is flat, one concern per file):
cmd -> download.Options -> download.Run(ctx, opts):
1. PROBE (probe.go): GET with "Range: bytes=0-0". 206 => parse Content-Range total => size known, acceptRanges=true. 200 => size from Content-Length, acceptRanges=false. 405/4xx-to-range or other => HEAD fallback (size from Content-Length, acceptRanges from "Accept-Ranges: bytes"). Capture ETag + Last-Modified. NEVER hard-fail on a streamable non-206; only transport errors propagate. size=-1 means unknown.
2. STRATEGY: remoteInfo.streamable() == (acceptRanges && size>0). If streamable => segmented; else => single sequential stream (no resume; skip size verify when size unknown).
3. RESUME STATE (state.go, segmented only): statePath = output + ".dr.json". If opts.Resume, LoadState (missing OR corrupt => (nil,nil) treated as fresh). If saved state exists AND st.Matches(remoteInfo) => reconstruct chunk plan via st.toChunks() with per-chunk done cursors; else discard + plan fresh. FRESH plan: open dest + Truncate(size) to pre-allocate. RESUME: open dest WITHOUT truncation.
4. PLAN (chunk.go): planChunks(size, concurrency) => disjoint contiguous inclusive [start,end] ranges using integer ceilDiv (NOT float). Clamp concurrency>=1 and when size<concurrency.
5. PROGRESS (progress.go): NewProgress(size,out,quiet); single render goroutine via Start(ctx); auto-suppress on quiet OR non-TTY. Seed() with completedBytes() on resume. onBytes callback feeds Progress.Add AND State.MarkProgress.
6. DOWNLOAD (pool.go + chunk.go): runSegmented = bounded worker pool (buffered semaphore channel + sync.WaitGroup + derived cancelable context). Each chunk goes through the retry wrapper; fetchChunk issues "Range: bytes=(start+done)-end", validates 206 (else ErrRangeNot206), streams resp.Body into dst via WriteAt at the absolute offset using a FIXED copyBufferSize (32KiB) buffer. After each WriteAt: advance ch.done, call onBytes(n). Pool flushes st.Save periodically (stateFlushInterval) and once on exit. Disjoint WriteAt offsets => no file lock needed. First fatal (non-retryable) error cancels siblings, returned wrapped with ErrChunkFailed.
7. INTERRUPT: ctx cancellation flows through; pool returns ctx.Err(); Run flushes st.Save and returns WITHOUT removing sidecar/partial file.
8. VERIFY+CLEANUP (verify.go, success only): verifySize(dst,size) (skip if size<=0) then verifyChecksum(path, opts.Checksum) (skip if Empty). Success => st.Remove(statePath). Any failure/cancel => retain sidecar + partial file.
Single-stream path: runSingle truncates dst to 0 and io.Copy's body in with the same fixed buffer, calling onBytes; NO state file created.

CROSS-CUTTING RULES (mandatory):
- Wrap returned errors with fmt.Errorf("...: %w", err) preserving the sentinel chain. Tests use errors.Is against the exported sentinels. Pool wraps chunk failures with ErrChunkFailed.
- Every blocking op takes ctx first and honors ctx.Done(); HTTP via http.NewRequestWithContext; backoff sleeps select on ctx.Done().
- Concurrent writes: os.File.WriteAt at disjoint absolute offsets ONLY. Never seek+write, never a shared cursor, never a mutex around WriteAt. State.mu guards only State fields.
- Copy loops use a FIXED copyBufferSize buffer. Never a buffer sized to a chunk/file.
- Truncate(size) ONLY on fresh segmented; resume opens without truncation; single-stream truncates to 0.
- Sidecar written atomically: marshal to <path>.tmp, (optionally) fsync, os.Rename to <path>. LoadState: missing/corrupt => (nil,nil). State.Remove only on success.
- Progress updated ONLY via onBytes->Progress.Add (atomic). Exactly one render goroutine, sole writer to Out. No counter resets.
- gofmt AND gofumpt clean (CI runs gofumpt). Doc comments on exported identifiers (already present in stubs — keep them). Compile + pass \`go test ./...\` on Go 1.22+.
- Tests: deterministic, table-driven where natural, use net/http/httptest, NEVER real network, no flaky time.Sleep (inject rng/base; keep backoff base tiny). Use testify (assert/require).
`

const FILE_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['file', 'status', 'notes', 'testFile', 'testCount'],
  properties: {
    file: { type: 'string' },
    status: { type: 'string', enum: ['done', 'blocked'] },
    notes: { type: 'string', description: 'What you implemented, any assumptions, and anything the integrators/reviewers must know.' },
    testFile: { type: 'string', description: 'Path of the test file you wrote (or empty if none required).' },
    testCount: { type: 'number', description: 'Number of test functions written.' },
  },
}

function implPrompt(file, responsibility, cases) {
  return `You are implementing ONE file of the durable-resume v2 refactor. The repo is at ${REPO} and contains compiling contract stubs.

${CONTRACT}

YOUR FILE: ${file}
RESPONSIBILITY: ${responsibility}

STEPS:
1. Read ${REPO}/${file} (the frozen stub) and ALL other stub files in ${REPO}/download/ (and cmd/root.go, download/download.go) you depend on, so you match their exact signatures. Read go.mod.
2. Replace the panic("not implemented") bodies in ${file} with correct, idiomatic, concurrency-safe implementations. Keep every signature EXACTLY. You may add private helpers in this file.
3. Write the matching test file with table-driven, deterministic tests (httptest, testify). Required test cases to cover:\n${cases.map(c => '   - ' + c).join('\n')}
4. Run \`cd ${REPO} && gofmt -w ${file} <yourtestfile> && go vet ./download/...\` (or ./cmd/... ) on the files you touched. Note: the package as a whole will NOT fully build until sibling files are implemented — that's expected; ensure YOUR file has no syntax/type errors against the frozen signatures.
5. Do NOT edit any file other than ${file} and its _test.go. Do NOT change signatures in other files. Do NOT add dependencies.

Return the structured result.`
}

phase('Wave1')
const wave1 = [
  { file: 'download/probe.go', resp: 'Remote inspection: probe(), parseContentRangeTotal(), newRequest(), remoteInfo.streamable().',
    cases: [
      "206 + Content-Range 'bytes 0-0/1000' => size=1000, acceptRanges=true, etag/lastmod captured",
      "200 + Content-Length=1000 => size=1000, acceptRanges=false",
      "Range GET returns 405 => HEAD fallback yields size from Content-Length and acceptRanges from 'Accept-Ranges: bytes'",
      "no Content-Length anywhere => size=-1 (unknown), no error",
      "non-206 streamable response does not hard-fail",
      "parseContentRangeTotal: valid 'bytes 0-0/1000' => 1000,true; 'bytes 0-0/*' => ok=false; malformed missing slash => ok=false",
      "remoteInfo.streamable: true only when acceptRanges && size>0",
      "newRequest sets Range header only when start>=0 && end>=0; forwards caller headers",
    ] },
  { file: 'download/chunk.go', resp: 'chunk type, remaining(), ceilDiv(), planChunks(), fetchChunk() (ranged WriteAt fetch).',
    cases: [
      "planChunks: size divisible by concurrency => equal disjoint contiguous chunks covering [0,size)",
      "planChunks: size with remainder => last chunk carries remainder, ranges gapless and non-overlapping",
      "planChunks: size<concurrency => clamps to fewer chunks, no empty chunks",
      "planChunks: size==0 and concurrency<1 edge cases handled without panic",
      "ceilDiv: (10,3)=4, (9,3)=3, (0,3)=0, (1,1)=1, (a,0)=0",
      "remaining: done=0 => offset=start,todo=true; chunk fully done => todo=false",
      "fetchChunk writes exact served bytes at chunk.start via WriteAt (use httptest range server); verify dst content and onBytes sum",
      "fetchChunk with ch.done>0 issues Range bytes=(start+done)-end and appends correctly",
      "fetchChunk rejects a 200 response when 206 expected => errors.Is(err, ErrRangeNot206)",
    ] },
  { file: 'download/state.go', resp: 'Durable resume sidecar: statePath, LoadState, newState, Save (atomic), Matches, MarkProgress, toChunks, completedBytes, Remove.',
    cases: [
      "Save then LoadState round-trips all fields including per-chunk Done",
      "Save is atomic: no leftover .tmp file afterwards, on-disk JSON valid and parseable",
      "Matches: same size+ETag => true; changed ETag => false; changed size => false; ETag absent on both uses Last-Modified; neither validator present => false",
      "toChunks reconstructs plan with done cursors; completedBytes sums all Done",
      "concurrent MarkProgress from many goroutines is race-free and sums correctly (must pass go test -race)",
      "LoadState on missing file => (nil,nil); on corrupt JSON => (nil,nil)",
      "Remove deletes the sidecar; Remove on missing file => nil",
    ] },
  { file: 'download/retry.go', resp: 'Retry/backoff + error classification: newRetry, backoff, httpStatusError, isRetryable.',
    cases: [
      "op fails transiently twice then succeeds => nil after 3 attempts",
      "op fails fatally (4xx httpStatusError) => returned immediately, exactly 1 attempt",
      "exhausts maxRetries => returns last error",
      "ctx canceled during backoff sleep => returns ctx.Err() promptly (use tiny base + cancel)",
      "backoff: always within [0,max]; grows with attempt; deterministic with injected rng (r constant)",
      "isRetryable: 500/502/503/429 retryable; 400/404 fatal; ErrRemoteChanged fatal; context.Canceled fatal; io.ErrUnexpectedEOF retryable",
    ] },
  { file: 'download/progress.go', resp: 'Concurrency-safe progress + single render goroutine + stdlib TTY detection + formatters.',
    cases: [
      "concurrent Add from many goroutines sums exactly (go test -race)",
      "Seed sets initial count for resume",
      "formatBytes table: B/KiB/MiB/GiB boundaries",
      "formatRate table: B/s, KiB/s, MiB/s",
      "percent: 0, partial, full, total<=0 => 0 (no divide-by-zero)",
      "eta: rate<=0 => 0; normal rate => expected duration",
      "isTTY returns false for a regular temp file",
      "quiet=true OR non-TTY out => Start/Stop write nothing (use a temp file as out and assert empty)",
    ] },
  { file: 'download/verify.go', resp: 'Post-download integrity: verifySize, verifyChecksum (sha256 streaming).',
    cases: [
      "verifySize equal => nil; off-by-one => errors.Is(err, ErrSizeMismatch); expected<=0 => nil (skip)",
      "verifyChecksum matching sha256 => nil",
      "verifyChecksum mismatch => errors.Is(err, ErrChecksumMismatch)",
      "verifyChecksum with empty Checksum => nil (skip)",
    ] },
]

const wave1Results = await parallel(wave1.map(w => () =>
  agent(implPrompt(w.file, w.resp, w.cases), { label: w.file, phase: 'Wave1', schema: FILE_SCHEMA, effort: 'high' })
))
const w1 = wave1Results.filter(Boolean)
log('Wave1 complete: ' + w1.map(r => r.file + '(' + r.status + ',' + r.testCount + 't)').join(' '))

phase('Wave2')
const wave2 = [
  { file: 'download/pool.go', resp: 'Bounded worker pool runSegmented (semaphore + WaitGroup + cancel + periodic Save) and single-stream runSingle. Uses fetchChunk, State, retryFunc.',
    cases: [
      "runSegmented concurrency=4 over httptest range server => output bytes identical to source payload",
      "runSegmented never exceeds `concurrency` in-flight workers (atomic gauge assertion)",
      "ctx cancel mid-download returns a context error promptly and does not hang (no goroutine leak)",
      "a chunk endpoint that always 500s exhausts retries and aborts the group with errors.Is(err, ErrChunkFailed)",
      "runSegmented calls st.Save so the sidecar exists on disk after the run",
      "runSingle streams full body to dst correctly for a no-range server and returns the byte count",
    ] },
  { file: 'download/download.go', resp: 'Run orchestrator + httpClient + Checksum.Empty. Wires probe->strategy->state->plan->progress->pool->verify->cleanup. This is the integrator; read ALL sibling files first.',
    cases: [
      "full ranged download end-to-end: file content equals served payload, sidecar removed on success",
      "no-range server (200, no Accept-Ranges): single-stream fallback produces correct file, NO sidecar created",
      "resume: pre-seed dst (partial bytes) + sidecar with partial Done; server serves remainder via Range; final content correct; dst NOT re-truncated",
      "resume aborts with errors.Is(err, ErrRemoteChanged) when served ETag differs from saved state",
      "size mismatch => errors.Is(err, ErrSizeMismatch), sidecar retained",
      "checksum mismatch on otherwise-good download => errors.Is(err, ErrChecksumMismatch), sidecar retained",
      "ctx cancel mid-download preserves partial file and sidecar (state flushed)",
      "extra Header entries are forwarded to the server",
      "Checksum.Empty: zero value true, set value false; httpClient nil-Client builds a client honoring Timeout",
    ] },
  { file: 'cmd/root.go', resp: 'Thin cobra command + flag wiring + signal/context + helpers (formatVersion, parseHeaders, parseChecksum, defaultOutputName, validateURL).',
    cases: [
      "zero args or >1 positional arg => error",
      "non-http scheme (ftp://, file://) => errors.Is(err, download.ErrUnsupportedScheme)",
      "http and https accepted by validateURL",
      "parseHeaders: 'Key: Value' ok; repeated headers accumulate; 'bogus' (no colon) => error; empty key => error",
      "parseChecksum: 'sha256:<64hex>' ok; '' => empty Checksum + nil err; 'md5:..' => error; odd-length/non-hex => error",
      "defaultOutputName: derived from URL path basename; trailing slash or empty path => 'download'",
      "--version output contains version, revision, date via formatVersion",
      "flag defaults map into download.Options (concurrency=4, retries=3, resume=true, timeout=30s)",
    ] },
]

const wave2Results = await parallel(wave2.map(w => () =>
  agent(implPrompt(w.file, w.resp, w.cases), { label: w.file, phase: 'Wave2', schema: FILE_SCHEMA, effort: 'high' })
))
const w2 = wave2Results.filter(Boolean)
log('Wave2 complete: ' + w2.map(r => r.file + '(' + r.status + ',' + r.testCount + 't)').join(' '))

phase('Gate')
// Fix loop: build/vet/test/race + gofumpt, hand failures to a fixer until green or budget.
const GATE_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['green', 'summary', 'remainingIssues'],
  properties: {
    green: { type: 'boolean', description: 'true only if build+vet+gofmt+go test ./... all pass.' },
    summary: { type: 'string' },
    remainingIssues: { type: 'array', items: { type: 'string' } },
  },
}

let green = false
let gateNotes = ''
for (let i = 0; i < 4 && !green; i++) {
  const r = await agent(
    `You are the BUILD/TEST GATE for the durable-resume v2 refactor at ${REPO}. Round ${i + 1}.

${CONTRACT}

Do the following, fixing any problems you find (you may edit ANY .go file in download/ or cmd/, but must NOT change the frozen exported/unexported signatures unless a signature is genuinely self-inconsistent — if so, fix minimally and note it; NEVER add dependencies):
1. cd ${REPO}
2. Run: gofmt -l . ; if non-empty, gofmt -w . the listed files.
3. Run: go build ./...   — fix all compile errors.
4. Run: go vet ./...     — fix all vet issues.
5. Run: go test ./...    — fix all failing tests. If a test is wrong (contradicts the contract) fix the TEST; if the impl is wrong, fix the IMPL.
6. Run: go test -race ./...  — fix any data races.
7. If gofumpt is available (go run mvdan.cc/gofumpt@latest -l . OR gofumpt -l .), apply it; if not installed, just ensure gofmt-clean.
Iterate until everything passes. Report green=true ONLY if build, vet, gofmt, and \`go test ./...\` all pass cleanly. List any remaining issues precisely.`,
    { label: `gate:round${i + 1}`, phase: 'Gate', schema: GATE_SCHEMA, effort: 'high' }
  )
  if (!r) break
  green = r.green
  gateNotes = r.summary + (r.remainingIssues && r.remainingIssues.length ? '\nRemaining: ' + r.remainingIssues.join('; ') : '')
  log(`Gate round ${i + 1}: green=${green}. ${r.summary}`)
  if (green) break
}

return { wave1: w1, wave2: w2, green, gateNotes }
