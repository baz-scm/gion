# Read one file from chromium at a pinned commit; the folder stays empty.
Output docs/assets/demos/cat.mp4
Output docs/assets/demos/cat.gif
Set Height 400
Source demos/_setup.tape

Type "ls -A"
Enter
Sleep 600ms
Type "gion cat chromium/chromium abc2bc3fe6dd:README.md | head -8"
Sleep 300ms
Enter
Wait
Sleep 1500ms
Type "ls -A"
Enter
Sleep 2s
