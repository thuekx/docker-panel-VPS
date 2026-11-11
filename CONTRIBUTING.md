# Contributing to Nostek Panel

Thanks for your interest. The project is in its design phase; the roadmap shows what comes next.

- Open an issue first for anything bigger than a typo, so we can agree on the approach.
- Branches: `step/<id>-<slug>` from `main` (for example `step/site-01-wordpress-install`).
- Shell scripts must pass `shellcheck -S warning`.
- Never commit secrets: no passwords, tokens, keys or `.env` files. Test fixtures must be obviously fake.
- Do not put real server addresses, user names or SSH ports in the repository.
- Use only our own product names in code, UI and docs; do not copy other vendors' names or artwork.
- UI text lives in locale files.

By contributing you agree that your contribution is licensed under the MIT licence.
