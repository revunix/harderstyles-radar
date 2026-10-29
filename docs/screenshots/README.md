# Screenshots

Pictures of the interface, rendered from the terminal output into the font
FiraCode Nerd Font Regular, in the dark scheme, in English, at 112 columns ×
38 rows (the narrow one at 82 × 26).

| File | Screen |
|------|--------|
| `00-intro` | the intro with the hardstyle.com and Spotify logos |
| `01-run` | a running sync: releases, details, log and playlist |
| `02-genre` | the releases filtered by genre |
| `03-done` | the end of a run with its numbers and the next run |
| `04-dry-run` | a dry run, which writes nothing |
| `05-log` | the enlarged log |
| `06-help` | the help with all keys |
| `07-blocked` | Spotify blocking the app, with the time it ends |
| `08-narrow` | a narrow terminal with the list alone |

## Regenerating

The pictures do not come from a terminal program. A Go test fills the model
with demo data and renders `View()` of each screen into an `.ans` file; the
helper in [`tools/ansi2png`](../../tools/ansi2png) draws the ANSI output as PNG.

1. Write the `.ans` files:

   ```sh
   SCREENSHOT_DIR="$PWD/docs/screenshots" go test ./internal/tui -run TestScreenshots -count=1
   ```

2. Convert every file, 112 columns (82 for the narrow one), with FiraCode
   Nerd Font Regular from the system:

   ```sh
   for f in docs/screenshots/*.ans; do
     name=$(basename "$f" .ans)
     width=112
     [ "$name" = "08-narrow" ] && width=82
     go run ./tools/ansi2png "$f" "docs/screenshots/$name.png" \
       "$HOME/Library/Fonts/FiraCodeNerdFont-Regular.ttf" "$width"
   done
   ```

The `.ans` files are only an intermediate step and are ignored by git.
