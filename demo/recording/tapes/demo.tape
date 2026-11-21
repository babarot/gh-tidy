# The recording in the README: gh tidy against a made-up world (ADR-010).
# make demo records it into demo/recording/out/demo.gif.

Output demo/recording/out/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1400
Set Height 820
Set Padding 16
Set TypingSpeed 60ms
Set Theme "Catppuccin Mocha"

Hide
Type "go build -o demo/recording/out/recording ./demo/recording && gh() { ./demo/recording/out/recording; } && clear"
Enter
Wait
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
