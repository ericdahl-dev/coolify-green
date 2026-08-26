package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var helpStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	Padding(1, 2).
	BorderForeground(lipgloss.Color("212"))

const helpMarkdown = `# coolify-green · keybindings

| Key | Action |
|-----|--------|
| **↑** / **k** | Move up |
| **↓** / **j** | Move down |
| **enter** / **space** | Expand or collapse a project, or a resource's deploy log |
| **f** | Smart fix (redeploy / start / restart / cancel) |
| **o** | Open the selected row in Coolify |
| **r** | Force refresh |
| **m** | Manage what is monitored |
| **?** | Toggle this help |
| **esc** | Close help |
| **q** / **ctrl+c** | Quit |

Projects and their resources are discovered from the Coolify API — there is no
resource list to maintain. Use **m** to mute anything you do not want watched.
`

// RenderHelp renders markdown help for the given terminal width.
func RenderHelp(width int) string {
	if width < 30 {
		width = 80
	}
	innerW := width - 4
	if innerW < 40 {
		innerW = 72
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(innerW),
		glamour.WithStandardStyle("dark"),
	)
	if err != nil {
		return helpStyle.Render(fallbackHelpText())
	}
	out, err := r.Render(helpMarkdown)
	if err != nil {
		return helpStyle.Render(fallbackHelpText())
	}
	return helpStyle.Render(strings.TrimRight(out, "\n"))
}

func fallbackHelpText() string {
	return `coolify-green keybindings

  ↑ / k          move up
  ↓ / j          move down
  enter / space  expand/collapse project or deploy log
  f              smart fix (redeploy / start / restart / cancel)
  o              open the selected row in Coolify
  r              force refresh
  m              manage what is monitored
  ?              toggle this help
  esc            close help
  q / ctrl+c     quit`
}
