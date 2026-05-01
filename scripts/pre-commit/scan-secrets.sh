#!/usr/bin/env bash

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Use `read` loops instead of `mapfile` so the script runs on the bash 3.2
# that ships with macOS (`/usr/bin/env bash`).
files=()
if [[ $# -gt 0 ]]; then
  while IFS= read -r line; do
    [[ -n "$line" ]] && files+=("$line")
  done < <(printf '%s\n' "$@")
else
  while IFS= read -r line; do
    files+=("$line")
  done < <(git diff --cached --name-only --diff-filter=ACMR)
fi

if [[ ${#files[@]} -eq 0 ]]; then
  exit 0
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

read -r -d '' secret_rules <<'EOF' || true
private key	-----BEGIN (RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----
github token	(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})
aws key	(AKIA|ASIA)[0-9A-Z]{16}
google api key	AIza[0-9A-Za-z_-]{35}
slack token	xox[baprs]-[A-Za-z0-9-]{10,}
stripe live key	sk_live_[0-9A-Za-z]+
EOF

failed=0

for file in "${files[@]}"; do
  if git cat-file -e ":$file" 2>/dev/null; then
    content_path="$tmp_dir/$(echo "$file" | tr '/ ' '__')"
    git show ":$file" >"$content_path"
  elif [[ -f "$file" ]]; then
    content_path="$file"
  else
    continue
  fi

  if ! LC_ALL=C grep -Iq . "$content_path"; then
    continue
  fi

  while IFS=$'\t' read -r label regex; do
    [[ -n "$label" ]] || continue

    lines=()
    while IFS= read -r line_no; do
      [[ -n "$line_no" ]] && lines+=("$line_no")
    done < <(LC_ALL=C grep -nE -- "$regex" "$content_path" | cut -d: -f1 | sort -u)
    if [[ ${#lines[@]} -eq 0 ]]; then
      continue
    fi

    failed=1
    printf 'Potential %s detected in %s at line(s): %s\n' \
      "$label" \
      "$file" \
      "$(IFS=,; echo "${lines[*]}")" >&2
  done <<<"$secret_rules"
done

if [[ $failed -ne 0 ]]; then
  cat >&2 <<'EOF'

Commit blocked by secret-pattern-scan.
If the match is a real secret, remove it from the commit and rotate it if necessary.
If the file is intentionally sensitive local material, move it outside the repository or keep it ignored and unstaged.
EOF
  exit 1
fi
