# Repository Standards

Use these standards when contributing to this repository.

## Core Rules

- Make minimal, safe, maintainable changes.
- Keep behavior and terminology consistent across controllers, webhooks, and APIs.
- Follow existing package boundaries and naming conventions.
- Prefer small, focused functions and single-purpose helpers.
- Add comments only for non-obvious intent or tradeoffs.

## Go References

- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)

## Repository References

- [Go Style Best Practices Summary](../../go-best-practices-summary.md)

## Testing Expectations

- Add or update tests for changed behavior.
- Keep unit tests behavior-focused and deterministic.
- Write test names/descriptions around observable behavior and contracts, not internal implementation details.
