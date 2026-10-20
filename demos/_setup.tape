# Shared settings for every demo; each tape Sources this first.
Require gion

Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 18
Set Width 900
Set Padding 24
Set Margin 0
Set WindowBar Colorful
Set BorderRadius 10
Set TypingSpeed 35ms
Set Framerate 30
Set WaitTimeout 60s
Set WaitPattern /^\$\s*$/
Set Theme { "name": "Baz", "background": "#15171f", "foreground": "#e9ebf1", "selection": "#2f3449", "cursor": "#949feb", "black": "#1d202c", "red": "#f3654a", "green": "#7cc7aa", "yellow": "#e8c07d", "blue": "#949feb", "magenta": "#c5ccfc", "cyan": "#7cc7aa", "white": "#cacedc", "brightBlack": "#646a8a", "brightRed": "#f3654a", "brightGreen": "#7cc7aa", "brightYellow": "#e8c07d", "brightBlue": "#adb6f5", "brightMagenta": "#c5ccfc", "brightCyan": "#7cc7aa", "brightWhite": "#f8f9fb" }

Hide
Type "export PS1='\[\e[38;2;148;159;235m\]$\[\e[0m\] ' && cd $(mktemp -d) && clear"
Enter
Show
Sleep 400ms
