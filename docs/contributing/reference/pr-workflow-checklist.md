# PR Workflow Checklist

Use this checklist before requesting review.

## Scope

- Keep each PR to one logical change.
- Avoid combining broad refactors with new behavior in one PR unless tightly coupled.

## Required Local Checks

```bash
make lint
make test
```

## Conditional Local Checks

- Run `make manifests` when CRDs, RBAC, or webhook configuration changes.
- Run `make test-e2e` when touching end-to-end behavior, integration flow, or deployment wiring.

## PR Description Checklist

- Explain what changed and why.
- Call out any breaking behavior or migration impact.
- Confirm tests added/updated for new or changed behavior.
- Ensure CI is green before requesting review.
