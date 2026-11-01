# TokenManager dashboard tour. Record with: vhs token.tape
# Full command reference: https://github.com/charmbracelet/vhs

Output docs/demo.gif

Require tokenmanager

Set Shell "bash"
Set Theme "Catppuccin Mocha"
Set FontSize 18
Set Width 1600
Set Height 900
Set Padding 24
Set LineHeight 1.15
Set Framerate 30
Set TypingSpeed 60ms
Set WindowBar Colorful
Set BorderRadius 10
Set Margin 20
Set MarginFill "#1e1e2e"
# Start the loop on the dashboard, not the empty prompt
Set LoopOffset 15%

# Warm the database off-camera so the first frame isn't a scan
Hide
Type "tokenmanager scan >/dev/null 2>&1; clear" Enter
Sleep 2s
Show

Type "tokenmanager" Sleep 400ms Enter
Sleep 2s
# Default is "this month"; step to last 30 days so every view has data
Type "f" Sleep 2.5s

# 2 Usage
Type "2" Sleep 2.5s
# 3 Agents
Type "3" Sleep 2.5s
# 4 Sessions: move down and open one
Type "4" Sleep 1.5s
Type@300ms "jj" Sleep 500ms
Enter Sleep 2.5s
Escape Sleep 800ms
# 6 Models
Type "6" Sleep 2.5s
# 7 Optimize: the selling point, linger here
Type "7" Sleep 4s

# End back on the Overview so the loop restarts cleanly
Type "1" Sleep 3s
Hide
Type "q"
