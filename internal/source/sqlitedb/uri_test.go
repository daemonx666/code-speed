package sqlitedb

import (
	"net/url"
	"testing"
)

func TestWindowsPathsMakeFileURIs(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`C:\Users\dev\AppData\Roaming\Cursor\state.vscdb`, "file:///C:/Users/dev/AppData/Roaming/Cursor/state.vscdb"},
		{`C:\Users\dev\My Data\x#1.db`, "file:///C:/Users/dev/My%20Data/x%231.db"},
		{`\\server\share\a.db`, "file:////server/share/a.db"},
	} {
		u := url.URL{Scheme: "file", Path: uriPath(c.in, true)}
		if got := u.String(); got != c.want {
			t.Errorf("%s: %s, want %s", c.in, got, c.want)
		}
	}
	if got := uriPath("/Users/dev/a.db", false); got != "/Users/dev/a.db" {
		t.Errorf("unix path changed: %s", got)
	}
}
