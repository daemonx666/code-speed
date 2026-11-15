package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/library"
)

// listLibrary prints the skill and MCP library (read only; the app's
// "Skills & MCP" window changes it): each item, the targets it is given
// to and its state in each, and what the agents hold that isn't in it.
func listLibrary(out io.Writer) int {
	st, err := library.New(appdir.Dir()).State()
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-speed:", err)
		return 1
	}
	labels := map[string]string{}
	for _, t := range st.Targets {
		l := t.Label
		if t.Project != "" {
			l += " (" + t.Project + ")"
		}
		labels[t.ID] = l
	}
	section := func(title string, items []*library.Item) {
		fmt.Fprintf(out, "%s (%d)\n", title, len(items))
		for _, it := range items {
			var parts []string
			for id, c := range it.Cells {
				if c.Want || c.State == library.CellDrift || c.State == library.CellRemove {
					p := labels[id]
					if c.State != library.CellOn {
						p += " [" + c.State + "]"
					}
					parts = append(parts, p)
				}
			}
			sort.Strings(parts)
			if len(parts) == 0 {
				parts = []string{"—"}
			}
			fmt.Fprintf(out, "  %-24s %s\n", it.Name, strings.Join(parts, ", "))
		}
	}
	section("MCP servers", st.MCP)
	section("Skills", st.Skills)
	if len(st.Found) > 0 {
		fmt.Fprintf(out, "Not in the library (%d)\n", len(st.Found))
		for _, f := range st.Found {
			var in []string
			for _, id := range f.In {
				in = append(in, labels[id])
			}
			c := ""
			if f.Conflict {
				c = "  [conflict]"
			}
			fmt.Fprintf(out, "  %-5s %-24s %s%s\n", f.Kind, f.Name, strings.Join(in, ", "), c)
		}
	}
	if st.Pending > 0 {
		fmt.Fprintf(out, "%d changes wait for Apply in the app's Skills & MCP window.\n", st.Pending)
	}
	return 0
}
