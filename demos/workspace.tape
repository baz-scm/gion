# Check out only chromium's docs folder at a pinned commit, edit, diff.
Output docs/assets/demos/workspace.mp4
Output docs/assets/demos/workspace.gif
Set Height 460
Source demos/_setup.tape

Type "gion clone chromium/chromium ./chromium --path docs --ref abc2bc3fe6dd && cd chromium"
Sleep 300ms
Enter
Wait
Sleep 800ms
Type "ls -A"
Enter
Sleep 1500ms
Type "echo 'hello' >> docs/README.md && gion diff"
Sleep 300ms
Enter
Wait
Sleep 2500ms
