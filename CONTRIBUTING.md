# Contributing

Thanks for your interest in instacrawl.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
go mod tidy
git diff --exit-code -- go.mod go.sum
```

Or run `make check`.

## Scope — please read before contributing

instacrawl archives the account owner's **own** Instagram data, locally. It does
not implement — and contributions must not add — ban-evasion, proxy rotation,
multi-account automation, or challenge/CAPTCHA solving. On any login challenge
the tool **stops** and hands the challenge URL to the user; it never tries to
defeat it. See `DECISIONS.md` §5.

## Conventions

- **Conventional Commits** (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`).
- Keep secrets out of logs, errors, and test fixtures — run the `session.Sanitize`
  redaction path on anything that may have touched a request or response, and use
  dummy values in fixtures only.
- Every response from every source lane is classified by the four-state gate
  before a single row is committed. A 200 is not success.
- New fetched tables carry a `source` column (the reliability lane tag) so the
  multi-source reconciler can merge web/mobile/carve rows.

## Compatibility

Pre-1.0: APIs may still change. Prefer additive changes.
