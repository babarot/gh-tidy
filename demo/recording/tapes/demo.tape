# The recording in the README: gh tidy against a made-up world (ADR-010).
# make demo records it into docs/demo.gif, which the README shows.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1400
Set Height 820
Set Padding 16
Set TypingSpeed 60ms
Set Theme "Catppuccin Mocha"

Hide
Type `w=${TMPDIR:-/tmp}/gh-tidy-demo-world && rm -rf "$w" && go build -o demo/recording/out/recording ./demo/recording && ./demo/recording/out/recording build "$w" && gh() { ./demo/recording/out/recording run "$w"; } && echo world-$((40+2))`
Enter
Wait+Screen@90s /world-42/
Type "clear"
Enter
Sleep 300ms
Show

# Open: the clones in the order of what needs doing.
Type "gh tidy"
Sleep 400ms
Enter
Wait+Screen /can update/
Sleep 2.5s

# P: every default branch moved forward, the work in the activity box.
Type "P"
Wait+Screen /update all/
Sleep 1.5s
Type "y"
Wait+Screen /\[\+\] Updat/
Sleep 3s
Wait+Screen /Updated/
Sleep 1.5s

# d on agent-runner: the worktree done goes, the one being worked on stays.
Type "g"
Sleep 600ms
Type "d"
Wait+Screen /clean up agent-runner/
Sleep 2.5s
Type "y"
Sleep 2.5s

# esc puts the results away, so that the rows take their order again.
Escape
Sleep 800ms

# v: the github columns. The cursor rests on agent-runner, with a new
# issue; then calendar-sync, with alerts and failing checks.
Type "v"
Wait+Screen /Retry the runner/
Sleep 3s
Type "g"
Wait+Screen /ical-parser/
Sleep 3.5s

Type "q"
Sleep 500ms
