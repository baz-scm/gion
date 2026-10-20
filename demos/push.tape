# Edit part of a repo and push a branch. record.sh screenshots and deletes it.
Output docs/assets/demos/push.mp4
Output docs/assets/demos/push.gif
Set Height 380
Source demos/_setup.tape

Type "gion clone baz-scm/gion ./gion --path docs --ref main && cd gion"
Sleep 300ms
Enter
Wait
Sleep 600ms
Type "echo 'Hello from gion.' >> docs/index.md"
Enter
Sleep 300ms
Type "gion status"
Enter
Wait
Sleep 800ms
Type "gion commit -m 'docs: demo edit' --branch demo/recording"
Sleep 300ms
Enter
Wait
Sleep 2500ms
