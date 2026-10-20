# Pull upstream changes into a workspace. record.sh creates demo/sync at an
# older commit first; the hidden step moves it forward, like a teammate pushing.
Output docs/assets/demos/sync.mp4
Output docs/assets/demos/sync.gif
Set Height 380
Source demos/_setup.tape

Type "gion clone baz-scm/gion ./gion --path docs --ref demo/sync && cd gion"
Sleep 300ms
Enter
Wait
Sleep 600ms
Hide
Type "gh api -X PATCH repos/baz-scm/gion/git/refs/heads/demo/sync -f sha=$(gh api repos/baz-scm/gion/commits/b6b40a09dd5c -q .sha) -F force=true >/dev/null && clear"
Enter
Wait
Type "clear"
Enter
Show
Type "gion sync"
Sleep 300ms
Enter
Wait
Sleep 800ms
Type "gion log -n 3 --oneline"
Enter
Wait
Sleep 2500ms
