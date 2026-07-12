# Repository instructions for AI agents

AI tools may modify and test the code, but they must never add themselves or
their vendors, models, sessions, bots, or service accounts to Git metadata.

- Keep the commit author and committer exactly as
  `Droponevedimka <34841931+Droponevedimka@users.noreply.github.com>`.
- Never add attribution trailers or markers such as `Co-Authored-By`,
  `Generated-By`, `Assisted-By`, `Signed-off-by`, or session URLs.
- Never change `git user.name`, `git user.email`, author, or committer values.
- Never bypass repository hooks with `--no-verify`.
- Before committing or pushing, run
  `pwsh -File tools/check-clean-contributors.ps1`.
- If an AI client cannot disable automatic attribution, leave the changes
  uncommitted for the repository owner.

These rules apply to every agent, subagent, editor assistant, and automated
commit workflow.
